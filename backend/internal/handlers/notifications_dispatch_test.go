package handlers

import (
	"testing"

	"github.com/google/uuid"

	"github.com/drivebai/backend/internal/models"
)

// TestBuildPushRequest_PerType pins down the per-NotificationType shape of
// the PushRequest we hand to the APNs HTTP/2 layer. iOS reads `type` and
// the related ID (chat_id / lease_request_id) to route on tap, and the
// collapse-id + category drive grouping + actions — so a regression here
// would silently change how every push looks on the springboard.
func TestBuildPushRequest_PerType(t *testing.T) {
	chatID := uuid.MustParse("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee")
	leaseID := uuid.MustParse("11111111-2222-3333-4444-555555555555")

	cases := []struct {
		name           string
		notifType      models.NotificationType
		chat           *uuid.UUID
		lease          *uuid.UUID
		wantCategory   string
		wantCollapseID string
		wantPriority   int
		wantDataKeys   []string // keys that MUST be present in Data
	}{
		{
			name:           "lease_request",
			notifType:      models.NotificationTypeLeaseRequest,
			lease:          &leaseID,
			wantCategory:   "LEASE_REQUEST",
			wantCollapseID: "lease:" + leaseID.String(),
			wantPriority:   10,
			wantDataKeys:   []string{"type", "lease_request_id"},
		},
		{
			name:           "payment",
			notifType:      models.NotificationTypePayment,
			lease:          &leaseID,
			wantCategory:   "PAYMENT",
			wantCollapseID: "payment:" + leaseID.String(),
			wantPriority:   10,
			wantDataKeys:   []string{"type", "lease_request_id"},
		},
		{
			name:           "key_handover",
			notifType:      models.NotificationTypeKeyHandover,
			lease:          &leaseID,
			wantCategory:   "KEY_HANDOVER",
			wantCollapseID: "handover:" + leaseID.String(),
			wantPriority:   10,
			wantDataKeys:   []string{"type", "lease_request_id"},
		},
		{
			name:           "chat_message",
			notifType:      models.NotificationTypeChatMessage,
			chat:           &chatID,
			wantCategory:   "CHAT_MESSAGE",
			wantCollapseID: "chat:" + chatID.String(),
			wantPriority:   5,
			wantDataKeys:   []string{"type", "chat_id"},
		},
		{
			name:           "system",
			notifType:      models.NotificationTypeSystem,
			wantCategory:   "SYSTEM",
			wantCollapseID: "", // system events are distinct, no grouping
			wantPriority:   5,
			wantDataKeys:   []string{"type"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := buildPushRequest(c.notifType, "title", "body", 3, c.chat, c.lease)

			if req.Category != c.wantCategory {
				t.Errorf("category: got %q want %q", req.Category, c.wantCategory)
			}
			if req.CollapseID != c.wantCollapseID {
				t.Errorf("collapse_id: got %q want %q", req.CollapseID, c.wantCollapseID)
			}
			if req.Priority != c.wantPriority {
				t.Errorf("priority: got %d want %d", req.Priority, c.wantPriority)
			}
			if req.Badge == nil || *req.Badge != 3 {
				t.Errorf("badge: got %v want %d (unread_count must propagate)", req.Badge, 3)
			}
			if req.Sound != "default" {
				t.Errorf("sound: got %q want %q", req.Sound, "default")
			}
			for _, k := range c.wantDataKeys {
				if _, ok := req.Data[k]; !ok {
					t.Errorf("Data[%q] missing — iOS DeepLinkRouter relies on this key", k)
				}
			}
			if got := req.Data["type"]; got != string(c.notifType) {
				t.Errorf("Data[type]: got %q want %q", got, string(c.notifType))
			}
			if c.lease != nil {
				if got := req.Data["lease_request_id"]; got != c.lease.String() {
					t.Errorf("Data[lease_request_id]: got %q want %q", got, c.lease.String())
				}
			}
			if c.chat != nil {
				if got := req.Data["chat_id"]; got != c.chat.String() {
					t.Errorf("Data[chat_id]: got %q want %q", got, c.chat.String())
				}
			}
		})
	}
}

// TestBuildPushRequest_BadgeNeverNil — the iOS springboard count goes stale
// if we omit `badge` from any push. Pass 0 explicitly when there's nothing
// unread, never leave it nil.
func TestBuildPushRequest_BadgeNeverNil(t *testing.T) {
	req := buildPushRequest(models.NotificationTypeSystem, "t", "b", 0, nil, nil)
	if req.Badge == nil {
		t.Fatal("badge must be set even when unread_count=0")
	}
	if *req.Badge != 0 {
		t.Errorf("badge: got %d want 0", *req.Badge)
	}
}

// TestBuildPushRequest_UnknownTypeDefaultsSafely — getting an unknown
// notification type from a future caller must not crash + must still
// deliver. Defaults to SYSTEM category, low priority, no collapse.
func TestBuildPushRequest_UnknownTypeDefaultsSafely(t *testing.T) {
	req := buildPushRequest(models.NotificationType("bogus_future_type"), "t", "b", 1, nil, nil)
	if req.Category != "SYSTEM" {
		t.Errorf("unknown type should default category to SYSTEM, got %q", req.Category)
	}
	if req.Priority != 5 {
		t.Errorf("unknown type should default to priority 5, got %d", req.Priority)
	}
	if req.Data["type"] != "bogus_future_type" {
		t.Errorf("Data[type] should still echo the raw value, got %q", req.Data["type"])
	}
}

// TestFormatChatPreview_Truncates makes sure long messages don't blow past
// the iOS banner cap. APNs accepts much larger payloads but the lock screen
// only renders ~178 chars; we cap conservatively at 140.
func TestFormatChatPreview_Truncates(t *testing.T) {
	long := make([]byte, 300)
	for i := range long {
		long[i] = 'x'
	}
	got := formatChatPreview(string(long))
	if len([]rune(got)) > 141 { // 140 + ellipsis
		t.Errorf("preview too long: %d", len([]rune(got)))
	}
}

// TestFormatChatPreview_CollapsesMultiline keeps the iOS lock-screen banner
// readable when the user sends a multi-line message. We render only the
// first line, appending an ellipsis to hint there's more.
func TestFormatChatPreview_CollapsesMultiline(t *testing.T) {
	got := formatChatPreview("line one\nline two\nline three")
	want := "line one…"
	if got != want {
		t.Errorf("multiline preview: got %q want %q", got, want)
	}
}
