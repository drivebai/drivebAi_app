package handlers

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	"github.com/drivebai/backend/internal/models"
)

// Pins the wire shape of SharedDocumentsListResponse. iOS decodes
// `driver_documents` and `vehicle_documents` by name, so a JSON-tag drift
// here would silently turn the Chat → Requests document section blank.
func TestSharedDocumentsListResponse_WireShape(t *testing.T) {
	resp := SharedDocumentsListResponse{
		ViewerRole: "owner",
		DriverDocuments: []SharedDocumentResponse{{
			ID:         uuid.New(),
			DocumentID: uuid.New(),
			UploaderID: uuid.New(),
			Type:       models.DocumentDriversLicense,
			FileName:   "license.jpg",
			FileURL:    "/uploads/abc/license.jpg?sig=deadbeef&exp=1",
		}},
		VehicleDocuments: []VehicleDocumentResponse{},
		Documents:        []SharedDocumentResponse{},
	}
	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"viewer_role", "driver_documents", "vehicle_documents", "documents"} {
		if _, ok := m[key]; !ok {
			t.Errorf("missing wire key %q in response: %s", key, raw)
		}
	}

	// driver_documents inner shape: signed file_url MUST round-trip.
	dd, _ := m["driver_documents"].([]any)
	if len(dd) != 1 {
		t.Fatalf("expected 1 driver doc, got %d", len(dd))
	}
	inner, _ := dd[0].(map[string]any)
	if u, _ := inner["file_url"].(string); u != "/uploads/abc/license.jpg?sig=deadbeef&exp=1" {
		t.Errorf("file_url not preserved: %v", inner["file_url"])
	}
}

// Pins the VehicleDocumentResponse wire shape — same drift risk on the
// driver side.
func TestVehicleDocumentResponse_WireShape(t *testing.T) {
	resp := SharedDocumentsListResponse{
		ViewerRole:      "driver",
		DriverDocuments: []SharedDocumentResponse{},
		VehicleDocuments: []VehicleDocumentResponse{{
			ID:           uuid.New(),
			DocumentType: models.CarDocRegistration,
			FileName:     "registration.pdf",
			FileURL:      "/uploads/cars/abc/documents/registration.pdf?sig=feedf00d&exp=1",
		}},
		Documents: []SharedDocumentResponse{},
	}
	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	vd, _ := m["vehicle_documents"].([]any)
	if len(vd) != 1 {
		t.Fatalf("expected 1 vehicle doc, got %d", len(vd))
	}
	inner, _ := vd[0].(map[string]any)
	for _, key := range []string{"id", "document_type", "file_name", "file_url"} {
		if _, ok := inner[key]; !ok {
			t.Errorf("missing wire key %q on vehicle doc: %s", key, raw)
		}
	}
	if dt, _ := inner["document_type"].(string); dt != "registration" {
		t.Errorf("document_type drift: got %q want %q", dt, "registration")
	}
}
