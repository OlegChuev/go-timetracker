package storage

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/oleg/go-timetracker/internal/worklog"
)

// migrationsFS is the real migrations directory, so tests run against the
// same SQL that ships.
func migrationsFS() fs.FS { return os.DirFS(filepath.Join("..", "..", "migrations")) }

// hourlyRate builds an hourly Rate for the tests.
func hourlyRate(cents int64) worklog.Rate {
	return worklog.Rate{Mode: worklog.RateModeHourly, HourlyCents: cents}
}

// newTestStore opens a throwaway database with all migrations applied.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	store := New(db)
	if err := store.Migrate(context.Background(), migrationsFS()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return store
}

// seedUser inserts a user together with one project paid 25.00 an hour, and
// returns the project since almost every shift test needs it.
func seedUser(t *testing.T, s *Store, id int64) worklog.Project {
	t.Helper()
	ctx := context.Background()
	if _, err := s.EnsureUser(ctx, worklog.User{ID: id, ChatID: id, FirstName: "Test"}); err != nil {
		t.Fatalf("ensure user: %v", err)
	}
	project, err := s.CreateProject(ctx, id, "Acme")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := s.UpdateProjectRate(ctx, id, project.ID, hourlyRate(2500)); err != nil {
		t.Fatalf("set rate: %v", err)
	}
	project.Rate = hourlyRate(2500)
	return project
}

