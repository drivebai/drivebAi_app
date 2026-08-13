package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/drivebai/backend/internal/auth"
	"github.com/drivebai/backend/internal/httputil"
	"github.com/drivebai/backend/internal/models"
	"github.com/drivebai/backend/internal/repository"
)

// Behavioral tests for the OTP-confirmed contact change (batch items 7+8):
// nothing commits before the code verifies, taken identifiers 409 at both
// request- and verify-time, wrong codes count attempts and lock out.

type fakeContactUsers struct {
	user        *models.User
	emailTaken  bool
	phoneTaken  bool
	updated     *models.User
	updateError error
}

func (f *fakeContactUsers) GetByID(_ context.Context, _ uuid.UUID) (*models.User, error) {
	u := *f.user
	return &u, nil
}
func (f *fakeContactUsers) Update(_ context.Context, u *models.User) error {
	if f.updateError != nil {
		return f.updateError
	}
	f.updated = u
	return nil
}
func (f *fakeContactUsers) EmailExists(_ context.Context, _ string) (bool, error) {
	return f.emailTaken, nil
}
func (f *fakeContactUsers) PhoneExistsExcludingUser(_ context.Context, _ string, _ uuid.UUID) (bool, error) {
	return f.phoneTaken, nil
}
func (f *fakeContactUsers) GetOTPSendCount(_ context.Context, _ string, _ time.Time) (int, error) {
	return 0, nil
}
func (f *fakeContactUsers) RecordOTPSend(_ context.Context, _, _ string) error { return nil }

type fakeChanges struct {
	latest    *repository.ContactChangeOTP
	created   *repository.ContactChangeOTP
	attempts  int
	consumed  bool
}

func (f *fakeChanges) Create(_ context.Context, o *repository.ContactChangeOTP) error {
	f.created = o
	return nil
}
func (f *fakeChanges) GetLatestUnconsumed(_ context.Context, _ uuid.UUID) (*repository.ContactChangeOTP, error) {
	if f.latest == nil {
		return nil, context.Canceled
	}
	o := *f.latest
	o.Attempts = f.attempts
	return &o, nil
}
func (f *fakeChanges) IncrementAttempts(_ context.Context, _ uuid.UUID) error {
	f.attempts++
	return nil
}
func (f *fakeChanges) MarkConsumed(_ context.Context, _ uuid.UUID) error {
	f.consumed = true
	return nil
}

func newContactHandler(users *fakeContactUsers, changes *fakeChanges) *ContactChangeHandler {
	return &ContactChangeHandler{
		users:     users,
		changes:   changes,
		otpSender: fakeOTPSender{},
		logger:    slog.Default(),
	}
}

func contactCtx(userID uuid.UUID) context.Context {
	ctx := context.WithValue(context.Background(), httputil.UserIDKey, userID)
	return context.WithValue(ctx, chi.RouteCtxKey, chi.NewRouteContext())
}

func doJSON(h http.HandlerFunc, ctx context.Context, body any) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(b)).WithContext(ctx)
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

func TestContactChange_RequestRejectsTakenEmail(t *testing.T) {
	uid := uuid.New()
	users := &fakeContactUsers{user: &models.User{ID: uid, Email: "old@example.com"}, emailTaken: true}
	changes := &fakeChanges{}
	h := newContactHandler(users, changes)

	rec := doJSON(h.Request, contactCtx(uid), map[string]string{"field": "email", "new_value": "new@example.com"})
	if rec.Code != http.StatusConflict {
		t.Fatalf("taken email must 409, got %d: %s", rec.Code, rec.Body.String())
	}
	if changes.created != nil {
		t.Error("no OTP row may be created for a taken email")
	}
}

