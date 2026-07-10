package handlers

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"testing"

	"github.com/google/uuid"
)

func validPNGBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.RGBA{0, 0, 0, 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

// malformedPNGBytes: valid magic + CRC-valid IHDR declaring absurd dimensions.
// This payload made the PDF library panic rather than error, and the panic
// escaped a detached goroutine and took the process with it.
func malformedPNGBytes() []byte {
	var b []byte
	b = append(b, 0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a)
	length := make([]byte, 4)
	binary.BigEndian.PutUint32(length, 13)
	b = append(b, length...)
	body := []byte{'I', 'H', 'D', 'R'}
	dim := make([]byte, 4)
	binary.BigEndian.PutUint32(dim, 0x7FFFFFFF)
	body = append(body, dim...)
	body = append(body, dim...)
	body = append(body, 8, 6, 0, 0, 0)
	b = append(b, body...)
	crc := make([]byte, 4)
	binary.BigEndian.PutUint32(crc, crc32.ChecksumIEEE(body))
	return append(b, crc...)
}

func TestValidateSignatureUpload_AcceptsRealPNG(t *testing.T) {
	if err := validateSignatureUpload(validPNGBytes(t, 160, 48)); err != nil {
		t.Fatalf("a real PNG signature should be accepted, got: %v", err)
	}
}

func TestValidateSignatureUpload_RejectsMalformedHeader(t *testing.T) {
	if err := validateSignatureUpload(malformedPNGBytes()); err == nil {
		t.Fatal("a PNG with an absurd IHDR must be rejected at upload")
	}
}

func TestValidateSignatureUpload_RejectsEmpty(t *testing.T) {
	if err := validateSignatureUpload(nil); err == nil {
		t.Fatal("an empty signature must be rejected")
	}
}

func TestValidateSignatureUpload_RejectsGarbage(t *testing.T) {
	if err := validateSignatureUpload([]byte("not an image at all")); err == nil {
		t.Fatal("garbage bytes must be rejected")
	}
}

func TestValidateSignatureUpload_RejectsOversizedPayload(t *testing.T) {
	if err := validateSignatureUpload(make([]byte, maxSignatureUploadBytes+1)); err == nil {
		t.Fatal("an oversized payload must be rejected")
	}
}

// beginFinalize is the per-purchase claim that keeps concurrent generators from
// rewriting the same file underneath a reader.
func TestBeginFinalize_OnlyOneClaimAtATime(t *testing.T) {
	id := uuid.MustParse("3f6d1a7c-2b48-4d5e-9a10-8c7b6e5d4f32")

	release, won := beginFinalize(id)
	if !won {
		t.Fatal("first caller should win the claim")
	}
	if _, secondWon := beginFinalize(id); secondWon {
		t.Fatal("a second concurrent caller must not win the claim")
	}
	release()

	release2, wonAgain := beginFinalize(id)
	if !wonAgain {
		t.Fatal("the claim should be reusable once released")
	}
	release2()
}
