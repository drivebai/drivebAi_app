// Package billofsale renders a signed Vehicle Bill of Sale to PDF bytes.
//
// The renderer is a pure function of its inputs (Data + on-disk signature
// PNG paths) so it is fully unit-testable without a database, filesystem
// fixtures beyond the signature images, or any network. The caller (the
// purchase handler) is responsible for loading the canonical row, resolving
// signature URLs to disk paths, persisting the output, and broadcasting.
//
// Deliberately NEUTRAL: the document carries no MV-912 reference, no state
// form number, and makes no state-compliance claim. It is a generic record
// of a private sale between two named parties.
package billofsale

import (
	"bytes"
	"fmt"
	"image"
	_ "image/png" // registers the PNG decoder used by validateSignaturePNG
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-pdf/fpdf"
)

// maxSignatureBytes caps how much of a signature file we will read while
// validating its header. Mirrors the upload limit enforced by the handler.
const maxSignatureBytes = 5 << 20

// Data is the canonical, presentation-ready payload for one Bill of Sale.
// All monetary values are in cents; all dates are pre-formatted strings so
// the renderer never touches time.Now or a locale.
type Data struct {
	// Transaction
	ReferenceID   string // purchase request id
	GeneratedDate string // e.g. "2006-01-02"

	// Vehicle
	VehicleYear  int
	VehicleMake  string
	VehicleModel string
	VIN          string
	Mileage      string // optional; rendered only when non-empty

	// Terms
	SalePriceCents int64
	Currency       string
	Terms          string

	// Seller
	SellerName          string
	SellerAddress       string
	SellerSignaturePath string // disk path to PNG; empty = not embedded
	SellerSignedAt      string

	// Buyer
	BuyerName          string
	BuyerAddress       string
	BuyerSignaturePath string // disk path to PNG; empty = not embedded
	BuyerSignedAt      string
}

const (
	marginLeft  = 20.0
	marginTop   = 20.0
	marginRight = 20.0
	// contentWidth is the usable text width on US Letter (215.9mm) with the
	// left/right margins above.
	contentWidth = 215.9 - marginLeft - marginRight
	// signatureWidth is the embedded signature-image width in mm. Height is
	// passed as 0 so fpdf preserves each PNG's aspect ratio.
	signatureWidth = 70.0
)

// maxSignaturePixels bounds each signature image's decoded dimensions. A
// hand-drawn signature is a few hundred pixels wide; anything past this is
// either a mistake or an attack payload.
const maxSignatureDimension = 8000

// validateSignaturePNG rejects anything that is not a small, well-formed PNG
// *before* fpdf is allowed to parse it.
//
// This is not defensive paranoia. fpdf's PNG reader trusts the IHDR chunk: a
// file with correct PNG magic but a bogus width/height makes it allocate from
// the declared dimensions and then panic ("short buffer") rather than return
// an error. image.DecodeConfig reads the same header safely and cheaply, so we
// use it as the gate. Validating here — rather than only at upload — also
// protects rows whose signature files were written before this check existed.
func validateSignaturePNG(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("signature image %q: %w", filepath.Base(path), err)
	}
	defer f.Close()

	cfg, format, err := image.DecodeConfig(io.LimitReader(f, maxSignatureBytes))
	if err != nil {
		return fmt.Errorf("signature image %q: not a decodable image: %w", filepath.Base(path), err)
	}
	if format != "png" {
		return fmt.Errorf("signature image %q: expected png, got %s", filepath.Base(path), format)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 ||
		cfg.Width > maxSignatureDimension || cfg.Height > maxSignatureDimension {
		return fmt.Errorf("signature image %q: implausible dimensions %dx%d", filepath.Base(path), cfg.Width, cfg.Height)
	}
	return nil
}

