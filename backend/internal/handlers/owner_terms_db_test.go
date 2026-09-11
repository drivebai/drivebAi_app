package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/drivebai/backend/internal/httputil"
	"github.com/drivebai/backend/internal/models"
	"github.com/drivebai/backend/internal/repository"
)

func ownerTermsReq(t *testing.T, userID uuid.UUID, method, path, body string, idParam *uuid.UUID) *http.Request {
	t.Helper()
	rctx := chi.NewRouteContext()
	if idParam != nil {
		rctx.URLParams.Add("id", idParam.String())
	}
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ctx := context.WithValue(req.Context(), httputil.UserIDKey, userID)
	ctx = context.WithValue(ctx, chi.RouteCtxKey, rctx)
	return req.WithContext(ctx)
}

// An owner cannot accept a ROLLING request without the owner package on
// record; a fixed-term request is never gated; app and admin recording both
// lift the gate; the recorded text is the package verbatim.
func TestOwnerTermsGateRollingAcceptOnly(t *testing.T) {
	e := newPayoutEnv(t)
	ctx := context.Background()
	termsRepo := repository.NewOwnerTermsRepository(e.db)
	e.leaseH.SetOwnerTermsRepository(termsRepo)
	billingRepo := repository.NewBillingRepository(e.db)
	e.leaseH.SetBillingDependencies(billingRepo, payoutTestFeeBPS, true)
	run := uuid.NewString()[:8]
	owner := e.seedUser(t, "car_owner", "ot_owner_"+run+"@example.com")
	driver := e.seedUser(t, "driver", "ot_driver_"+run+"@example.com")
	admin := e.seedUser(t, "admin", "ot_admin_"+run+"@example.com")
	e.seedLicense(t, driver)
	t.Cleanup(func() { e.db.Pool.Exec(ctx, `DELETE FROM owner_terms_acceptances WHERE owner_id = $1`, owner) })

	request := func(rolling bool) uuid.UUID {
		car := e.seedCar(t, owner, "available", true, false)
		body := `{"weeks":1}`
		if rolling {
			body = `{"weeks":1,"billing_mode":"rolling"}`
		}
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("listingId", car.String())
		req := httptest.NewRequest(http.MethodPost, "/api/v1/listings/"+car.String()+"/lease-requests", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		c := context.WithValue(req.Context(), httputil.UserIDKey, driver)
		c = context.WithValue(c, chi.RouteCtxKey, rctx)
		rr := httptest.NewRecorder()
		e.leaseH.CreateLeaseRequest(rr, req.WithContext(c))
		if rr.Code != http.StatusCreated {
			t.Fatalf("create (rolling=%v): %d %s", rolling, rr.Code, rr.Body.String())
		}
		var created struct {
			LeaseRequest struct {
				ID uuid.UUID `json:"id"`
			} `json:"lease_request"`
		}
		_ = json.Unmarshal(rr.Body.Bytes(), &created)
		return created.LeaseRequest.ID
	}
	accept := func(leaseID uuid.UUID) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		e.leaseH.AcceptLeaseRequest(rr, ownerTermsReq(t, owner, http.MethodPost, "/api/v1/lease-requests/"+leaseID.String()+"/accept", `{}`, &leaseID))
		return rr
	}

	// Fixed-term: never gated.
	if rr := accept(request(false)); rr.Code != http.StatusOK {
		t.Fatalf("fixed-term accept with no owner terms on file: %d %s, want 200", rr.Code, rr.Body.String())
	}
	// Rolling: refused with the text in hand.
	rolling := request(true)
	rr := accept(rolling)
	if rr.Code != http.StatusConflict || errCodeOf(t, rr) != "OWNER_TERMS_REQUIRED" {
		t.Fatalf("rolling accept with no owner terms: %d %s, want 409 OWNER_TERMS_REQUIRED", rr.Code, rr.Body.String())
	}
	if d := errDetailsOf(t, rr); d["terms_version"] != models.TermsVersionOwnerRollingV2 || d["terms_text"] != models.RollingOwnerTermsV2 {
		t.Errorf("refusal does not carry the current package: %v", d)
	}
	// GET shows not accepted.
	rr = httptest.NewRecorder()
	e.leaseH.GetOwnerTerms(rr, ownerTermsReq(t, owner, http.MethodGet, "/api/v1/me/owner-terms", "", nil))
	var got ownerTermsResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &got)
	if rr.Code != http.StatusOK || got.AcceptedAt != nil || got.TermsText != models.RollingOwnerTermsV2 {
		t.Fatalf("GET owner terms before acceptance: %d %+v", rr.Code, got)
	}
	// A stale version cannot be accepted.
	rr = httptest.NewRecorder()
	e.leaseH.AcceptOwnerTerms(rr, ownerTermsReq(t, owner, http.MethodPost, "/api/v1/me/owner-terms/accept", `{"terms_version":"owner-rolling-v1 (2026-09-07)"}`, nil))
	if rr.Code != http.StatusConflict || errCodeOf(t, rr) != "OWNER_TERMS_VERSION_MISMATCH" {
		t.Fatalf("stale version accepted: %d %s", rr.Code, rr.Body.String())
	}
	// The current version can.
	rr = httptest.NewRecorder()
	e.leaseH.AcceptOwnerTerms(rr, ownerTermsReq(t, owner, http.MethodPost, "/api/v1/me/owner-terms/accept", `{"terms_version":"`+models.TermsVersionOwnerRollingV2+`"}`, nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("accept owner terms: %d %s", rr.Code, rr.Body.String())
	}
	var stored struct {
		Text, Channel string
	}
	_ = e.db.Pool.QueryRow(ctx, `SELECT terms_text, channel FROM owner_terms_acceptances WHERE owner_id=$1 AND terms_version=$2`, owner, models.TermsVersionOwnerRollingV2).Scan(&stored.Text, &stored.Channel)
	if stored.Text != models.RollingOwnerTermsV2 || stored.Channel != "app" {
		t.Errorf("recorded acceptance is not the package verbatim from the app: channel=%q", stored.Channel)
	}
	// Now the rolling accept goes through.
	if rr := accept(rolling); rr.Code != http.StatusOK {
		t.Fatalf("rolling accept after owner terms: %d %s, want 200", rr.Code, rr.Body.String())
	}

	// Admin recording on another owner's behalf: note required, then lifts.
	owner2 := e.seedUser(t, "car_owner", "ot_owner2_"+run+"@example.com")
	t.Cleanup(func() { e.db.Pool.Exec(ctx, `DELETE FROM owner_terms_acceptances WHERE owner_id = $1`, owner2) })
	rr = httptest.NewRecorder()
	e.leaseH.AdminRecordOwnerTerms(rr, ownerTermsReq(t, admin, http.MethodPost, "/api/v1/admin/users/"+owner2.String()+"/owner-terms", `{"note":"short"}`, &owner2))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("admin record without a real note: %d, want 400", rr.Code)
	}
	rr = httptest.NewRecorder()
	e.leaseH.AdminRecordOwnerTerms(rr, ownerTermsReq(t, admin, http.MethodPost, "/api/v1/admin/users/"+owner2.String()+"/owner-terms", `{"note":"Read to the owner on a call on 2026-09-11; owner agreed verbally."}`, &owner2))
	if rr.Code != http.StatusOK {
		t.Fatalf("admin record: %d %s", rr.Code, rr.Body.String())
	}
	var ch string
	var by *uuid.UUID
	_ = e.db.Pool.QueryRow(ctx, `SELECT channel, recorded_by FROM owner_terms_acceptances WHERE owner_id=$1`, owner2).Scan(&ch, &by)
	if ch != "admin" || by == nil || *by != admin {
		t.Errorf("admin record provenance wrong: channel=%q recorded_by=%v", ch, by)
	}
	ok, _, _ := termsRepo.Accepted(ctx, owner2, models.TermsVersionOwnerRollingV2)
	if !ok {
		t.Error("admin record did not lift the gate")
	}
	// Idempotent: a second record changes nothing.
	if created, _ := termsRepo.Record(ctx, owner2, models.TermsVersionOwnerRollingV2, "different text", "app", nil, nil); created {
		t.Error("a second acceptance overwrote the first")
	}
}
