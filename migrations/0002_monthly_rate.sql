-- Pay may be named per hour or per month.
--
-- `rate_mode` says which of the two amounts on users is in force. Both are
-- kept so switching modes does not discard the other figure. On shifts,
-- `rate_mode` says how to read the `rate_cents` snapshot: as an hourly rate,
-- or as the monthly salary the shift was paid a slice of. Existing rows are
-- hourly, which is what the default gives them.

ALTER TABLE users ADD COLUMN rate_mode TEXT NOT NULL DEFAULT 'hourly';
ALTER TABLE users ADD COLUMN monthly_rate_cents INTEGER NOT NULL DEFAULT 0;

ALTER TABLE shifts ADD COLUMN rate_mode TEXT NOT NULL DEFAULT 'hourly';
