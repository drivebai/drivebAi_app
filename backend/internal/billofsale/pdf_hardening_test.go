package billofsale

import (
	"encoding/binary"
	"hash/crc32"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeMalformedPNG produces a file with a correct PNG signature and a CRC-valid
// IHDR chunk that declares absurd dimensions. The bytes are structurally
// "PNG enough" that a naive reader will trust the header and then run off the
// end of the pixel data.
//
// This is the exact shape that made the PDF library panic with "short buffer"
// instead of returning an error. Because generation runs on a detached
// goroutine, that panic killed the whole API process — and since the finalize
// was re-kicked on every Bill-of-Sale fetch, a single poisoned row turned into
// a restart loop that took the service down for every user.
func writeMalformedPNG(t *testing.T, path string) string {
	t.Helper()
	var b []byte
	b = append(b, 0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a)

	length := make([]byte, 4)
	binary.BigEndian.PutUint32(length, 13)
	b = append(b, length...)

	body := []byte{'I', 'H', 'D', 'R'}
	dim := make([]byte, 4)
	binary.BigEndian.PutUint32(dim, 0x7FFFFFFF)
	body = append(body, dim...) // width
	body = append(body, dim...) // height
	body = append(body, 8, 6, 0, 0, 0)
	b = append(b, body...)

	crc := make([]byte, 4)
	binary.BigEndian.PutUint32(crc, crc32.ChecksumIEEE(body))
	b = append(b, crc...)

	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatalf("write malformed png: %v", err)
	}
	return path
}

// TestRender_MalformedPNGReturnsErrorNotPanic is the regression guard for the
// crash-loop. Render must fail cleanly so the caller leaves finalized_pdf_url
// NULL, keeps both signatures valid, and stays alive.
func TestRender_MalformedPNGReturnsErrorNotPanic(t *testing.T) {
	dir := t.TempDir()
	d := sampleData()
	d.SellerSignaturePath = writeMalformedPNG(t, filepath.Join(dir, "seller.png"))
	d.BuyerSignaturePath = writePNG(t, filepath.Join(dir, "buyer.png"))

	out, err := Render(d)
	if err == nil {
		t.Fatal("expected an error for a malformed signature PNG, got nil")
	}
	if out != nil {
		t.Fatalf("expected no PDF bytes on failure, got %d", len(out))
	}
	if !strings.Contains(err.Error(), "seller.png") {
		t.Errorf("error should name the offending file, got: %v", err)
	}
}

// TestRender_NonPNGSignatureRejected: a JPEG (or anything else) masquerading as
// a signature is refused rather than handed to the PDF library.
func TestRender_NonPNGSignatureRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "seller.png")
	// Minimal JFIF header — decodable enough for DecodeConfig to name the format.
	jpeg := []byte{
		0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00,
		0x01, 0x01, 0x00, 0x00, 0x01, 0x00, 0x01, 0x00, 0x00,
		0xFF, 0xC0, 0x00, 0x11, 0x08, 0x00, 0x10, 0x00, 0x10,
		0x03, 0x01, 0x11, 0x00, 0x02, 0x11, 0x01, 0x03, 0x11, 0x01,
	}
	if err := os.WriteFile(path, jpeg, 0o644); err != nil {
		t.Fatalf("write jpeg: %v", err)
	}
	d := sampleData()
	d.SellerSignaturePath = path
	d.BuyerSignaturePath = writePNG(t, filepath.Join(dir, "buyer.png"))

	if _, err := Render(d); err == nil {
		t.Fatal("expected a non-PNG signature to be rejected")
	}
}

// TestRender_GarbageSignatureRejected: random bytes with no image header.
func TestRender_GarbageSignatureRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "seller.png")
	if err := os.WriteFile(path, []byte("this is definitely not an image"), 0o644); err != nil {
		t.Fatalf("write garbage: %v", err)
	}
	d := sampleData()
	d.SellerSignaturePath = path
	d.BuyerSignaturePath = writePNG(t, filepath.Join(dir, "buyer.png"))

	if _, err := Render(d); err == nil {
		t.Fatal("expected garbage signature bytes to be rejected")
	}
}

// TestRender_EmptySignaturePathsStillValid: an unsigned-yet document renders.
// Guards against the validation loop treating "" as a missing file.
func TestRender_EmptySignaturePathsStillValid(t *testing.T) {
	d := sampleData()
	d.SellerSignaturePath = ""
	d.BuyerSignaturePath = ""

	out, err := Render(d)
	if err != nil {
		t.Fatalf("empty signature paths should render, got: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("expected PDF bytes")
	}
}
