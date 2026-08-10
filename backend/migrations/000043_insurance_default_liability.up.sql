-- 000043_insurance_default_liability
--
-- Insurance semantics correction (7/31 batch item 7): insurance_coverage
-- describes the level the OWNER's car carries — confirmed on the Vehicle
-- Documents step — not a minimum imposed on the renter, and the default is
-- the state minimum, not full coverage.
--
-- Column and enum are unchanged (the enum type itself is the value guard);
-- existing rows keep whatever the owner picked — no data is rewritten,
-- because an owner's stored answer is not invalidated by the label moving.
-- Only NEW rows created without an explicit value change behavior, and the
-- API handler default flips in the same deploy.

ALTER TABLE cars ALTER COLUMN insurance_coverage SET DEFAULT 'liability_only';
