package handlers

import (
	"os"
	"strings"
	"testing"
)

// Source-lock for the account-deletion guards (batch item 3): deletion is
// SOFT (anonymize-in-place) and gated — removing any guard fails the build
// tests. The data contract itself (identifiers freed, row kept, blocked) is
// proven behaviorally by TestSoftDeleteUser_FreesIdentifiersAndBlocks in
// the repository package against a migrated DB.
func TestDeleteUser_GuardsAndBiteNow(t *testing.T) {
	src, err := os.ReadFile("admin_delete_user.go")
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	s := string(src)

	for marker, why := range map[string]string{
		"CANNOT_DELETE_SELF":  "an admin must not delete their own account",
		"CANNOT_DELETE_ADMIN": "admin accounts must not be deletable",
		"ALREADY_DELETED":     "double-deletion must 409, not re-run",
		"SoftDeleteUser(":     "deletion must go through the anonymize path",
		"RevokeAllForUser(":   "live refresh tokens must be revoked immediately",
		"DisconnectUser(":     "live sockets must be cut immediately",
		"Invalidate(":         "the block cache must be dropped so enforcement is immediate",
	} {
		if !strings.Contains(s, marker) {
			t.Errorf("DeleteUser must contain %q — %s", marker, why)
		}
	}
	if strings.Contains(s, "DELETE FROM users") {
		t.Error("DeleteUser must never hard-delete the users row")
	}
}
