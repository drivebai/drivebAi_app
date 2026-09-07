DROP INDEX IF EXISTS idx_billing_cycles_needs_action;
DROP INDEX IF EXISTS idx_billing_cycles_due;
DROP INDEX IF EXISTS uq_billing_cycles_lease_cycle;
DROP TABLE IF EXISTS billing_cycles;
DROP INDEX IF EXISTS uq_billing_consents_active;
DROP TABLE IF EXISTS lease_billing_consents;
