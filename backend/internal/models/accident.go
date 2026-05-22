package models

import (
	"time"

	"github.com/google/uuid"
)

// AccidentStatus represents the lifecycle state of an accident report.
type AccidentStatus string

const (
	AccidentStatusDraft     AccidentStatus = "draft"
	AccidentStatusSubmitted AccidentStatus = "submitted"
	AccidentStatusInReview  AccidentStatus = "in_review"
	AccidentStatusResolved  AccidentStatus = "resolved"
)

// Driver1Info / Driver2Info contains the person + registrant data for each driver.
// Stored as JSONB in the accidents table.
type DriverInfo struct {
	// Driver Info section
	DriverLicenseID        string `json:"driver_license_id"`
	StateOfLicense         string `json:"state_of_license"`
	DriverName             string `json:"driver_name"`
	Address                string `json:"address"`
	City                   string `json:"city"`
	State                  string `json:"state"`
	ZIP                    string `json:"zip"`
	DOB                    string `json:"dob"`
	PeopleInVehicle        string `json:"people_in_vehicle"`
	PublicPropertyDamaged  string `json:"public_property_damaged"`
	Injuries               string `json:"injuries"`
	// Registrant section
	RegistrantName    string `json:"registrant_name"`
	RegistrantAddress string `json:"registrant_address"`
	RegistrantCity    string `json:"registrant_city"`
	RegistrantState   string `json:"registrant_state"`
	RegistrantZIP     string `json:"registrant_zip"`
	PlateNumber       string `json:"plate_number"`
	StateOfReg        string `json:"state_of_reg"`
	VehicleYearMake   string `json:"vehicle_year_make"`
	VehicleType       string `json:"vehicle_type"`
	InsCode           string `json:"ins_code"`
}

// VehicleDamage stores damage description + diagram selection.
type VehicleDamage struct {
	Description string `json:"description"`
	Diagram     int    `json:"diagram"` // 0–8
}

// InsuranceInfo stores insurance data for vehicle 1.
type InsuranceInfo struct {
	InsuranceCompany  string `json:"insurance_company"`
	VIN               string `json:"vin"`
	PolicyNumber      string `json:"policy_number"`
	PolicyPeriodFrom  string `json:"policy_period_from"`
	PolicyPeriodTo    string `json:"policy_period_to"`
}

// OtherInfo stores miscellaneous accident metadata.
type OtherInfo struct {
	Month              string `json:"month"`
	Day                string `json:"day"`
	Year               string `json:"year"`
	DayOfWeek          string `json:"day_of_week"`
	Time               string `json:"time"`
	NumVehicles        string `json:"num_vehicles"`
	NumInjured         string `json:"num_injured"`
	NumKilled          string `json:"num_killed"`
	PoliceInvestigated string `json:"police_investigated"`
}

// Accident is the core domain model.
type Accident struct {
	ID                  uuid.UUID      `json:"id"`
	ReporterID          uuid.UUID      `json:"reporter_id"`
	RelatedChatID       *uuid.UUID     `json:"related_chat_id,omitempty"`
	RelatedCarID        *uuid.UUID     `json:"related_car_id,omitempty"`
	Status              AccidentStatus `json:"status"`
	Driver1Info         *DriverInfo    `json:"driver1_info,omitempty"`
	Driver2Info         *DriverInfo    `json:"driver2_info,omitempty"`
	VehicleDamage       *VehicleDamage `json:"vehicle_damage,omitempty"`
	AccidentDescription string         `json:"accident_description,omitempty"`
	InsuranceInfo       *InsuranceInfo `json:"insurance_info,omitempty"`
	OtherInfo           *OtherInfo     `json:"other_info,omitempty"`
	SignatureURL        string         `json:"signature_url,omitempty"`
	SignatureSignedAt   *time.Time     `json:"signature_signed_at,omitempty"`
	SubmittedAt         *time.Time     `json:"submitted_at,omitempty"`
	Attachments         []AccidentAttachment `json:"attachments"`
	CreatedAt           time.Time      `json:"created_at"`
	UpdatedAt           time.Time      `json:"updated_at"`
}

// AttachmentSlot identifies the purpose of an uploaded file.
type AttachmentSlot string

const (
	SlotAccidentPhoto     AttachmentSlot = "accident_photo"
	SlotAccidentVideo     AttachmentSlot = "accident_video"
	SlotDriver1License    AttachmentSlot = "driver1_license"
	SlotDriver2Plate      AttachmentSlot = "driver2_plate"
	SlotSecondVehicleDocs AttachmentSlot = "second_vehicle_docs"
)

// AccidentAttachment represents a file attached to an accident report.
type AccidentAttachment struct {
	ID         uuid.UUID      `json:"id"`
	AccidentID uuid.UUID      `json:"accident_id"`
	Slot       AttachmentSlot `json:"slot"`
	FileURL    string         `json:"file_url"`
	FileSize   int64          `json:"file_size"`
	MimeType   string         `json:"mime_type"`
	CreatedAt  time.Time      `json:"created_at"`
}