// Render lays out the Bill of Sale and returns the PDF as bytes.
//
// It returns a non-nil error when the document cannot be produced — most
// importantly when a non-empty signature path does not resolve to a
// readable PNG on disk. Callers treat that as a controlled failure: the
// signatures and purchase status remain valid, finalized_pdf_url stays NULL,
// and a retry can regenerate later. Long free-text terms/addresses wrap via
// MultiCell and never overflow or crash.
//
// Render never panics: a panic from the underlying PDF library is converted
// into an error. Generation runs on a detached goroutine, where an escaping
// panic would take the whole process down.
func Render(d Data) (out []byte, err error) {
	defer func() {
		if r := recover(); r != nil {
			out, err = nil, fmt.Errorf("bill of sale: render panicked: %v", r)
		}
	}()
	// An empty path means "no signature image to embed" and is legal; a
	// non-empty path must resolve to a sane PNG.
	for _, path := range []string{d.SellerSignaturePath, d.BuyerSignaturePath} {
		if strings.TrimSpace(path) == "" {
			continue
		}
		if err := validateSignaturePNG(path); err != nil {
			return nil, err
		}
	}
	return render(d, true)
}

// render is the implementation of Render with an explicit compression toggle.
// Production always compresses; tests pass compress=false so page-content
// text (vehicle/party/terms fields) is readable in the raw bytes for content
// assertions.
func render(d Data, compress bool) ([]byte, error) {
	pdf := fpdf.New("P", "mm", "Letter", "")
	pdf.SetCompression(compress)
	pdf.SetMargins(marginLeft, marginTop, marginRight)
	pdf.SetAutoPageBreak(true, marginTop)
	pdf.AddPage()

	// ── Header ───────────────────────────────────────────────────────────
	pdf.SetFont("Helvetica", "B", 20)
	pdf.SetTextColor(20, 20, 20)
	pdf.CellFormat(contentWidth, 12, "Vehicle Bill of Sale", "", 1, "C", false, 0, "")

	// Neutral jurisdiction note — generic, no state-form / MV-912 reference.
	pdf.SetFont("Helvetica", "I", 9)
	pdf.SetTextColor(120, 120, 120)
	pdf.MultiCell(contentWidth, 5,
		"This document records a private vehicle sale between the parties named below. "+
			"It is not a government form and makes no representation of compliance with any "+
			"state or local titling requirement.", "", "C", false)
	pdf.Ln(4)
	pdf.SetTextColor(20, 20, 20)

	// ── Transaction ──────────────────────────────────────────────────────
	sectionHeader(pdf, "Transaction")
	keyValue(pdf, "Reference", d.ReferenceID)
	keyValue(pdf, "Date", d.GeneratedDate)

	// ── Vehicle ──────────────────────────────────────────────────────────
	sectionHeader(pdf, "Vehicle")
	vehicle := strings.TrimSpace(fmt.Sprintf("%d %s %s", d.VehicleYear, d.VehicleMake, d.VehicleModel))
	keyValue(pdf, "Description", vehicle)
	keyValue(pdf, "VIN", d.VIN)
	if strings.TrimSpace(d.Mileage) != "" {
		keyValue(pdf, "Mileage", d.Mileage)
	}

	// ── Seller ───────────────────────────────────────────────────────────
	sectionHeader(pdf, "Seller")
	keyValue(pdf, "Name", d.SellerName)
	keyValueWrap(pdf, "Address", d.SellerAddress)

	// ── Buyer ────────────────────────────────────────────────────────────
	sectionHeader(pdf, "Buyer")
	keyValue(pdf, "Name", d.BuyerName)
	keyValueWrap(pdf, "Address", d.BuyerAddress)

	// ── Terms ────────────────────────────────────────────────────────────
	sectionHeader(pdf, "Terms")
	keyValue(pdf, "Sale price", formatMoney(d.SalePriceCents, d.Currency))
	keyValueWrap(pdf, "Conditions", d.Terms)

	// ── Signatures ───────────────────────────────────────────────────────
	pdf.Ln(6)
	sectionHeader(pdf, "Signatures")
	signatureBlock(pdf, "Seller", d.SellerName, d.SellerSignedAt, d.SellerSignaturePath)
	pdf.Ln(6)
	signatureBlock(pdf, "Buyer", d.BuyerName, d.BuyerSignedAt, d.BuyerSignaturePath)

	// fpdf accumulates errors internally (e.g. a signature PNG that could
	// not be opened) and surfaces them on Output. Return them to the caller
	// rather than emitting a truncated/invalid PDF.
	if err := pdf.Error(); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// sectionHeader draws a bold section title with a thin rule beneath it.
func sectionHeader(pdf *fpdf.Fpdf, title string) {
	pdf.Ln(2)
	pdf.SetFont("Helvetica", "B", 12)
	pdf.SetTextColor(20, 20, 20)
	pdf.CellFormat(contentWidth, 7, title, "B", 1, "L", false, 0, "")
	pdf.Ln(1)
	pdf.SetFont("Helvetica", "", 11)
}

// labelWidth is the fixed column width for key/value rows.
const labelWidth = 38.0

// keyValue renders a single-line "Label: value" row.
func keyValue(pdf *fpdf.Fpdf, label, value string) {
	pdf.SetFont("Helvetica", "B", 10)
	pdf.CellFormat(labelWidth, 6, label, "", 0, "L", false, 0, "")
	pdf.SetFont("Helvetica", "", 10)
	pdf.CellFormat(contentWidth-labelWidth, 6, value, "", 1, "L", false, 0, "")
}

// keyValueWrap renders a "Label" then a wrapping value block for long text
// (addresses, terms) so it never overflows the page width.
func keyValueWrap(pdf *fpdf.Fpdf, label, value string) {
	pdf.SetFont("Helvetica", "B", 10)
	pdf.CellFormat(labelWidth, 6, label, "", 0, "L", false, 0, "")
	pdf.SetFont("Helvetica", "", 10)
	x := pdf.GetX()
	y := pdf.GetY()
	pdf.SetXY(x, y)
	pdf.MultiCell(contentWidth-labelWidth, 6, value, "", "L", false)
}

// signatureBlock draws one signer's embedded signature image (if a path is
// given) plus the signer name and signed-at line. The image is placed with
// height 0 so fpdf preserves the PNG aspect ratio from its width alone.
func signatureBlock(pdf *fpdf.Fpdf, role, name, signedAt, path string) {
	pdf.SetFont("Helvetica", "B", 10)
	pdf.CellFormat(contentWidth, 6, role, "", 1, "L", false, 0, "")
	if strings.TrimSpace(path) != "" {
		x := pdf.GetX()
		y := pdf.GetY()
		pdf.ImageOptions(path, x, y, signatureWidth, 0, true,
			fpdf.ImageOptions{ImageType: "PNG", ReadDpi: false}, 0, "")
	}
	pdf.SetFont("Helvetica", "", 9)
	pdf.SetTextColor(80, 80, 80)
	line := name
	if strings.TrimSpace(signedAt) != "" {
		line = strings.TrimSpace(name + "  ·  Signed " + signedAt)
	}
	pdf.CellFormat(contentWidth, 5, line, "T", 1, "L", false, 0, "")
	pdf.SetTextColor(20, 20, 20)
}

// formatMoney renders cents as a currency string, e.g. 50000 → "$500.00 USD".
func formatMoney(cents int64, currency string) string {
	neg := cents < 0
	if neg {
		cents = -cents
	}
	dollars := cents / 100
	rem := cents % 100
	// Group the integer part with thousands separators.
	intStr := fmt.Sprintf("%d", dollars)
	var grouped strings.Builder
	n := len(intStr)
	for i, ch := range intStr {
		if i > 0 && (n-i)%3 == 0 {
			grouped.WriteByte(',')
		}
		grouped.WriteRune(ch)
	}
	cur := strings.TrimSpace(currency)
	if cur == "" {
		cur = "USD"
	}
	sign := ""
	if neg {
		sign = "-"
	}
	return fmt.Sprintf("%s$%s.%02d %s", sign, grouped.String(), rem, cur)
}