func TestContactChange_NothingCommitsUntilVerify(t *testing.T) {
	uid := uuid.New()
	users := &fakeContactUsers{user: &models.User{ID: uid, Email: "old@example.com"}}
	changes := &fakeChanges{}
	h := newContactHandler(users, changes)

	rec := doJSON(h.Request, contactCtx(uid), map[string]string{"field": "email", "new_value": "New@Example.com"})
	if rec.Code != http.StatusOK {
		t.Fatalf("request: %d %s", rec.Code, rec.Body.String())
	}
	if users.updated != nil {
		t.Fatal("Request must not write the user")
	}
	if changes.created == nil || changes.created.NewValue != "new@example.com" {
		t.Fatalf("OTP row must store the lowercased new value, got %+v", changes.created)
	}
}

func TestContactChange_VerifyCommitsEmailAndMarksVerified(t *testing.T) {
	uid := uuid.New()
	code, hash, _ := auth.GenerateOTP()
	users := &fakeContactUsers{user: &models.User{ID: uid, Email: "old@example.com", IsEmailVerified: false}}
	changes := &fakeChanges{latest: &repository.ContactChangeOTP{
		ID: uuid.New(), UserID: uid, Field: "email", NewValue: "new@example.com",
		CodeHash: hash, ExpiresAt: time.Now().Add(5 * time.Minute),
	}}
	h := newContactHandler(users, changes)

	rec := doJSON(h.Verify, contactCtx(uid), map[string]string{"code": code})
	if rec.Code != http.StatusOK {
		t.Fatalf("verify: %d %s", rec.Code, rec.Body.String())
	}
	if !changes.consumed {
		t.Error("OTP must be consumed")
	}
	if users.updated == nil || users.updated.Email != "new@example.com" {
		t.Fatalf("email must be committed, got %+v", users.updated)
	}
	if !users.updated.IsEmailVerified {
		t.Error("a code delivered to the new address proves ownership — IsEmailVerified must be set")
	}
}

func TestContactChange_WrongCodeCountsAndLocksOut(t *testing.T) {
	uid := uuid.New()
	_, hash, _ := auth.GenerateOTP()
	users := &fakeContactUsers{user: &models.User{ID: uid, Email: "old@example.com"}}
	changes := &fakeChanges{latest: &repository.ContactChangeOTP{
		ID: uuid.New(), UserID: uid, Field: "email", NewValue: "new@example.com",
		CodeHash: hash, ExpiresAt: time.Now().Add(5 * time.Minute),
	}}
	h := newContactHandler(users, changes)

	for i := 0; i < contactChangeMaxAttempts; i++ {
		rec := doJSON(h.Verify, contactCtx(uid), map[string]string{"code": "000000"})
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("wrong code %d must 401, got %d", i, rec.Code)
		}
	}
	if changes.attempts != contactChangeMaxAttempts {
		t.Fatalf("attempts must count, got %d", changes.attempts)
	}
	rec := doJSON(h.Verify, contactCtx(uid), map[string]string{"code": "000000"})
	var envelope struct {
		Error struct{ Code string } `json:"error"`
	}
	json.Unmarshal(rec.Body.Bytes(), &envelope)
	if envelope.Error.Code != models.ErrOTPAttemptsExceeded.Code {
		t.Fatalf("after max attempts must lock out, got %s: %s", envelope.Error.Code, rec.Body.String())
	}
	if users.updated != nil {
		t.Error("nothing may commit on wrong codes")
	}
}

func TestContactChange_VerifyRaceBackstop(t *testing.T) {
	uid := uuid.New()
	code, hash, _ := auth.GenerateOTP()
	users := &fakeContactUsers{user: &models.User{ID: uid, Email: "old@example.com"}, emailTaken: true}
	changes := &fakeChanges{latest: &repository.ContactChangeOTP{
		ID: uuid.New(), UserID: uid, Field: "email", NewValue: "new@example.com",
		CodeHash: hash, ExpiresAt: time.Now().Add(5 * time.Minute),
	}}
	h := newContactHandler(users, changes)

	rec := doJSON(h.Verify, contactCtx(uid), map[string]string{"code": code})
	if rec.Code != http.StatusConflict {
		t.Fatalf("identifier claimed since request must 409 at verify, got %d", rec.Code)
	}
	if users.updated != nil {
		t.Error("nothing may commit when the identifier was claimed")
	}
}
