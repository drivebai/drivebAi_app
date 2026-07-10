package handlers

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/drivebai/backend/internal/httputil"
	"github.com/drivebai/backend/internal/models"
)

// writeSignaturePNG writes a valid signature-shaped PNG at path (creating
// parent dirs) so the renderer can embed it.
func writeSignaturePNG(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
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
}

func signedBOS(id uuid.UUID) *models.PurchaseBillOfSale {
	sellerURL := "/uploads/purchases/" + id.String() + "/seller_signature.png"
	buyerURL := "/uploads/purchases/" + id.String() + "/buyer_signature.png"
	now := time.Now().UTC()
	return &models.PurchaseBillOfSale{
		ID:                 uuid.New(),
		PurchaseRequestID:  id,
		VehicleYear:        2020,
		VehicleMake:        "Honda",
		VehicleModel:       "Civic",
		VIN:                "2HGES16575H000001",
		SaleAmountCents:    50000,
		Currency:           "USD",
		TermsConditions:    models.DefaultBOSTerms,
		SellerName:         "Sam Seller",
		SellerAddress:      "1 A St",
		SellerSignatureURL: &sellerURL,
		SellerSignedAt:     &now,
		BuyerName:          "Betty Buyer",
		BuyerAddress:       "2 B St",
		BuyerSignatureURL:  &buyerURL,
		BuyerSignedAt:      &now,
	}
}

// TestBillOfSalePDFRelPath_Deterministic: the stored URL is deterministic (one
// file per purchase, id encoded twice) — the foundation of retry idempotency.
func TestBillOfSalePDFRelPath_Deterministic(t *testing.T) {
	id := uuid.New()
	a := billOfSalePDFRelPath(id)
	b := billOfSalePDFRelPath(id)
	if a != b {
		t.Fatalf("path not deterministic: %q vs %q", a, b)
	}
	want := "/uploads/purchases/" + id.String() + "/bill_of_sale_" + id.String() + ".pdf"
	if a != want {
		t.Fatalf("got %q, want %q", a, want)
	}
	if billOfSalePDFRelPath(uuid.New()) == a {
		t.Fatalf("distinct purchases must map to distinct paths")
	}
}

func TestUploadRelToDiskPath(t *testing.T) {
	h := &PurchaseRequestHandler{uploadDir: "/data/uploads"}
	got := h.uploadRelToDiskPath("/uploads/purchases/abc/seller_signature.png")
	want := filepath.Join("/data/uploads", "purchases", "abc", "seller_signature.png")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if h.uploadRelToDiskPath("https://example.com/x.png") != "" {
		t.Errorf("non-/uploads path should resolve to empty string")
	}
}

// TestWriteBillOfSalePDF_Success: both signature files present → a valid PDF is
// written to the deterministic path and the bare relative URL returned. A
// second call overwrites the SAME file (no second file) — retry idempotency.
func TestWriteBillOfSalePDF_Success(t *testing.T) {
	dir := t.TempDir()
	h := &PurchaseRequestHandler{uploadDir: dir}
	id := uuid.New()
	b := signedBOS(id)
	writeSignaturePNG(t, h.uploadRelToDiskPath(*b.SellerSignatureURL))
	writeSignaturePNG(t, h.uploadRelToDiskPath(*b.BuyerSignatureURL))

	rel, err := h.writeBillOfSalePDF(b)
	if err != nil {
		t.Fatalf("writeBillOfSalePDF error: %v", err)
	}
	if rel != billOfSalePDFRelPath(id) {
		t.Fatalf("returned url %q != deterministic %q", rel, billOfSalePDFRelPath(id))
	}
	pdfPath := h.billOfSalePDFDiskPath(id)
	data, err := os.ReadFile(pdfPath)
	if err != nil {
		t.Fatalf("expected PDF at %q: %v", pdfPath, err)
	}
	if !bytes.HasPrefix(data, []byte("%PDF-")) {
		t.Fatalf("written file is not a PDF")
	}

	// Second call = retry → overwrites the same path, no additional file.
	if _, err := h.writeBillOfSalePDF(b); err != nil {
		t.Fatalf("second writeBillOfSalePDF error: %v", err)
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "purchases", id.String()))
	pdfCount := 0
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".pdf" {
			pdfCount++
		}
	}
	if pdfCount != 1 {
		t.Fatalf("expected exactly one finalized PDF file, got %d", pdfCount)
	}
}

