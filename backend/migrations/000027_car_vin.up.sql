-- Optional VIN column on car listings.
--
-- VIN is not required to list a car — owners can still create a listing
-- without one (existing rows stay valid). The field exists so the
-- VIN-decode autofill flow on iOS can persist the VIN it just decoded,
-- and so we have a stable identifier to cross-reference against the VIN
-- captured in accident reports.
--
-- We don't enforce the 17-char SAE format here: it's normalized + checked
-- client-side and by the backend handler that calls NHTSA, but cars
-- imported before this column existed have NULL and should remain so.
ALTER TABLE cars
    ADD COLUMN vin VARCHAR(32) NULL;
