package handlers

import (
	"os"
	"strings"
	"testing"

	"github.com/drivebai/backend/internal/models"
)

// Insurance level defaults to Liability Only (7/31 batch item 7) — the
// client's explicit ask, previously full_coverage. Source-lock in the style
// of the reservation-guard tests: reverting the fallback fails the build.
func TestCarCreate_InsuranceDefaultsToLiabilityOnly(t *testing.T) {
	src, err := os.ReadFile("car.go")
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	s := string(src)
	if !strings.Contains(s, "car.InsuranceCoverage = models.InsuranceLiabilityOnly") {
		t.Error("Create must default insurance_coverage to InsuranceLiabilityOnly when omitted")
	}
	if strings.Contains(s, "car.InsuranceCoverage = models.InsuranceFullCoverage") {
		t.Error("full_coverage must not be the fallback anywhere in car.go")
	}
}

func TestInsuranceCoverage_IsValid(t *testing.T) {
	if !models.InsuranceLiabilityOnly.IsValid() || !models.InsuranceFullCoverage.IsValid() {
		t.Error("both enum values must be valid")
	}
	if models.InsuranceCoverage("comprehensive").IsValid() {
		t.Error("unknown values must be invalid")
	}
}
