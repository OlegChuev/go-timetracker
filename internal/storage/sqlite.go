// Package storage implements the worklog repository on SQLite.
package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	sqlite3 "modernc.org/sqlite"
	sqlite3lib "modernc.org/sqlite/lib"

	"github.com/oleg/go-timetracker/internal/worklog"
)

// Layouts for the text columns. Instants are RFC 3339 in UTC; work_date is a
// bare calendar date.
const (
	timeLayout = "2006-01-02T15:04:05.000Z"
	dateLayout = "2006-01-02"
)

// Store is a worklog.Repository backed by SQLite.
type Store struct {
	db *sql.DB
}

// New wraps an existing pool.
func New(db *sql.DB) *Store { return &Store{db: db} }

// Open opens the database file, creating it if needed, and applies the pragmas
// the bot depends on: foreign keys, WAL for concurrent reads, and a busy
// timeout so a writer waits its turn instead of failing.
func Open(ctx context.Context, path string) (*sql.DB, error) {
	if path == "" {
		return nil, errors.New("database path is empty")
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := ensureDir(dir); err != nil {
			return nil, err
		}
	}

	// EscapedPath keeps directory separators intact while escaping characters
	// such as spaces that would otherwise break the file: URI.
	dsn := "file:" + (&url.URL{Path: path}).EscapedPath() +
		"?_pragma=busy_timeout(5000)" +
		"&_pragma=foreign_keys(1)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=synchronous(NORMAL)"

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}
	// SQLite takes one writer at a time; a small pool avoids lock contention
	// while still letting reads run in parallel under WAL.
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(time.Hour)

	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		db.Close()
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}
	return db, nil
}

// EnsureUser inserts the user on first contact, or refreshes the mutable
// Telegram profile fields on later ones, and returns the stored row.
func (s *Store) EnsureUser(ctx context.Context, u worklog.User) (worklog.User, error) {
	const q = `
		INSERT INTO users (id, chat_id, username, first_name)
		VALUES (?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET
			chat_id    = excluded.chat_id,
			username   = excluded.username,
			first_name = excluded.first_name`
	if _, err := s.db.ExecContext(ctx, q, u.ID, u.ChatID, u.Username, u.FirstName); err != nil {
		return worklog.User{}, fmt.Errorf("ensure user %d: %w", u.ID, err)
	}
	return s.GetUser(ctx, u.ID)
}

// GetUser loads a user row.
func (s *Store) GetUser(ctx context.Context, userID int64) (worklog.User, error) {
	const q = `
		SELECT id, chat_id, username, first_name, currency, timezone, pending_action
		FROM users WHERE id = ?`
	var u worklog.User
	err := s.db.QueryRowContext(ctx, q, userID).Scan(
		&u.ID, &u.ChatID, &u.Username, &u.FirstName,
		&u.Currency, &u.Timezone, &u.PendingAction,
	)
	if err != nil {
		return worklog.User{}, fmt.Errorf("get user %d: %w", userID, err)
	}
	return u, nil
}

// UpdateSettings writes only the settings the patch names.
func (s *Store) UpdateSettings(ctx context.Context, userID int64, patch worklog.SettingsPatch) error {
	set := make([]string, 0, 3)
	args := make([]any, 0, 4)
	addString := func(column string, value *string) {
		if value != nil {
			set = append(set, column+" = ?")
			args = append(args, *value)
		}
	}
	addString("currency", patch.Currency)
	addString("timezone", patch.Timezone)
	addString("pending_action", patch.PendingAction)
	if len(set) == 0 {
		return nil
	}

	args = append(args, userID)
	q := "UPDATE users SET " + strings.Join(set, ", ") + " WHERE id = ?"
	if _, err := s.db.ExecContext(ctx, q, args...); err != nil {
		return fmt.Errorf("update settings for %d: %w", userID, err)
	}
	return nil
}

