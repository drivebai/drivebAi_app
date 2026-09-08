package repository

import "errors"

// Amendment sentinel errors (batch: rolling amendments) — the handler
// maps each to its own 409 so the driver sees why the accept refused.
var (
	ErrAmendmentGone      = errors.New("amendment offer is no longer open")
	ErrAmendmentCycleOpen = errors.New("a billing cycle is in flight — amendments apply between cycles")
	ErrAmendmentNoMandate = errors.New("no activated billing mandate to amend")
)
