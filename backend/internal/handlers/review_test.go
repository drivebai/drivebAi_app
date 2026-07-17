package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/drivebai/backend/internal/httputil"
	"github.com/drivebai/backend/internal/models"
	"github.com/drivebai/backend/internal/repository"
	"github.com/google/uuid"
)

// BEHAVIORAL tests for the rating submission rules, run through the real
// handler over httptest with a fake store: party checks, completed-only,
// consumer-only car rating, star bounds, and the double-submit 409.

type fakeReviewStore struct {
	tx        *repository.ReviewTransaction
	created   []*models.Review
	createErr error
}

func (f *fakeReviewStore) Create(_ context.Context, rev *models.Review) error {
	if f.createErr != nil {
		return f.createErr
	}
	f.created = append(f.created, rev)
	return nil
}

func (f *fakeReviewStore) ResolveCompletedTransaction(_ context.Context, _ models.ReviewTransactionType, _ uuid.UUID) (*repository.ReviewTransaction, error) {
	return f.tx, nil
}

func postReview(t *testing.T, h *ReviewHandler, callerID uuid.UUID, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/reviews", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), httputil.UserIDKey, callerID))
	rr := httptest.NewRecorder()
	h.SubmitReview(rr, req)
	return rr
}

func reviewBody(txID uuid.UUID, extra string) string {
	return fmt.Sprintf(`{"transaction_type":"purchase","transaction_id":"%s"%s}`, txID, extra)
}

func TestSubmitReview_BuyerRatesCarAndSeller(t *testing.T) {
	buyer, seller, car, txID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	store := &fakeReviewStore{tx: &repository.ReviewTransaction{CarID: car, OwnerSideID: seller, ConsumerSideID: buyer}}
	h := &ReviewHandler{reviews: store, logger: discardLogger()}

	rr := postReview(t, h, buyer, reviewBody(txID, `,"car_rating":4,"partner_rating":5,"comment":"smooth handover"`))

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", rr.Code, rr.Body.String())
	}
	if len(store.created) != 2 {
		t.Fatalf("created %d reviews, want 2", len(store.created))
	}
	carRev, userRev := store.created[0], store.created[1]
	if carRev.SubjectType != models.ReviewSubjectCar || carRev.SubjectCarID == nil || *carRev.SubjectCarID != car || carRev.Rating != 4 {
		t.Fatalf("car review wrong: %+v", carRev)
	}
	if userRev.SubjectType != models.ReviewSubjectUser || userRev.SubjectUserID == nil || *userRev.SubjectUserID != seller || userRev.Rating != 5 {
		t.Fatalf("user review wrong: %+v", userRev)
	}
	if userRev.AuthorID != buyer {
		t.Fatalf("author = %s, want buyer", userRev.AuthorID)
	}
}

func TestSubmitReview_SellerRatesBuyerOnly(t *testing.T) {
	buyer, seller, car, txID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	store := &fakeReviewStore{tx: &repository.ReviewTransaction{CarID: car, OwnerSideID: seller, ConsumerSideID: buyer}}
	h := &ReviewHandler{reviews: store, logger: discardLogger()}

	rr := postReview(t, h, seller, reviewBody(txID, `,"partner_rating":3`))
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", rr.Code, rr.Body.String())
	}
	if len(store.created) != 1 || *store.created[0].SubjectUserID != buyer {
		t.Fatalf("seller's review must target the buyer: %+v", store.created)
	}
}

func TestSubmitReview_SellerCannotRateOwnCar(t *testing.T) {
	buyer, seller, car, txID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	store := &fakeReviewStore{tx: &repository.ReviewTransaction{CarID: car, OwnerSideID: seller, ConsumerSideID: buyer}}
	h := &ReviewHandler{reviews: store, logger: discardLogger()}

	rr := postReview(t, h, seller, reviewBody(txID, `,"car_rating":5`))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (owner side rating own car)", rr.Code)
	}
	if len(store.created) != 0 {
		t.Fatal("no review may be created")
	}
}

func TestSubmitReview_NonPartyForbidden(t *testing.T) {
	store := &fakeReviewStore{tx: &repository.ReviewTransaction{CarID: uuid.New(), OwnerSideID: uuid.New(), ConsumerSideID: uuid.New()}}
	h := &ReviewHandler{reviews: store, logger: discardLogger()}

	rr := postReview(t, h, uuid.New(), reviewBody(uuid.New(), `,"partner_rating":5`))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
}

func TestSubmitReview_UnknownOrIncompleteTransaction404(t *testing.T) {
	h := &ReviewHandler{reviews: &fakeReviewStore{tx: nil}, logger: discardLogger()}

	rr := postReview(t, h, uuid.New(), reviewBody(uuid.New(), `,"partner_rating":5`))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (only COMPLETED transactions are rateable)", rr.Code)
	}
}

func TestSubmitReview_ValidationErrors(t *testing.T) {
	buyer := uuid.New()
	store := &fakeReviewStore{tx: &repository.ReviewTransaction{CarID: uuid.New(), OwnerSideID: uuid.New(), ConsumerSideID: buyer}}
	h := &ReviewHandler{reviews: store, logger: discardLogger()}

	cases := []struct {
		name string
		body string
	}{
		{"no ratings at all", reviewBody(uuid.New(), ``)},
		{"stars below 1", reviewBody(uuid.New(), `,"car_rating":0`)},
		{"stars above 5", reviewBody(uuid.New(), `,"partner_rating":6`)},
		{"bad transaction type", `{"transaction_type":"trade","transaction_id":"` + uuid.NewString() + `","partner_rating":5}`},
		{"bad transaction id", `{"transaction_type":"purchase","transaction_id":"nope","partner_rating":5}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := postReview(t, h, buyer, tc.body)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body: %s", rr.Code, rr.Body.String())
			}
		})
	}
	if len(store.created) != 0 {
		t.Fatal("validation failures must not create reviews")
	}
}

func TestSubmitReview_DoubleSubmit409(t *testing.T) {
	buyer := uuid.New()
	store := &fakeReviewStore{
		tx:        &repository.ReviewTransaction{CarID: uuid.New(), OwnerSideID: uuid.New(), ConsumerSideID: buyer},
		createErr: models.ErrAlreadyReviewed,
	}
	h := &ReviewHandler{reviews: store, logger: discardLogger()}

	rr := postReview(t, h, buyer, reviewBody(uuid.New(), `,"partner_rating":5`))
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body: %s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil || resp.Error.Code != "REVIEW_ALREADY_EXISTS" {
		t.Fatalf("error code = %q, want REVIEW_ALREADY_EXISTS (%v)", resp.Error.Code, err)
	}
}
