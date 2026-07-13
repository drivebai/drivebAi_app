// Package billofsale renders a signed Vehicle Bill of Sale to PDF bytes.
//
// The renderer is a pure function of its inputs (Data + on-disk signature
// PNG paths) so it is fully unit-testable without a database, filesystem
// fixtures beyond the signature images, or any network. The caller (the
// purchase handler) is responsible for loading the canonical row, resolving
// signature URLs to disk paths, persisting the output, and broadcasting.
//
// The layout MIRRORS the familiar field structure of a state motor-vehicle
// bill-of-sale (preamble sentence, "Description of Vehicle", "Terms and
// Conditions", separate Seller/Buyer blocks) so it reads as a complete,
// recognisable record. It is DELIBERATELY NOT a government form: it carries
// NO state seal or logo, NO "MV-912"/state form number, NO state web address,
// and makes NO claim of compliance with any titling authority. A codified
// disclaimer under the title states plainly that this is a private record.
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

	// TitleConditionLabel is the human-readable title brand (e.g. "Clean",
	// "Salvage", "Other: <detail>"). Empty when the seller has not declared
	// one yet — rendered as an em dash.
	TitleConditionLabel string

	// Terms
	SalePriceCents int64
	Currency       string
	Terms          string

	// Seller
	SellerName          string
	SellerAddress       string
	SellerSignaturePath string // disk path to PNG; empty = not embedded
	SellerSignedAt      string
	// SellerIDOnFile prints a neutral "Government ID: on file" acknowledgement.
	// The ID image itself is sensitive PII and is NEVER embedded in the PDF —
	// it is only ever exposed via signed, in-app URLs.
	SellerIDOnFile bool

	// Buyer
	BuyerName          string
	BuyerAddress       string
	BuyerSignaturePath string // disk path to PNG; empty = not embedded
	BuyerSignedAt      string
	BuyerIDOnFile      bool
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
	// labelWidth is the fixed column width for "Label  value" party rows.
	labelWidth = 34.0
)

// maxSignatureDimension bounds each signature image's decoded dimensions. A
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

