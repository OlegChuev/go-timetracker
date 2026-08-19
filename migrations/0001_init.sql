-- Time tracker schema (SQLite).
--
-- Instants are stored as RFC 3339 UTC text so the file stays readable in the
-- sqlite3 shell and still sorts correctly. `work_date` is the calendar date in
-- the user's own timezone at the moment the shift started, which is what the
-- daily, weekly and monthly reports group by. A shift running past midnight is
-- therefore counted entirely on the day it began.

CREATE TABLE IF NOT EXISTS users (
    id                INTEGER PRIMARY KEY,           -- Telegram user id
    chat_id           INTEGER NOT NULL,
    username          TEXT    NOT NULL DEFAULT '',
    first_name        TEXT    NOT NULL DEFAULT '',
    hourly_rate_cents INTEGER NOT NULL DEFAULT 0,    -- rate in minor units
    currency          TEXT    NOT NULL DEFAULT 'USD',
    timezone          TEXT    NOT NULL DEFAULT 'UTC',-- IANA name
    pending_action    TEXT    NOT NULL DEFAULT '',   -- text input being awaited
    created_at        TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at        TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE TABLE IF NOT EXISTS shifts (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id          INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    started_at       TEXT    NOT NULL,               -- UTC
    ended_at         TEXT        NULL,               -- UTC, NULL while running
    work_date        TEXT    NOT NULL,               -- local date the shift started on
    timezone         TEXT    NOT NULL DEFAULT 'UTC', -- timezone in force at start
    duration_seconds INTEGER     NULL,
    rate_cents       INTEGER     NULL,               -- rate snapshot taken at close
    earnings_cents   INTEGER     NULL,
    currency         TEXT        NULL,               -- currency snapshot taken at close
    created_at       TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),

    CHECK (ended_at IS NULL OR ended_at >= started_at)
);

-- A user may hold at most one running shift. The partial index applies only to
-- open rows, leaving any number of finished shifts per user.
CREATE UNIQUE INDEX IF NOT EXISTS uq_one_open_shift_per_user
    ON shifts (user_id) WHERE ended_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_shifts_user_work_date ON shifts (user_id, work_date);
CREATE INDEX IF NOT EXISTS idx_shifts_user_started   ON shifts (user_id, started_at);

-- Keep users.updated_at current without the application having to set it.
CREATE TRIGGER IF NOT EXISTS trg_users_updated_at
AFTER UPDATE ON users FOR EACH ROW
BEGIN
    UPDATE users SET updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE id = OLD.id;
END;
