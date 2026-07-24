package handlers

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/drivebai/backend/internal/database"
	"github.com/drivebai/backend/internal/httputil"
	"github.com/drivebai/backend/internal/models"
	"github.com/drivebai/backend/internal/repository"
	"github.com/drivebai/backend/internal/urlsigner"
	"github.com/drivebai/backend/internal/ws"
)

// TestSignedAttachmentJSON_EndToEnd drives the REAL handlers → REAL repos →
// REAL DB → REAL signer → REAL JSON serialization and asserts every attachment
// URL in the actual response body carries a signature. This is the "look at the
// actual JSON, not the code" check the accident admin bug demanded.
//
// It is skipped unless TEST_DATABASE_URL points at a migrated, disposable DB:
//
//	TEST_DATABASE_URL="postgres://drivebai:drivebai_secret@localhost:5432/drivebai_verify?sslmode=disable" \
//	  go test ./internal/handlers/ -run TestSignedAttachmentJSON_EndToEnd -v
func TestSignedAttachmentJSON_EndToEnd(t *testing.T) {
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("set TEST_DATABASE_URL (migrated, disposable DB) to run the end-to-end signed-JSON check")
	}
	ctx := context.Background()
	db, err := database.Connect(ctx, dbURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer db.Close()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	signer := &PrivateURLSigner{Signer: urlsigner.New("test-secret"), TTL: time.Hour}
	hub := ws.NewHub(logger)

	ticketRepo := repository.NewTicketRepository(db)
	adminRepo := repository.NewAdminRepository(db)
	supportRepo := repository.NewSupportRepository(db)
	userRepo := repository.NewUserRepository(db)

	ticketHandler := NewTicketHandler(ticketRepo, adminRepo, hub, "/tmp/uploads", signer, logger)
	supportHandler := NewSupportHandler(supportRepo, adminRepo, hub, "/tmp/uploads", signer, logger)
	adminHandler := NewAdminHandler(adminRepo, userRepo, hub, signer, logger)

	// ── Seed: a regular user, an admin, a submitted ticket with evidence, and a
	// support chat with an attachment message. Unique emails so reruns don't
	// collide on the users.email unique index.
	suffix := uuid.NewString()[:8]
	var userID, adminID uuid.UUID
	if err := db.Pool.QueryRow(ctx, `INSERT INTO users (email, role, first_name, last_name)
		VALUES ($1,'driver','Test','User') RETURNING id`, "e2e_user_"+suffix+"@example.com").Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if err := db.Pool.QueryRow(ctx, `INSERT INTO users (email, role, first_name, last_name)
		VALUES ($1,'admin','Test','Admin') RETURNING id`, "e2e_admin_"+suffix+"@example.com").Scan(&adminID); err != nil {
		t.Fatalf("seed admin: %v", err)
	}

	var ticketID uuid.UUID
	if err := db.Pool.QueryRow(ctx, `INSERT INTO support_tickets (user_id, category, description, status, submitted_at)
		VALUES ($1,'renting','something broke','open',NOW()) RETURNING id`, userID).Scan(&ticketID); err != nil {
		t.Fatalf("seed ticket: %v", err)
	}
	ticketURL := "/uploads/tickets/" + ticketID.String() + "/evidence_" + suffix + ".jpg"
	if _, err := db.Pool.Exec(ctx, `INSERT INTO support_ticket_attachments (ticket_id, file_url, file_path, file_size, mime_type)
		VALUES ($1,$2,$3,123,'image/jpeg')`, ticketID, ticketURL, "/tmp"+ticketURL); err != nil {
		t.Fatalf("seed ticket attachment: %v", err)
	}

	var chatID, msgID uuid.UUID
	if err := db.Pool.QueryRow(ctx, `INSERT INTO support_chats (user_id) VALUES ($1) RETURNING id`, userID).Scan(&chatID); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	if err := db.Pool.QueryRow(ctx, `INSERT INTO support_messages (support_chat_id, sender_id, sender_kind, body)
		VALUES ($1,$2,'user','look at this') RETURNING id`, chatID, userID).Scan(&msgID); err != nil {
		t.Fatalf("seed message: %v", err)
	}
	chatURL := "/uploads/support/" + chatID.String() + "/attach_" + suffix + ".jpg"
	if _, err := db.Pool.Exec(ctx, `INSERT INTO support_message_attachments (message_id, file_url, file_path, file_size, mime_type)
		VALUES ($1,$2,$3,456,'image/jpeg')`, msgID, chatURL, "/tmp"+chatURL); err != nil {
		t.Fatalf("seed message attachment: %v", err)
	}

	// Cleanup the seeded rows (users cascade to everything else).
	defer db.Pool.Exec(ctx, `DELETE FROM users WHERE id = ANY($1)`, []uuid.UUID{userID, adminID})

	// ── Exercise each emit path and assert the ACTUAL JSON is signed.
	// Every attachment file_url in each response must carry ?sig=&exp=.
	check := func(name, body string, minURLs int) {
		urls := extractFileURLs(body)
		if len(urls) < minURLs {
			t.Errorf("%s: expected ≥%d file_url in JSON, got %d\nbody: %s", name, minURLs, len(urls), body)
			return
		}
		for _, u := range urls {
			if !strings.Contains(u, "sig=") || !strings.Contains(u, "exp=") {
				t.Errorf("%s: UNSIGNED file_url in actual JSON: %q", name, u)
			} else {
				t.Logf("%s: signed ✓ %s", name, u)
			}
		}
	}

	// 1) User GET /tickets/{id}
	check("ticket user Get",
		doReq(ticketHandler.Get, userID, models.RoleDriver, map[string]string{"id": ticketID.String()}), 1)
	// 2) Admin GET /admin/tickets/{id} (same handler serves both — the fix that
	//    keeps admin signing from drifting away from user signing).
	check("ticket admin Get",
		doReq(ticketHandler.AdminGet, adminID, models.RoleAdmin, map[string]string{"id": ticketID.String()}), 1)
	// 3) User GET /support/chats/{chatId}/messages
	check("support user ListMessages",
		doReq(supportHandler.ListMessages, userID, models.RoleDriver, map[string]string{"chatId": chatID.String()}), 1)
	// 4) Admin GET /admin/support/chats/{id}/messages
	check("support admin ListSupportMessages",
		doReq(adminHandler.ListSupportMessages, adminID, models.RoleAdmin, map[string]string{"id": chatID.String()}), 1)
}

// doReq runs a handler with an injected auth context + chi URL params and
// returns the response body.
func doReq(h http.HandlerFunc, userID uuid.UUID, role models.Role, params map[string]string) string {
	ctx := context.WithValue(context.Background(), httputil.UserIDKey, userID)
	ctx = context.WithValue(ctx, httputil.RoleKey, role)
	rctx := chi.NewRouteContext()
	for k, v := range params {
		rctx.URLParams.Add(k, v)
	}
	ctx = context.WithValue(ctx, chi.RouteCtxKey, rctx)
	req := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec.Body.String()
}

// extractFileURLs pulls every "file_url":"..." value out of a JSON blob,
// regardless of nesting, by walking the decoded structure.
func extractFileURLs(body string) []string {
	var root any
	if err := json.Unmarshal([]byte(body), &root); err != nil {
		return nil
	}
	var out []string
	var walk func(v any)
	walk = func(v any) {
		switch t := v.(type) {
		case map[string]any:
			for k, val := range t {
				if k == "file_url" {
					if s, ok := val.(string); ok && s != "" {
						out = append(out, s)
					}
				}
				walk(val)
			}
		case []any:
			for _, e := range t {
				walk(e)
			}
		}
	}
	walk(root)
	return out
}