// doc bundles the fpdf handle with a cp1252 unicode translator so every piece
// of user-facing text (which may contain an em dash, middle dot, or accented
// address) is encoded correctly for the built-in core fonts.
type doc struct {
	pdf *fpdf.Fpdf
	tr  func(string) string
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

	dc := &doc{pdf: pdf, tr: pdf.UnicodeTranslatorFromDescriptor("")}

	// Footer on every page: neutral transaction reference + generation date.
	footer := "Transaction reference: " + valueOrDash(d.ReferenceID) +
		"   ·   Generated: " + valueOrDash(d.GeneratedDate)
	pdf.SetFooterFunc(func() {
		pdf.SetY(-15)
		pdf.SetFont("Helvetica", "I", 8)
		pdf.SetTextColor(120, 120, 120)
		pdf.SetDrawColor(200, 200, 200)
		pdf.CellFormat(contentWidth, 8, dc.tr(footer), "T", 0, "C", false, 0, "")
		pdf.SetTextColor(20, 20, 20)
	})

	pdf.AddPage()

	// ── Header ───────────────────────────────────────────────────────────
	pdf.SetFont("Helvetica", "B", 20)
	pdf.SetTextColor(20, 20, 20)
	pdf.CellFormat(contentWidth, 12, "Vehicle Bill of Sale", "", 1, "C", false, 0, "")

	// Codified private-record disclaimer — no state name, seal, or form number.
	pdf.SetFont("Helvetica", "I", 8.5)
	pdf.SetTextColor(110, 110, 110)
	pdf.MultiCell(contentWidth, 4.6,
		dc.tr("This is not an official government form. It is a private record of a vehicle sale "+
			"between the parties named below. Title, registration and tax requirements vary by "+
			"jurisdiction."), "", "C", false)
	pdf.Ln(3)
	pdf.SetTextColor(20, 20, 20)

	// ── Preamble (bill-of-sale style conveyance sentence) ────────────────
	pdf.SetFont("Helvetica", "", 11)
	preamble := fmt.Sprintf(
		"I, %s, in consideration of %s, do hereby sell, transfer and convey to %s, the following vehicle:",
		nameOrBlank(d.SellerName),
		formatMoney(d.SalePriceCents, d.Currency),
		nameOrBlank(d.BuyerName),
	)
	pdf.MultiCell(contentWidth, 6, dc.tr(preamble), "", "L", false)
	pdf.Ln(2)

	// ── Description of Vehicle (bordered) ────────────────────────────────
	dc.sectionBar("DESCRIPTION OF VEHICLE")
	dc.vehicleDescription(d)

	// ── Terms and Conditions (bordered box) ──────────────────────────────
	dc.sectionBar("TERMS AND CONDITIONS")
	pdf.SetFont("Helvetica", "", 10)
	pdf.SetDrawColor(180, 180, 180)
	pdf.MultiCell(contentWidth, 5.5, dc.tr(valueOrDash(d.Terms)), "1", "L", false)

	// ── Seller ───────────────────────────────────────────────────────────
	pdf.Ln(4)
	dc.sectionBar("SELLER")
	dc.partyBlock(d.SellerName, d.SellerAddress, d.SellerSignedAt, d.SellerSignaturePath, d.SellerIDOnFile)

	// ── Buyer ────────────────────────────────────────────────────────────
	pdf.Ln(4)
	dc.sectionBar("BUYER")
	dc.partyBlock(d.BuyerName, d.BuyerAddress, d.BuyerSignedAt, d.BuyerSignaturePath, d.BuyerIDOnFile)

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

// sectionBar draws a solid, dark section header bar with light text — a
// generic form-section separator (no seal, no agency mark).
func (dc *doc) sectionBar(title string) {
	pdf := dc.pdf
	pdf.Ln(2)
	pdf.SetFont("Helvetica", "B", 10)
	pdf.SetFillColor(45, 45, 45)
	pdf.SetTextColor(255, 255, 255)
	pdf.CellFormat(contentWidth, 7, dc.tr("  "+title), "", 1, "L", true, 0, "")
	pdf.SetTextColor(20, 20, 20)
	pdf.SetFont("Helvetica", "", 10)
}

// vehicleDescription renders the bordered vehicle block: a Year | Make | Model
// mini-table followed by full-width VIN, title-condition, and (optional)
// mileage rows. Every row is self-bordered, so the block reads as one framed
// section regardless of how the free text wraps.
func (dc *doc) vehicleDescription(d Data) {
	pdf := dc.pdf
	pdf.SetDrawColor(180, 180, 180)

	colW := contentWidth / 3.0

	// Header row.
	pdf.SetFont("Helvetica", "B", 9)
	pdf.SetFillColor(240, 240, 240)
	pdf.SetTextColor(60, 60, 60)
	pdf.CellFormat(colW, 6, dc.tr("  Year"), "1", 0, "L", true, 0, "")
	pdf.CellFormat(colW, 6, dc.tr("  Make"), "1", 0, "L", true, 0, "")
	pdf.CellFormat(colW, 6, dc.tr("  Model"), "1", 1, "L", true, 0, "")

	// Value row.
	pdf.SetFont("Helvetica", "", 10)
	pdf.SetTextColor(20, 20, 20)
	year := "—"
	if d.VehicleYear > 0 {
		year = fmt.Sprintf("%d", d.VehicleYear)
	}
	pdf.CellFormat(colW, 7, dc.tr("  "+year), "1", 0, "L", false, 0, "")
	pdf.CellFormat(colW, 7, dc.tr("  "+valueOrDash(d.VehicleMake)), "1", 0, "L", false, 0, "")
	pdf.CellFormat(colW, 7, dc.tr("  "+valueOrDash(d.VehicleModel)), "1", 1, "L", false, 0, "")

	// Full-width detail rows.
	dc.detailRow("Vehicle Identification Number (VIN):", valueOrDash(d.VIN))
	dc.detailRow("Title condition:", valueOrDash(d.TitleConditionLabel))
	if strings.TrimSpace(d.Mileage) != "" {
		dc.detailRow("Mileage:", d.Mileage)
	}
}

// detailRow draws one bordered full-width row with a bold label column and a
// wrapping value column. The row height is sized to whichever column needs the
// most lines, so long "Other: …" title text or a verbose VIN never overflows
// its frame. A manual page-break check keeps the drawn border on one page.
func (dc *doc) detailRow(label, value string) {
	pdf := dc.pdf
	const lineH = 5.5
	const pad = 2.0
	const labelColW = 66.0
	valueColW := contentWidth - labelColW

	tl := dc.tr(label)
	tv := dc.tr(value)

	pdf.SetFont("Helvetica", "B", 9)
	labelLines := pdf.SplitLines([]byte(tl), labelColW-2*pad)
	pdf.SetFont("Helvetica", "", 10)
	valueLines := pdf.SplitLines([]byte(tv), valueColW-2*pad)

	n := len(labelLines)
	if len(valueLines) > n {
		n = len(valueLines)
	}
	if n < 1 {
		n = 1
	}
	rowH := float64(n)*lineH + 2*pad

	x := marginLeft
	y := pdf.GetY()
	// Rect/Line ignore auto page-break, so break manually when the framed row
	// would spill past the bottom margin.
	_, pageH := pdf.GetPageSize()
	_, _, _, bMargin := pdf.GetMargins()
	if y+rowH > pageH-bMargin {
		pdf.AddPage()
		y = pdf.GetY()
	}

	pdf.SetDrawColor(180, 180, 180)
	pdf.Rect(x, y, contentWidth, rowH, "D")
	pdf.Line(x+labelColW, y, x+labelColW, y+rowH)

	pdf.SetFont("Helvetica", "B", 9)
	pdf.SetTextColor(60, 60, 60)
	pdf.SetXY(x+pad, y+pad)
	pdf.MultiCell(labelColW-2*pad, lineH, tl, "", "L", false)

	pdf.SetFont("Helvetica", "", 10)
	pdf.SetTextColor(20, 20, 20)
	pdf.SetXY(x+labelColW+pad, y+pad)
	pdf.MultiCell(valueColW-2*pad, lineH, tv, "", "L", false)

	pdf.SetXY(x, y+rowH)
}

// partyBlock renders one party's identity (name, address), optional
// "Government ID: on file" acknowledgement, and a ruled signature line with
// the embedded signature image placed above the rule (aspect preserved).
func (dc *doc) partyBlock(name, address, signedAt, sigPath string, idOnFile bool) {
	pdf := dc.pdf

	// Name.
	pdf.SetFont("Helvetica", "B", 10)
	pdf.CellFormat(labelWidth, 6, dc.tr("Name"), "", 0, "L", false, 0, "")
	pdf.SetFont("Helvetica", "", 10)
	pdf.CellFormat(contentWidth-labelWidth, 6, dc.tr(valueOrDash(name)), "", 1, "L", false, 0, "")

	// Address (wraps).
	pdf.SetFont("Helvetica", "B", 10)
	pdf.CellFormat(labelWidth, 6, dc.tr("Address"), "", 0, "L", false, 0, "")
	pdf.SetFont("Helvetica", "", 10)
	pdf.MultiCell(contentWidth-labelWidth, 6, dc.tr(valueOrDash(address)), "", "L", false)

	// Government ID acknowledgement — text only, never the image.
	if idOnFile {
		pdf.SetFont("Helvetica", "", 9)
		pdf.SetTextColor(80, 80, 80)
		pdf.CellFormat(contentWidth, 5, dc.tr("Government ID: on file"), "", 1, "L", false, 0, "")
		pdf.SetTextColor(20, 20, 20)
	}

	// Signature: the image sits above the rule; flow=true advances the cursor
	// below the image by its own (aspect-preserved) height, so the rule lands
	// under any signature regardless of its proportions.
	pdf.Ln(6)
	if strings.TrimSpace(sigPath) != "" {
		x := pdf.GetX()
		y := pdf.GetY()
		pdf.ImageOptions(sigPath, x, y, signatureWidth, 0, true,
			fpdf.ImageOptions{ImageType: "PNG", ReadDpi: false}, 0, "")
		pdf.Ln(1)
	} else {
		// No embedded signature yet — leave blank space to sign by hand.
		pdf.Ln(12)
	}

	ruleY := pdf.GetY()
	pdf.SetDrawColor(120, 120, 120)
	pdf.Line(marginLeft, ruleY, marginLeft+signatureWidth, ruleY)
	pdf.Ln(1)

	pdf.SetFont("Helvetica", "", 9)
	pdf.SetTextColor(80, 80, 80)
	pdf.CellFormat(signatureWidth, 5, dc.tr("Signature"), "", 0, "L", false, 0, "")
	pdf.CellFormat(contentWidth-signatureWidth, 5, dc.tr("Date: "+valueOrDash(signedAt)), "", 1, "L", false, 0, "")
	pdf.SetTextColor(20, 20, 20)
}

// valueOrDash returns s, or an em dash when s is blank — used everywhere a
// field may not yet be declared.
func valueOrDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

// nameOrBlank returns a name, or a fill-in underscore run when empty, so the
// conveyance sentence reads like a form with a blank to complete.
func nameOrBlank(s string) string {
	if strings.TrimSpace(s) == "" {
		return "____________"
	}
	return s
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
