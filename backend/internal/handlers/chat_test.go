package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/drivebai/backend/internal/httputil"
)

// These tests exercise the auth + input-validation paths that return BEFORE any
// repository access. Full integration testing of UploadAttachment's DB +
// broadcast flow would require a test Postgres + WS hub setup that this
// codebase does not currently provide.

func TestUploadAttachment_Unauthorized(t *testing.T) {
	h := &ChatHandler{}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/chats/"+uuid.New().String()+"/attachments", nil)
	rr := httptest.NewRecorder()

	h.UploadAttachment(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rr.Code)
	}
}

func TestUploadAttachment_InvalidChatID(t *testing.T) {
	h := &ChatHandler{}

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("chatId", "not-a-uuid")

	req := httptest.NewRequest(http.MethodPost, "/api/v1/chats/not-a-uuid/attachments", nil)
	req = req.WithContext(context.WithValue(req.Context(), httputil.UserIDKey, uuid.New()))
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rr := httptest.NewRecorder()
	h.UploadAttachment(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rr.Code)
	}
}

func TestSendMessage_Unauthorized(t *testing.T) {
	h := &ChatHandler{}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/chats/"+uuid.New().String()+"/messages", nil)
	rr := httptest.NewRecorder()

	h.SendMessage(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rr.Code)
	}
}