// OpenShift starts a shift. The partial unique index turns a double tap of the
// start button into a constraint violation rather than a second open shift.
func (s *Store) OpenShift(ctx context.Context, userID, projectID int64, startedAt, workDate time.Time, tz string) (worklog.Shift, error) {
	const q = `
		INSERT INTO shifts (user_id, project_id, started_at, work_date, timezone)
		VALUES (?, ?, ?, ?, ?)`
	res, err := s.db.ExecContext(ctx, q, userID, projectID,
		startedAt.UTC().Format(timeLayout), workDate.Format(dateLayout), tz)
	if err != nil {
		if isUniqueViolation(err) {
			return worklog.Shift{}, worklog.ErrShiftAlreadyOpen
		}
		return worklog.Shift{}, fmt.Errorf("open shift for %d: %w", userID, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return worklog.Shift{}, fmt.Errorf("open shift for %d: %w", userID, err)
	}
	return worklog.Shift{
		ID:        id,
		UserID:    userID,
		ProjectID: projectID,
		StartedAt: startedAt.UTC(),
		WorkDate:  workDate,
		Timezone:  tz,
	}, nil
}

// CloseShift ends the running shift and stores its duration, the rate that
// applied at close time and the resulting earnings.
func (s *Store) CloseShift(ctx context.Context, userID int64, endedAt time.Time, rate worklog.Rate, currency string) (worklog.Shift, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return worklog.Shift{}, fmt.Errorf("close shift for %d: %w", userID, err)
	}
	defer tx.Rollback()

	const selectQ = `
		SELECT id, project_id, started_at, work_date, timezone
		FROM shifts
		WHERE user_id = ? AND ended_at IS NULL`
	var (
		shift               worklog.Shift
		rawStarted, rawDate string
	)
	err = tx.QueryRowContext(ctx, selectQ, userID).
		Scan(&shift.ID, &shift.ProjectID, &rawStarted, &rawDate, &shift.Timezone)
	if errors.Is(err, sql.ErrNoRows) {
		return worklog.Shift{}, worklog.ErrNoOpenShift
	}
	if err != nil {
		return worklog.Shift{}, fmt.Errorf("close shift for %d: %w", userID, err)
	}
	if shift.StartedAt, shift.WorkDate, err = parseShiftTimes(rawStarted, rawDate); err != nil {
		return worklog.Shift{}, fmt.Errorf("close shift for %d: %w", userID, err)
	}

	endedAt = endedAt.UTC()
	// A clock that jumped backwards must not produce a negative shift, which
	// the CHECK constraint would reject anyway.
	if endedAt.Before(shift.StartedAt) {
		endedAt = shift.StartedAt
	}
	seconds := int64(endedAt.Sub(shift.StartedAt).Seconds())
	earnings := rate.Earnings(seconds)

	const updateQ = `
		UPDATE shifts
		SET ended_at = ?, duration_seconds = ?, rate_mode = ?, rate_cents = ?,
		    earnings_cents = ?, currency = ?
		WHERE id = ? AND ended_at IS NULL`
	res, err := tx.ExecContext(ctx, updateQ,
		endedAt.Format(timeLayout), seconds, rate.Mode, rate.Amount(),
		earnings, currency, shift.ID)
	if err != nil {
		return worklog.Shift{}, fmt.Errorf("close shift for %d: %w", userID, err)
	}
	// A concurrent stop that won the race leaves nothing for this one to do.
	if affected, err := res.RowsAffected(); err == nil && affected == 0 {
		return worklog.Shift{}, worklog.ErrNoOpenShift
	}
	if err := tx.Commit(); err != nil {
		return worklog.Shift{}, fmt.Errorf("close shift for %d: %w", userID, err)
	}

	shift.UserID = userID
	shift.EndedAt = &endedAt
	shift.DurationSeconds = seconds
	shift.Rate = rate
	shift.EarningsCents = earnings
	shift.Currency = currency
	return shift, nil
}

// FindOpenShift returns the running shift for a user, or nil.
func (s *Store) FindOpenShift(ctx context.Context, userID int64) (*worklog.Shift, error) {
	const q = `
		SELECT s.id, s.user_id, s.project_id, COALESCE(p.name, ''),
		       s.started_at, s.work_date, s.timezone
		FROM shifts s
		LEFT JOIN projects p ON p.id = s.project_id
		WHERE s.user_id = ? AND s.ended_at IS NULL`
	var (
		shift               worklog.Shift
		rawStarted, rawDate string
	)
	err := s.db.QueryRowContext(ctx, q, userID).
		Scan(&shift.ID, &shift.UserID, &shift.ProjectID, &shift.ProjectName,
			&rawStarted, &rawDate, &shift.Timezone)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find open shift for %d: %w", userID, err)
	}
	if shift.StartedAt, shift.WorkDate, err = parseShiftTimes(rawStarted, rawDate); err != nil {
		return nil, fmt.Errorf("find open shift for %d: %w", userID, err)
	}
	return &shift, nil
}

// TotalsBetween sums finished shifts whose work_date falls in [from, to].
func (s *Store) TotalsBetween(ctx context.Context, userID int64, from, to time.Time) (worklog.Totals, error) {
	const q = `
		SELECT COALESCE(SUM(duration_seconds), 0),
		       COALESCE(SUM(earnings_cents), 0),
		       COUNT(*)
		FROM shifts
		WHERE user_id = ? AND ended_at IS NOT NULL AND work_date BETWEEN ? AND ?`
	var t worklog.Totals
	err := s.db.QueryRowContext(ctx, q, userID, from.Format(dateLayout), to.Format(dateLayout)).
		Scan(&t.Seconds, &t.EarningsCents, &t.Shifts)
	if err != nil {
		return worklog.Totals{}, fmt.Errorf("totals for %d: %w", userID, err)
	}
	return t, nil
}

// parseInstant decodes one RFC 3339 timestamp column.
func parseInstant(raw string) (time.Time, error) {
	return time.Parse(timeLayout, raw)
}

// parseShiftTimes decodes the text timestamp columns of a shift row.
func parseShiftTimes(rawStarted, rawDate string) (started, workDate time.Time, err error) {
	if started, err = time.Parse(timeLayout, rawStarted); err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("parse started_at %q: %w", rawStarted, err)
	}
	if workDate, err = time.Parse(dateLayout, rawDate); err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("parse work_date %q: %w", rawDate, err)
	}
	return started, workDate, nil
}

// isUniqueViolation reports whether err is a SQLite uniqueness failure.
func isUniqueViolation(err error) bool {
	var sqErr *sqlite3.Error
	if !errors.As(err, &sqErr) {
		return false
	}
	code := sqErr.Code()
	return code == sqlite3lib.SQLITE_CONSTRAINT_UNIQUE || code == sqlite3lib.SQLITE_CONSTRAINT_PRIMARYKEY
}