// TestWriteBillOfSalePDF_MissingSignatureFile: a signature URL that does not
// resolve to a file on disk → controlled error AND no PDF written (failure
// isolation: signatures/status remain valid, finalized_pdf_url stays NULL).
func TestWriteBillOfSalePDF_MissingSignatureFile(t *testing.T) {
	dir := t.TempDir()
	h := &PurchaseRequestHandler{uploadDir: dir}
	id := uuid.New()
	b := signedBOS(id)
	// Only the seller file exists; buyer's is missing.
	writeSignaturePNG(t, h.uploadRelToDiskPath(*b.SellerSignatureURL))

	if _, err := h.writeBillOfSalePDF(b); err == nil {
		t.Fatalf("expected error for missing signature file")
	}
	if _, err := os.Stat(h.billOfSalePDFDiskPath(id)); !os.IsNotExist(err) {
		t.Fatalf("no PDF should have been written on render failure")
	}
}

// TestWriteBillOfSalePDF_RequiresBothSignatures: cannot finalize before both
// parties have signed.
func TestWriteBillOfSalePDF_RequiresBothSignatures(t *testing.T) {
	h := &PurchaseRequestHandler{uploadDir: t.TempDir()}
	b := signedBOS(uuid.New())
	b.SellerSignatureURL = nil
	if _, err := h.writeBillOfSalePDF(b); err == nil {
		t.Fatalf("expected error when a signature is missing")
	}
}

// TestGenerateAndStore_NoOpWhenFinalized: an already-finalized BoS is a no-op —
// no render, no file, no DB write. This is the duplicate-finalize idempotency
// guard (safe even with a nil repo, proving no DB access occurs).
func TestGenerateAndStore_NoOpWhenFinalized(t *testing.T) {
	dir := t.TempDir()
	h := &PurchaseRequestHandler{uploadDir: dir} // repo intentionally nil
	id := uuid.New()
	b := signedBOS(id)
	already := billOfSalePDFRelPath(id)
	b.FinalizedPDFURL = &already

	if err := h.generateAndStoreBillOfSale(context.Background(), b); err != nil {
		t.Fatalf("no-op finalize should return nil, got %v", err)
	}
	if _, err := os.Stat(h.billOfSalePDFDiskPath(id)); !os.IsNotExist(err) {
		t.Fatalf("already-finalized finalize must not write a file")
	}
}

// TestBuildBOSData maps fields and resolves signature disk paths.
func TestBuildBOSData(t *testing.T) {
	h := &PurchaseRequestHandler{uploadDir: "/data/uploads"}
	id := uuid.New()
	b := signedBOS(id)
	d := h.buildBOSData(b)
	if d.ReferenceID != id.String() {
		t.Errorf("reference id mismatch: %q", d.ReferenceID)
	}
	if d.VIN != b.VIN || d.SellerName != b.SellerName || d.BuyerName != b.BuyerName {
		t.Errorf("core fields not mapped")
	}
	if d.SalePriceCents != 50000 {
		t.Errorf("sale price cents not mapped")
	}
	if d.SellerSignaturePath != filepath.Join("/data/uploads", "purchases", id.String(), "seller_signature.png") {
		t.Errorf("seller signature path not resolved: %q", d.SellerSignaturePath)
	}
	if d.SellerSignedAt == "" || d.BuyerSignedAt == "" {
		t.Errorf("signed-at should be formatted, got seller=%q buyer=%q", d.SellerSignedAt, d.BuyerSignedAt)
	}
}

// TestFinalizeBOS_Unauthorized: no auth context → 401 before any DB access.
func TestFinalizeBOS_Unauthorized(t *testing.T) {
	h := &PurchaseRequestHandler{}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/purchase-requests/"+uuid.New().String()+"/bos/finalize", nil)
	rr := httptest.NewRecorder()
	h.FinalizeBOS(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rr.Code)
	}
}

// TestFinalizeBOS_InvalidID: authed but malformed id → 400 before DB access.
func TestFinalizeBOS_InvalidID(t *testing.T) {
	h := &PurchaseRequestHandler{}
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "not-a-uuid")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/purchase-requests/not-a-uuid/bos/finalize", nil)
	ctx := context.WithValue(req.Context(), httputil.UserIDKey, uuid.New())
	ctx = context.WithValue(ctx, chi.RouteCtxKey, rctx)
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.FinalizeBOS(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rr.Code)
	}
}

// TestPurchase_Create_NegativeOffer: negative offers are rejected by the same
// positivity floor as $0.
func TestPurchase_Create_NegativeOffer(t *testing.T) {
	h := &PurchaseRequestHandler{}
	carID := uuid.New()
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("carId", carID.String())
	req := httptest.NewRequest(http.MethodPost, "/api/v1/cars/"+carID.String()+"/purchase-requests", bytes.NewBufferString(`{"offer_amount_cents":-100}`))
	ctx := context.WithValue(req.Context(), httputil.UserIDKey, uuid.New())
	ctx = context.WithValue(ctx, chi.RouteCtxKey, rctx)
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400 for negative offer, got %d", rr.Code)
	}
}
