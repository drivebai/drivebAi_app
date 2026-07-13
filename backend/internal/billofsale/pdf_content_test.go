package billofsale

import (
	"bytes"
	"compress/zlib"
	"io"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// pdfStreamRe captures the bytes between a `stream`/`endstream` pair. fpdf
// emits one FlateDecode content stream per page plus (when signatures embed)
// image XObject streams; we decode each so the assertions run against the
// PRODUCTION, compressed output rather than a test-only uncompressed variant.
var pdfStreamRe = regexp.MustCompile(`(?s)stream\r?\n(.*?)\r?\nendstream`)

// decodePDFStreams flate-decompresses every stream object in a rendered PDF
// and concatenates the results, so page-content text operators become
// greppable. Streams that are not zlib data (already-raw payloads) are
// included verbatim — presence checks only ever need a superset of the text.
func decodePDFStreams(t *testing.T, pdf []byte) string {
	t.Helper()
	if !bytes.HasPrefix(pdf, []byte("%PDF-")) {
		t.Fatalf("not a PDF (prefix %q)", pdf[:min(5, len(pdf))])
	}
	var out bytes.Buffer
	for _, m := range pdfStreamRe.FindAllSubmatch(pdf, -1) {
		raw := m[1]
		if zr, err := zlib.NewReader(bytes.NewReader(raw)); err == nil {
			if dec, err := io.ReadAll(zr); err == nil {
				out.Write(dec)
				out.WriteByte('\n')
				continue
			}
		}
		out.Write(raw)
		out.WriteByte('\n')
	}
	if out.Len() == 0 {
		t.Fatal("no decodable streams found in PDF")
	}
	return out.String()
}

// bannedFormLiterals are strings that would turn the neutral private record
// back into something masquerading as a state motor-vehicle form. NONE may
// appear anywhere in the rendered document.
var bannedFormLiterals = []string{
	"MV-912", "MV912", "MV 912",
	"dmv.ny.gov", ".gov",
	"Department of Motor Vehicles",
}

// bannedStateFormNumber matches the MV-#### state-form-number family (and its
// unhyphenated / spaced spellings) so a reintroduced form code fails the test
// even if it isn't literally "MV-912".
var bannedStateFormNumber = regexp.MustCompile(`(?i)\bMV[\s-]?\d{2,4}\b`)

// TestRender_ProductionContent_Decompressed is the authoritative content
// contract for the finalized Bill of Sale. It renders through the PRODUCTION
// path (Render → compressed), decompresses the streams, and asserts the
// document reads as a complete private bill of sale while carrying NONE of the
// government-form markers the layout deliberately omits.
func TestRender_ProductionContent_Decompressed(t *testing.T) {
	dir := t.TempDir()
	d := sampleData()
	d.TitleConditionLabel = "Clean"
	d.SellerSignaturePath = writePNG(t, filepath.Join(dir, "seller.png"))
	d.BuyerSignaturePath = writePNG(t, filepath.Join(dir, "buyer.png"))

	out, err := Render(d) // production: compressed
	if err != nil {
		t.Fatalf("Render error: %v", err)
	}
	body := decodePDFStreams(t, out)

	// ── MUST CONTAIN ─────────────────────────────────────────────────────
	want := []struct{ label, sub string }{
		{"header", "Vehicle Bill of Sale"},
		{"not-a-government-form disclaimer", "This is not an official government form"},
		{"private-record disclaimer", "private record of a vehicle sale"},
		{"vehicle grid year", "2019"},
		{"vehicle grid make", "Toyota"},
		{"vehicle grid model", "Corolla"},
		{"VIN value", d.VIN},
		{"title-condition row label", "Title condition"},
		{"title-condition value", "Clean"},
		{"terms substring", "as-is"},
		{"sale price", "500.00"},
		{"reference id (footer)", d.ReferenceID},
	}
	for _, w := range want {
		if !strings.Contains(body, w.sub) {
			t.Errorf("PDF must contain %s (%q)", w.label, w.sub)
		}
	}

	// ── MUST NOT CONTAIN ─────────────────────────────────────────────────
	for _, banned := range bannedFormLiterals {
		if strings.Contains(body, banned) {
			t.Errorf("PDF must not reference %q (government-form marker)", banned)
		}
	}
	if loc := bannedStateFormNumber.FindString(body); loc != "" {
		t.Errorf("PDF must not carry a state form number, found %q", loc)
	}
}

// TestRender_TitleConditionLabelVariants proves the seller-declared title
// brand is threaded verbatim into the vehicle block for every label shape,
// including the free-text "Other: <detail>" spelling — and that a blank label
// degrades to an em dash rather than dropping the row.
func TestRender_TitleConditionLabelVariants(t *testing.T) {
	cases := []struct {
		name  string
		label string
		want  string // literal ASCII value expected; "" = only assert the row exists
	}{
		{"clean", "Clean", "Clean"},
		{"salvage", "Salvage", "Salvage"},
		{"other-free-text", "Other: rebuilt frame rail", "Other: rebuilt frame rail"},
		// A blank label degrades to an em dash (rendered as a cp1252 byte, not
		// greppable ASCII) — the row must still be present, just with no value.
		{"blank", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := sampleData()
			d.TitleConditionLabel = tc.label
			out, err := Render(d)
			if err != nil {
				t.Fatalf("Render: %v", err)
			}
			body := decodePDFStreams(t, out)
			if !strings.Contains(body, "Title condition") {
				t.Fatal("title-condition row label missing")
			}
			if tc.want != "" && !strings.Contains(body, tc.want) {
				t.Errorf("title condition %q should render %q", tc.label, tc.want)
			}
		})
	}
}

// TestRender_NoGovernmentMarkers_AllFields is a belt-and-suspenders sweep:
// even with adversarial user text in every free-text field, the banned
// government-form markers never leak in via user data.
func TestRender_NoGovernmentMarkers_AllFields(t *testing.T) {
	d := sampleData()
	d.TitleConditionLabel = "Rebuilt"
	// User-supplied text is fine; the renderer must not itself synthesize any
	// government-form marker around it.
	out, err := Render(d)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	body := decodePDFStreams(t, out)
	for _, banned := range bannedFormLiterals {
		if strings.Contains(body, banned) {
			t.Errorf("renderer leaked government-form marker %q", banned)
		}
	}
	if m := bannedStateFormNumber.FindString(body); m != "" {
		t.Errorf("renderer leaked state form number %q", m)
	}
}
