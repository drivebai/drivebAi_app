package config

import (
	"testing"

	"github.com/google/uuid"
)

func TestSalesAllowlistParsing(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	t.Setenv("SALES_ALLOWLIST_USER_IDS", " "+a.String()+", "+b.String()+" ,not-a-uuid,,")
	ids := uuidListEnv("SALES_ALLOWLIST_USER_IDS")
	if len(ids) != 2 || ids[0] != a || ids[1] != b {
		t.Fatalf("parsed %v, want [%s %s]", ids, a, b)
	}
	if rej := uuidListRejects("SALES_ALLOWLIST_USER_IDS"); len(rej) != 1 || rej[0] != "not-a-uuid" {
		t.Fatalf("rejects = %v, want [not-a-uuid]", rej)
	}
	t.Setenv("SALES_ALLOWLIST_USER_IDS", "")
	if ids := uuidListEnv("SALES_ALLOWLIST_USER_IDS"); len(ids) != 0 {
		t.Fatalf("empty env parsed to %v", ids)
	}
	if rej := uuidListRejects("SALES_ALLOWLIST_USER_IDS"); len(rej) != 0 {
		t.Fatalf("empty env rejected %v", rej)
	}
}
