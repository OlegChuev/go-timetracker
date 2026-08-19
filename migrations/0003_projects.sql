-- Work is tracked per project, and the rate belongs to the project rather than
-- to the person. Someone can bill one client by the hour and draw a salary
-- from another at the same time.
--
-- Existing rates are carried onto a project named "My work" so nothing has to
-- be retyped, and existing shifts are attached to it so history keeps its pay.

CREATE TABLE IF NOT EXISTS projects (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id            INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    name               TEXT    NOT NULL,
    rate_mode          TEXT    NOT NULL DEFAULT 'hourly',
    hourly_rate_cents  INTEGER NOT NULL DEFAULT 0,
    monthly_rate_cents INTEGER NOT NULL DEFAULT 0,
    created_at         TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

-- Project names are unique per user, compared without regard to case so that
-- "Acme" and "acme" cannot both exist and confuse the picker.
CREATE UNIQUE INDEX IF NOT EXISTS uq_project_name_per_user
    ON projects (user_id, name COLLATE NOCASE);

INSERT INTO projects (user_id, name, rate_mode, hourly_rate_cents, monthly_rate_cents)
SELECT id, 'My work', rate_mode, hourly_rate_cents, monthly_rate_cents
FROM users
WHERE hourly_rate_cents > 0 OR monthly_rate_cents > 0;

ALTER TABLE shifts ADD COLUMN project_id INTEGER REFERENCES projects (id) ON DELETE CASCADE;

UPDATE shifts
SET project_id = (SELECT p.id FROM projects p WHERE p.user_id = shifts.user_id LIMIT 1)
WHERE project_id IS NULL;

CREATE INDEX IF NOT EXISTS idx_shifts_project ON shifts (project_id, work_date);

-- Rates now live on projects, so the columns on users are dead weight.
ALTER TABLE users DROP COLUMN rate_mode;
ALTER TABLE users DROP COLUMN hourly_rate_cents;
ALTER TABLE users DROP COLUMN monthly_rate_cents;
