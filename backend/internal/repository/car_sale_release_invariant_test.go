package repository

import (
	"context"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// These tests guard the Car-Sale-release invariants that live in SQL executed
// against Postgres. The unit suite has no live database, so the query text and
// the migration predicate are asserted structurally (source-level) alongside
// the one branch that IS reachable without a DB: the empty-VIN exemption.

// ── Empty-VIN exemption (reachable behavior) ─────────────────────────────────

// TestExistsByVIN_EmptyVINExempt: an empty / whitespace VIN short-circuits to
// (false, nil) BEFORE any pool access, so it is safe to run against a
// zero-value repository (nil db). This is the shared front-half of the
// three-place VIN invariant.
func TestExistsByVIN_EmptyVINExempt(t *testing.T) {
	r := &CarRepository{} // nil db: the empty-VIN branch must not touch it
	for _, vin := range []string{"", "   ", "\t"} {
		got, err := r.ExistsByVIN(context.Background(), vin)
		if err != nil {
			t.Errorf("ExistsByVIN(%q) err = %v", vin, err)
		}
		if got {
			t.Errorf("ExistsByVIN(%q) = true, want false (empty VINs never collide)", vin)
		}
		got, err = r.ExistsByVINExcludingID(context.Background(), vin, uuid.New())
		if err != nil {
			t.Errorf("ExistsByVINExcludingID(%q) err = %v", vin, err)
		}
		if got {
			t.Errorf("ExistsByVINExcludingID(%q) = true, want false", vin)
		}
	}
}

// ── Three-place VIN predicate invariant (item 2) ─────────────────────────────

// TestVINThreePlaceInvariant asserts the partial-unique-index predicate
// (migration 000035) is mirrored verbatim by ExistsByVIN and
// ExistsByVINExcludingID: uniqueness is scoped to live rows (non-archived,
// non-sold, non-empty VIN). A sold OR archived listing therefore frees its VIN
// for a re-reviewed relist. Drift in any one place fails here.
func TestVINThreePlaceInvariant(t *testing.T) {
	repoSrc, err := os.ReadFile("car_repository.go")
	if err != nil {
		t.Fatalf("read car_repository.go: %v", err)
	}
	existsByVIN := extractFuncSrc(t, string(repoSrc), "func (r *CarRepository) ExistsByVIN(")
	existsExcl := extractFuncSrc(t, string(repoSrc), "func (r *CarRepository) ExistsByVINExcludingID(")

	// The predicate fragments every place must share.
	fragments := []string{"vin <> ''", "archived_at IS NULL", "status <> 'sold'"}
	for _, frag := range fragments {
		if !strings.Contains(existsByVIN, frag) {
			t.Errorf("ExistsByVIN query missing predicate %q", frag)
		}
		if !strings.Contains(existsExcl, frag) {
			t.Errorf("ExistsByVINExcludingID query missing predicate %q", frag)
		}
	}
	// The excluding variant must additionally scope out the row itself.
	if !strings.Contains(existsExcl, "id <> $2") {
		t.Error("ExistsByVINExcludingID must exclude the current row (id <> $2)")
	}

	// Migration 000035 index predicate (whitespace-normalized).
	migSrc, err := os.ReadFile("../../migrations/000035_car_vin_and_status_dedupe.up.sql")
	if err != nil {
		t.Fatalf("read migration 000035: %v", err)
	}
	mig := normalizeWS(string(migSrc))
	wantIdx := "CREATE UNIQUE INDEX cars_vin_unique_lower_idx ON cars (LOWER(vin)) " +
		"WHERE vin IS NOT NULL AND vin <> '' AND archived_at IS NULL AND status <> 'sold'"
	if !strings.Contains(mig, wantIdx) {
		t.Errorf("migration 000035 index predicate does not match the repository queries;\nwant to contain: %s", wantIdx)
	}
}

// ── Driver docs: license is the only required document (item 6) ──────────────

// TestHasRequiredDocuments_LicenseOnly guards the relaxation: a driver needs
// ONLY a driver's license — registration must never gate them. The check runs
// in SQL, so this locks the query shape (license param, count >= 1) and the
// absence of any registration requirement.
func TestHasRequiredDocuments_LicenseOnly(t *testing.T) {
	src, err := os.ReadFile("document_repository.go")
	if err != nil {
		t.Fatalf("read document_repository.go: %v", err)
	}
	fn := extractFuncSrc(t, string(src), "func (r *DocumentRepository) HasRequiredDocuments(")

	if !strings.Contains(fn, "models.DocumentDriversLicense") {
		t.Error("HasRequiredDocuments must key on the driver's license document")
	}
	if !strings.Contains(fn, "count >= 1") {
		t.Error("HasRequiredDocuments must pass on a single license (count >= 1)")
	}
	if strings.Contains(fn, "DocumentRegistration") {
		t.Error("HasRequiredDocuments must NOT require a registration document")
	}
	if strings.Contains(fn, ">= 2") {
		t.Error("HasRequiredDocuments must not demand two document types anymore")
	}
}

// ── Admin approval publishes (item 7) ────────────────────────────────────────

// TestSetCarApproved_PublishesOnApproval guards that approval is the single
// publish gate: the SAME UPDATE that sets is_approved=true moves status
// 'pending' → 'available', and leaves any other status (rented/sold/paused)
// untouched via ELSE. It also returns owner_id + status so the handler can
// notify the owner. Enforced in SQL, so asserted on the query text.
func TestSetCarApproved_PublishesOnApproval(t *testing.T) {
	src, err := os.ReadFile("admin_repository.go")
	if err != nil {
		t.Fatalf("read admin_repository.go: %v", err)
	}
	fn := normalizeWS(extractFuncSrc(t, string(src), "func (r *AdminRepository) SetCarApproved("))

	wants := []string{
		"WHEN $2 = true AND status = 'pending'", // publish only from pending, only on approve
		"THEN 'available'",                      // → available (cars.status is VARCHAR — no enum cast)
		"ELSE status",                           // rented/sold/paused left untouched
		"RETURNING owner_id, status::text",      // owner + resulting status for the notify path
	}
	for _, w := range wants {
		if !strings.Contains(fn, w) {
			t.Errorf("SetCarApproved SQL must contain %q", w)
		}
	}
}

// ── BoS title-condition 'other requires other-text' rule (item 8) ────────────

// TestBOSTitleConditionOtherRule guards the seller-declared title-condition
// validation in the BoS update path: an invalid brand is rejected, and
// selecting 'other' requires the free-text detail (from this patch or the row).
// The validation is inside a DB-bound repository method, so the rule is locked
// structurally.
func TestBOSTitleConditionOtherRule(t *testing.T) {
	src, err := os.ReadFile("purchase_request_repository.go")
	if err != nil {
		t.Fatalf("read purchase_request_repository.go: %v", err)
	}
	body := string(src)
	// The enum-validity guard.
	if !regexp.MustCompile(`if\s+!tc\.IsValid\(\)\s*\{\s*return nil, models\.ErrInvalidTitleCondition`).MatchString(normalizeWS(body)) {
		t.Error("BoS update must reject an invalid title condition with ErrInvalidTitleCondition")
	}
	// The 'other' → other-text requirement.
	n := normalizeWS(body)
	otherIdx := strings.Index(n, "tc == models.TitleConditionOther")
	if otherIdx < 0 {
		t.Fatal("BoS update must special-case TitleConditionOther")
	}
	reqIdx := strings.Index(n, "return nil, models.ErrTitleConditionOtherRequired")
	if reqIdx < 0 {
		t.Fatal("BoS update must require other-text for 'other'")
	}
	if reqIdx < otherIdx {
		t.Error("the other-text requirement must be guarded by the TitleConditionOther branch")
	}
}

// ── source-scan helpers ──────────────────────────────────────────────────────

var wsRe = regexp.MustCompile(`\s+`)

// normalizeWS collapses every run of whitespace to a single space so
// multi-line SQL literals can be matched with flat substrings.
func normalizeWS(s string) string {
	return strings.TrimSpace(wsRe.ReplaceAllString(s, " "))
}

// extractFuncSrc returns the source of the function whose signature starts at
// header, up to the next top-level `\nfunc ` declaration.
func extractFuncSrc(t *testing.T, src, header string) string {
	t.Helper()
	start := strings.Index(src, header)
	if start < 0 {
		t.Fatalf("function %q not found", header)
	}
	rest := src[start+len(header):]
	if next := strings.Index(rest, "\nfunc "); next >= 0 {
		return rest[:next]
	}
	return rest
}
