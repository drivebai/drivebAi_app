package billofsale

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writePNG writes a small, valid PNG (wide-and-short, like a real signature)
// to path and returns it. Fails the test on any I/O error.
func writePNG(t *testing.T, path string) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 160, 48))
	for x := 0; x < 160; x++ {
		img.Set(x, 24, color.RGBA{0, 0, 0, 255})
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create png: %v", err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return path
}

func sampleData() Data {
	return Data{
		ReferenceID:    "1f2e3d4c-5b6a-7980-a1b2-c3d4e5f60718",
		GeneratedDate:  "2026-07-10",
		VehicleYear:    2019,
		VehicleMake:    "Toyota",
		VehicleModel:   "Corolla",
		VIN:            "1HGCM82633A004352",
		Mileage:        "42000",
		SalePriceCents: 50000,
		Currency:       "USD",
		Terms:          "Vehicle is sold as-is, where-is, with no warranties unless otherwise stated in writing.",
		SellerName:     "Alice Seller",
		SellerAddress:  "100 Market Street, Springfield",
		BuyerName:      "Bob Buyer",
		BuyerAddress:   "200 Elm Avenue, Shelbyville",
		SellerSignedAt: "2026-07-10 12:00 UTC",
		BuyerSignedAt:  "2026-07-10 12:05 UTC",
	}
}

// TestRender_ValidPDFWithSignatures: both signature PNGs embed, output is a
// well-formed PDF, and embedding signatures makes it larger than the
// no-signature baseline.
func TestRender_ValidPDFWithSignatures(t *testing.T) {
	dir := t.TempDir()
	d := sampleData()
	d.SellerSignaturePath = writePNG(t, filepath.Join(dir, "seller.png"))
	d.BuyerSignaturePath = writePNG(t, filepath.Join(dir, "buyer.png"))

	out, err := Render(d)
	if err != nil {
		t.Fatalf("Render error: %v", err)
	}
	if !bytes.HasPrefix(out, []byte("%PDF-")) {
		t.Fatalf("output is not a PDF (prefix %q)", out[:min(5, len(out))])
	}

	// Baseline with no signatures should be strictly smaller — proving the
	// PNGs were actually embedded.
	noSig := sampleData()
	base, err := Render(noSig)
	if err != nil {
		t.Fatalf("Render baseline error: %v", err)
	}
	if len(out) <= len(base) {
		t.Errorf("expected signed PDF (%d bytes) larger than unsigned (%d bytes)", len(out), len(base))
	}
}

// TestRender_ContentFields: the uncompressed PDF text carries the vehicle,
// seller, buyer, terms fields and the purchase-id reference.
func TestRender_ContentFields(t *testing.T) {
	dir := t.TempDir()
	d := sampleData()
	d.SellerSignaturePath = writePNG(t, filepath.Join(dir, "seller.png"))
	d.BuyerSignaturePath = writePNG(t, filepath.Join(dir, "buyer.png"))

	out, err := render(d, false) // uncompressed so literal text is greppable
	if err != nil {
		t.Fatalf("render error: %v", err)
	}
	body := string(out)
	for _, want := range []string{
		"Vehicle Bill of Sale", // header
		d.ReferenceID,          // transaction reference = purchase id
		"Toyota",               // vehicle make
		"Corolla",              // vehicle model
		d.VIN,                  // VIN
		"Alice Seller",         // seller
		"Bob Buyer",            // buyer
		"as-is",                // terms substring
		"500.00",               // sale price
	} {
		if !strings.Contains(body, want) {
			t.Errorf("PDF content missing %q", want)
		}
	}
	// Neutral jurisdiction: no MV-912 / state-form references.
	for _, banned := range []string{"MV-912", "MV912", "MV 912"} {
		if strings.Contains(body, banned) {
			t.Errorf("PDF must not reference %q", banned)
		}
	}
}

// TestRender_LongTermsDoNotCrash: a very long terms/address block wraps
// (MultiCell) and produces a valid PDF instead of overflowing or panicking.
func TestRender_LongTermsDoNotCrash(t *testing.T) {
	dir := t.TempDir()
	d := sampleData()
	d.SellerSignaturePath = writePNG(t, filepath.Join(dir, "seller.png"))
	d.BuyerSignaturePath = writePNG(t, filepath.Join(dir, "buyer.png"))
	d.Terms = strings.Repeat("This vehicle is sold strictly as-is with no warranty of any kind. ", 200)
	d.SellerAddress = strings.Repeat("Really Long Street Name ", 60)

	out, err := Render(d)
	if err != nil {
		t.Fatalf("Render with long terms errored: %v", err)
	}
	if !bytes.HasPrefix(out, []byte("%PDF-")) {
		t.Fatalf("long-terms output is not a PDF")
	}
}

// TestRender_MissingSignatureFile: a signature path that does not resolve to a
// readable file surfaces as a controlled error (no panic, no partial PDF).
func TestRender_MissingSignatureFile(t *testing.T) {
	dir := t.TempDir()
	d := sampleData()
	d.SellerSignaturePath = writePNG(t, filepath.Join(dir, "seller.png"))
	d.BuyerSignaturePath = filepath.Join(dir, "does-not-exist.png") // missing

	out, err := Render(d)
	if err == nil {
		t.Fatalf("expected error for missing signature file, got nil (out=%d bytes)", len(out))
	}
}

// TestRender_NoSignaturesStillRenders: with both signature paths empty (not
// yet signed) the renderer skips embedding but still produces a valid PDF —
// generateAndStoreBillOfSale guards the both-signed precondition separately.
func TestRender_NoSignaturesStillRenders(t *testing.T) {
	out, err := Render(sampleData())
	if err != nil {
		t.Fatalf("Render error: %v", err)
	}
	if !bytes.HasPrefix(out, []byte("%PDF-")) {
		t.Fatalf("output is not a PDF")
	}
}

func TestFormatMoney(t *testing.T) {
	cases := map[int64]string{
		50000:   "$500.00 USD",
		100000:  "$1,000.00 USD",
		1:       "$0.01 USD",
		0:       "$0.00 USD",
		1234567: "$12,345.67 USD",
	}
	for cents, want := range cases {
		if got := formatMoney(cents, "USD"); got != want {
			t.Errorf("formatMoney(%d) = %q, want %q", cents, got, want)
		}
	}
	if got := formatMoney(50000, ""); got != "$500.00 USD" {
		t.Errorf("empty currency should default to USD, got %q", got)
	}
}