// Migrate must be safe to run on every start.
func TestMigrateIsIdempotent(t *testing.T) {
	s := newTestStore(t)
	for i := 0; i < 3; i++ {
		if err := s.Migrate(context.Background(), migrationsFS()); err != nil {
			t.Fatalf("re-migrate %d: %v", i, err)
		}
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	names, err := fs.Glob(migrationsFS(), "*.sql")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if count != len(names) {
		t.Errorf("recorded %d migrations, want %d", count, len(names))
	}
}

// A database created before the monthly rate existed must upgrade in place
// rather than needing a reset.
func TestMigrateUpgradesAnExistingDatabase(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	store := New(db)

	// Apply only the first migration, as an older deployment would have.
	first, err := os.ReadFile(filepath.Join("..", "..", "migrations", "0001_init.sql"))
	if err != nil {
		t.Fatalf("read first migration: %v", err)
	}
	if _, err := db.ExecContext(ctx, createMigrationsTable); err != nil {
		t.Fatalf("create migrations table: %v", err)
	}
	if err := store.applyMigration(ctx, "0001_init.sql", string(first)); err != nil {
		t.Fatalf("apply first migration: %v", err)
	}

	// Seed a user and a finished shift under the old schema.
	if _, err := db.ExecContext(ctx,
		`INSERT INTO users (id, chat_id, hourly_rate_cents) VALUES (1, 1, 2500)`); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO shifts (user_id, started_at, ended_at, work_date, duration_seconds,
		                     rate_cents, earnings_cents, currency)
		 VALUES (1, '2026-08-19T06:00:00.000Z', '2026-08-19T14:00:00.000Z', '2026-08-19',
		         28800, 2500, 20000, 'USD')`); err != nil {
		t.Fatalf("seed shift: %v", err)
	}

	if err := store.Migrate(ctx, migrationsFS()); err != nil {
		t.Fatalf("upgrade: %v", err)
	}

	// The old per user rate must have moved onto a project, not been lost.
	projects, err := store.ListProjects(ctx, 1)
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}
	if len(projects) != 1 {
		t.Fatalf("projects after upgrade = %d, want the migrated one", len(projects))
	}
	if projects[0].Rate.HourlyCents != 2500 || projects[0].Rate.IsMonthly() {
		t.Errorf("migrated rate = %+v, want 2500 hourly", projects[0].Rate)
	}

	// The historical shift must keep its earnings untouched.
	day := time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC)
	totals, err := store.TotalsBetween(ctx, 1, day, day)
	if err != nil {
		t.Fatalf("totals: %v", err)
	}
	if totals.Shifts != 1 || totals.EarningsCents != 20000 || totals.Seconds != 28800 {
		t.Errorf("totals after upgrade = %+v, want the seeded shift unchanged", totals)
	}

	// And it must have been attached to the migrated project, so it still
	// shows up in the per project breakdown.
	byProject, err := store.TotalsByProject(ctx, 1, day, day)
	if err != nil {
		t.Fatalf("totals by project: %v", err)
	}
	if len(byProject) != 1 || byProject[0].Totals.EarningsCents != 20000 {
		t.Errorf("per project totals after upgrade = %+v, want the shift attached", byProject)
	}
}

func TestEnsureUserAppliesSchemaDefaults(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	u, err := s.EnsureUser(ctx, worklog.User{ID: 1, ChatID: 1, Username: "oleg", FirstName: "Oleg"})
	if err != nil {
		t.Fatalf("ensure user: %v", err)
	}
	if u.Currency != "USD" || u.Timezone != "UTC" {
		t.Errorf("defaults = %q/%q, want USD/UTC", u.Currency, u.Timezone)
	}
	if u.PendingAction != "" {
		t.Errorf("unexpected initial pending state: %q", u.PendingAction)
	}

	// A second contact refreshes the profile but must not reset settings.
	tz := "Europe/Kyiv"
	if err := s.UpdateSettings(ctx, 1, worklog.SettingsPatch{Timezone: &tz}); err != nil {
		t.Fatalf("update settings: %v", err)
	}
	again, err := s.EnsureUser(ctx, worklog.User{ID: 1, ChatID: 99, Username: "renamed", FirstName: "Oleg K"})
	if err != nil {
		t.Fatalf("re-ensure user: %v", err)
	}
	if again.Timezone != tz {
		t.Errorf("timezone = %q, want it preserved", again.Timezone)
	}
	if again.ChatID != 99 || again.Username != "renamed" {
		t.Errorf("profile not refreshed: chat=%d username=%q", again.ChatID, again.Username)
	}
}

func TestUpdateSettingsTouchesOnlyGivenFields(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedUser(t, s, 1)

	tz := "Europe/Kyiv"
	if err := s.UpdateSettings(ctx, 1, worklog.SettingsPatch{Timezone: &tz}); err != nil {
		t.Fatalf("update timezone: %v", err)
	}
	u, _ := s.GetUser(ctx, 1)
	if u.Timezone != tz || u.Currency != "USD" {
		t.Errorf("unexpected row after partial update: %+v", u)
	}

	// No pointers set at all must be a no-op rather than an error.
	if err := s.UpdateSettings(ctx, 1, worklog.SettingsPatch{}); err != nil {
		t.Errorf("empty update: %v", err)
	}
}

func TestOpenAndCloseShiftRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	project := seedUser(t, s, 1)

	start := time.Date(2026, 8, 19, 6, 0, 0, 0, time.UTC)
	workDate := time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC)
	opened, err := s.OpenShift(ctx, 1, project.ID, start, workDate, "Europe/Kyiv")
	if err != nil {
		t.Fatalf("open shift: %v", err)
	}
	if opened.ID == 0 {
		t.Error("opened shift has no id")
	}

	found, err := s.FindOpenShift(ctx, 1)
	if err != nil {
		t.Fatalf("find open shift: %v", err)
	}
	if found == nil {
		t.Fatal("open shift not found")
	}
	if !found.StartedAt.Equal(start) {
		t.Errorf("started_at = %v, want %v", found.StartedAt, start)
	}
	if got := found.WorkDate.Format("2006-01-02"); got != "2026-08-19" {
		t.Errorf("work_date = %s, want 2026-08-19", got)
	}
	if found.Timezone != "Europe/Kyiv" {
		t.Errorf("timezone = %q, want Europe/Kyiv", found.Timezone)
	}

	closed, err := s.CloseShift(ctx, 1, start.Add(8*time.Hour), hourlyRate(2500), "USD")
	if err != nil {
		t.Fatalf("close shift: %v", err)
	}
	if closed.DurationSeconds != 8*3600 {
		t.Errorf("duration = %d, want %d", closed.DurationSeconds, 8*3600)
	}
	if closed.EarningsCents != 20000 {
		t.Errorf("earnings = %d, want 20000", closed.EarningsCents)
	}

	if after, err := s.FindOpenShift(ctx, 1); err != nil || after != nil {
		t.Errorf("shift still open after close: %v, %v", after, err)
	}
}

// The partial unique index is what stops a second concurrent shift.
func TestSecondOpenShiftIsRejected(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	project := seedUser(t, s, 1)

	start := time.Date(2026, 8, 19, 6, 0, 0, 0, time.UTC)
	day := time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC)
	if _, err := s.OpenShift(ctx, 1, project.ID, start, day, "UTC"); err != nil {
		t.Fatalf("first open: %v", err)
	}
	_, err := s.OpenShift(ctx, 1, project.ID, start.Add(time.Hour), day, "UTC")
	if !errors.Is(err, worklog.ErrShiftAlreadyOpen) {
		t.Fatalf("second open error = %v, want ErrShiftAlreadyOpen", err)
	}

	// Closing the first must free the slot for a new one.
	if _, err := s.CloseShift(ctx, 1, start.Add(2*time.Hour), hourlyRate(2500), "USD"); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := s.OpenShift(ctx, 1, project.ID, start.Add(3*time.Hour), day, "UTC"); err != nil {
		t.Errorf("open after close: %v", err)
	}
}

// The index must be per user, not global.
func TestDifferentUsersMayWorkAtOnce(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	project := seedUser(t, s, 1)
	second := seedUser(t, s, 2)

	start := time.Date(2026, 8, 19, 6, 0, 0, 0, time.UTC)
	day := time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC)
	if _, err := s.OpenShift(ctx, 1, project.ID, start, day, "UTC"); err != nil {
		t.Fatalf("user 1 open: %v", err)
	}
	if _, err := s.OpenShift(ctx, 2, second.ID, start, day, "UTC"); err != nil {
		t.Errorf("user 2 open: %v", err)
	}
}

func TestCloseWithoutOpenShift(t *testing.T) {
	s := newTestStore(t)
	seedUser(t, s, 1)
	_, err := s.CloseShift(context.Background(), 1, time.Now().UTC(), hourlyRate(2500), "USD")
	if !errors.Is(err, worklog.ErrNoOpenShift) {
		t.Errorf("error = %v, want ErrNoOpenShift", err)
	}
}

// A backwards clock must clamp to a zero length shift, not violate the CHECK.
func TestCloseClampsBackwardsClock(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	project := seedUser(t, s, 1)

	start := time.Date(2026, 8, 19, 6, 0, 0, 0, time.UTC)
	day := time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC)
	if _, err := s.OpenShift(ctx, 1, project.ID, start, day, "UTC"); err != nil {
		t.Fatalf("open: %v", err)
	}
	closed, err := s.CloseShift(ctx, 1, start.Add(-2*time.Hour), hourlyRate(2500), "USD")
	if err != nil {
		t.Fatalf("close: %v", err)
	}
	if closed.DurationSeconds != 0 || closed.EarningsCents != 0 {
		t.Errorf("duration = %d, earnings = %d, want 0/0", closed.DurationSeconds, closed.EarningsCents)
	}
}

func TestTotalsBetweenGroupsByWorkDate(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	project := seedUser(t, s, 1)

	// Three finished shifts across two days, plus one still running.
	days := []struct {
		date  string
		hours int
	}{
		{"2026-08-17", 4},
		{"2026-08-19", 3},
		{"2026-08-19", 5},
	}
	base := time.Date(2026, 8, 17, 6, 0, 0, 0, time.UTC)
	for i, d := range days {
		day, _ := time.Parse("2006-01-02", d.date)
		start := base.Add(time.Duration(i*24) * time.Hour)
		if _, err := s.OpenShift(ctx, 1, project.ID, start, day, "UTC"); err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		if _, err := s.CloseShift(ctx, 1, start.Add(time.Duration(d.hours)*time.Hour), hourlyRate(2500), "USD"); err != nil {
			t.Fatalf("close %d: %v", i, err)
		}
	}
	running, _ := time.Parse("2006-01-02", "2026-08-19")
	if _, err := s.OpenShift(ctx, 1, project.ID, base.Add(100*time.Hour), running, "UTC"); err != nil {
		t.Fatalf("open running: %v", err)
	}

	cases := []struct {
		name         string
		from, to     string
		wantSeconds  int64
		wantShifts   int
		wantEarnings int64
	}{
		{"single day", "2026-08-19", "2026-08-19", 8 * 3600, 2, 20000},
		{"whole week", "2026-08-17", "2026-08-23", 12 * 3600, 3, 30000},
		{"empty day", "2026-08-18", "2026-08-18", 0, 0, 0},
		{"whole month", "2026-08-01", "2026-08-31", 12 * 3600, 3, 30000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			from, _ := time.Parse("2006-01-02", tc.from)
			to, _ := time.Parse("2006-01-02", tc.to)
			got, err := s.TotalsBetween(ctx, 1, from, to)
			if err != nil {
				t.Fatalf("totals: %v", err)
			}
			if got.Seconds != tc.wantSeconds || got.Shifts != tc.wantShifts || got.EarningsCents != tc.wantEarnings {
				t.Errorf("got %+v, want seconds=%d shifts=%d earnings=%d",
					got, tc.wantSeconds, tc.wantShifts, tc.wantEarnings)
			}
		})
	}
}

// Concurrent start taps must yield exactly one shift.
func TestConcurrentStartsCreateOneShift(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	project := seedUser(t, s, 1)

	start := time.Date(2026, 8, 19, 6, 0, 0, 0, time.UTC)
	day := time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC)

	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		succeeded int
	)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.OpenShift(ctx, 1, project.ID, start, day, "UTC"); err == nil {
				mu.Lock()
				succeeded++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if succeeded != 1 {
		t.Errorf("%d starts succeeded, want exactly 1", succeeded)
	}
}

// Concurrent stop taps must close the shift once and pay once.
func TestConcurrentStopsCloseOnce(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	project := seedUser(t, s, 1)

	start := time.Date(2026, 8, 19, 6, 0, 0, 0, time.UTC)
	day := time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC)
	if _, err := s.OpenShift(ctx, 1, project.ID, start, day, "UTC"); err != nil {
		t.Fatalf("open: %v", err)
	}

	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		succeeded int
	)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.CloseShift(ctx, 1, start.Add(time.Hour), hourlyRate(2500), "USD"); err == nil {
				mu.Lock()
				succeeded++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if succeeded != 1 {
		t.Errorf("%d stops succeeded, want exactly 1", succeeded)
	}
	totals, err := s.TotalsBetween(ctx, 1, day, day)
	if err != nil {
		t.Fatalf("totals: %v", err)
	}
	if totals.Shifts != 1 || totals.EarningsCents != 2500 {
		t.Errorf("totals = %+v, want 1 shift paying 2500", totals)
	}
}

// Open must create the parent directory rather than failing on a fresh deploy.
func TestOpenCreatesParentDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "deeper", "tt.db")
	db, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if _, err := os.Stat(filepath.Dir(path)); err != nil {
		t.Errorf("parent directory missing: %v", err)
	}
}

func TestOpenRejectsEmptyPath(t *testing.T) {
	if _, err := Open(context.Background(), ""); err == nil {
		t.Error("expected an error for an empty path")
	}
}

// A monthly rate must survive the round trip through the database, and the
// shift must record which mode it was paid under.
func TestMonthlyRateRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	project := seedUser(t, s, 1)

	monthly := worklog.Rate{
		Mode: worklog.RateModeMonthly, HourlyCents: 2500, MonthlyCents: 336000,
	}
	if err := s.UpdateProjectRate(ctx, 1, project.ID, monthly); err != nil {
		t.Fatalf("set monthly rate: %v", err)
	}

	stored, err := s.GetProject(ctx, 1, project.ID)
	if err != nil {
		t.Fatalf("get project: %v", err)
	}
	if !stored.Rate.IsMonthly() || stored.Rate.MonthlyCents != 336000 {
		t.Fatalf("stored rate = %+v, want a monthly 336000", stored.Rate)
	}
	// The hourly amount must be kept alongside it.
	if stored.Rate.HourlyCents != 2500 {
		t.Errorf("hourly amount = %d, want it kept at 2500", stored.Rate.HourlyCents)
	}

	start := time.Date(2026, 8, 19, 6, 0, 0, 0, time.UTC)
	day := time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC)
	if _, err := s.OpenShift(ctx, 1, project.ID, start, day, "UTC"); err != nil {
		t.Fatalf("open: %v", err)
	}
	closed, err := s.CloseShift(ctx, 1, start.Add(8*time.Hour), stored.Rate, "USD")
	if err != nil {
		t.Fatalf("close: %v", err)
	}
	if closed.EarningsCents != 15508 {
		t.Errorf("earnings = %d, want 15508", closed.EarningsCents)
	}

	// Read the snapshot back from SQL to prove the mode was persisted.
	var (
		storedMode string
		storedRate int64
	)
	if err := s.db.QueryRowContext(ctx,
		`SELECT rate_mode, rate_cents FROM shifts WHERE id = ?`, closed.ID).
		Scan(&storedMode, &storedRate); err != nil {
		t.Fatalf("read shift: %v", err)
	}
	if storedMode != worklog.RateModeMonthly || storedRate != 336000 {
		t.Errorf("stored snapshot = %s/%d, want monthly/336000", storedMode, storedRate)
	}
	if rebuilt := worklog.NewRate(storedMode, storedRate); rebuilt.Hourly() != 1938 {
		t.Errorf("rebuilt rate implies %d/h, want 1938", rebuilt.Hourly())
	}
}

// Mixed hourly and monthly shifts must total correctly together.
func TestTotalsMixHourlyAndMonthlyShifts(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	project := seedUser(t, s, 1)

	day := time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC)
	base := time.Date(2026, 8, 19, 6, 0, 0, 0, time.UTC)
	rates := []worklog.Rate{
		{Mode: worklog.RateModeHourly, HourlyCents: 2500},
		{Mode: worklog.RateModeMonthly, MonthlyCents: 336000},
	}
	for i, rate := range rates {
		start := base.Add(time.Duration(i*9) * time.Hour)
		if _, err := s.OpenShift(ctx, 1, project.ID, start, day, "UTC"); err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		if _, err := s.CloseShift(ctx, 1, start.Add(8*time.Hour), rate, "USD"); err != nil {
			t.Fatalf("close %d: %v", i, err)
		}
	}

	totals, err := s.TotalsBetween(ctx, 1, day, day)
	if err != nil {
		t.Fatalf("totals: %v", err)
	}
	// 8h at 25.00 is 200.00, 8h of a 3360.00 month is 155.08.
	if totals.EarningsCents != 20000+15508 {
		t.Errorf("earnings = %d, want 35508", totals.EarningsCents)
	}
	if totals.Shifts != 2 || totals.Seconds != 16*3600 {
		t.Errorf("totals = %+v, want 2 shifts over 16 hours", totals)
	}
}

// A database created before the migration runner existed has no
// schema_migrations table, so every migration looks unapplied. The first one
// must be safe to re-run against the tables it already created.
func TestMigrateAdoptsAnUntrackedDatabase(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "untracked.db")
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	// Apply the original schema the way the old code did: straight exec, with
	// nothing recorded anywhere.
	first, err := os.ReadFile(filepath.Join("..", "..", "migrations", "0001_init.sql"))
	if err != nil {
		t.Fatalf("read first migration: %v", err)
	}
	if _, err := db.ExecContext(ctx, string(first)); err != nil {
		t.Fatalf("apply original schema: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO users (id, chat_id, hourly_rate_cents) VALUES (5, 5, 1800)`); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	store := New(db)
	if err := store.Migrate(ctx, migrationsFS()); err != nil {
		t.Fatalf("adopt and upgrade: %v", err)
	}

	projects, err := store.ListProjects(ctx, 5)
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}
	if len(projects) != 1 || projects[0].Rate.HourlyCents != 1800 {
		t.Errorf("projects = %+v, want the seeded rate migrated onto one", projects)
	}
}
