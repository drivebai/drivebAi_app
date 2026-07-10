package handlers

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/drivebai/backend/internal/models"
	"github.com/drivebai/backend/internal/urlsigner"
)

// TestBuildBOSResponse_SignsPrivateURLs proves buildBOSResponse signs the
// finalized PDF and both signature URLs exactly like car documents — the
// stored value stays BARE, the emitted value carries ?sig=&exp=. This is the
// privacy invariant: /uploads/purchases/** is private, so the response must
// mint short-TTL signatures for each.
func TestBuildBOSResponse_SignsPrivateURLs(t *testing.T) {
	signer := urlsigner.New("test-secret")
	h := &PurchaseRequestHandler{
		urlSigner: &PrivateURLSigner{Signer: signer, TTL: time.Minute},
	}
	id := uuid.New()
	sellerURL := "/uploads/purchases/" + id.String() + "/seller_signature.png"
	buyerURL := "/uploads/purchases/" + id.String() + "/buyer_signature.png"
	pdfURL := billOfSalePDFRelPath(id)
	now := time.Now().UTC()
	b := &models.PurchaseBillOfSale{
		ID:                 uuid.New(),
		PurchaseRequestID:  id,
		Currency:           "USD",
		SellerSignatureURL: &sellerURL,
		SellerSignedAt:     &now,
		BuyerSignatureURL:  &buyerURL,
		BuyerSignedAt:      &now,
		FinalizedPDFURL:    &pdfURL,
		FinalizedAt:        &now,
	}

	resp := h.buildBOSResponse(b)

	for name, got := range map[string]*string{
		"finalized_pdf_url":    resp.FinalizedPDFURL,
		"seller_signature_url": resp.SellerSignatureURL,
		"buyer_signature_url":  resp.BuyerSignatureURL,
	} {
		if got == nil {
			t.Fatalf("%s should be present in response", name)
			continue
		}
		if !strings.Contains(*got, "sig=") || !strings.Contains(*got, "exp=") {
			t.Errorf("%s should be signed with ?sig=&exp=, got %q", name, *got)
		}
	}

	// The stored (DB) values remain the bare relative paths — never signed.
	if *b.FinalizedPDFURL != pdfURL {
		t.Errorf("stored finalized_pdf_url must remain bare, got %q", *b.FinalizedPDFURL)
	}
	if strings.Contains(*b.FinalizedPDFURL, "sig=") {
		t.Errorf("stored value must not carry a signature")
	}
}

// TestBillOfSalePurchasePathIsPrivate locks the privacy posture: the
// finalized PDF and signature paths under /uploads/purchases/** must be
// classified PRIVATE so FilesHandler refuses unsigned fetches in production.
func TestBillOfSalePurchasePathIsPrivate(t *testing.T) {
	id := uuid.New()
	cases := []string{
		"purchases/" + id.String() + "/bill_of_sale_" + id.String() + ".pdf",
		"purchases/" + id.String() + "/seller_signature.png",
		"purchases/" + id.String() + "/buyer_signature.png",
	}
	for _, rel := range cases {
		if !IsPrivateUploadPath(rel) {
			t.Errorf("expected %q to be private", rel)
		}
	}
}
