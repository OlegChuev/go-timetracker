package worklog_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/oleg/go-timetracker/internal/storage"
	"github.com/oleg/go-timetracker/internal/worklog"
)

// newStore opens a throwaway SQLite database with the real schema. Running the
// service against real storage rather than a hand written fake keeps the two
// from drifting apart.
func newStore(t *testing.T) *storage.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := storage.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	store := storage.New(db)
	if err := store.Migrate(context.Background(), os.DirFS(filepath.Join("..", "..", "migrations"))); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return store
}

// fixture is a service wired to real storage, with a clock the test advances.
type fixture struct {
	svc   *worklog.Service
	store *storage.Store
	now   time.Time
	user  worklog.User
}

// newFixture registers a user in Europe/Kyiv and returns a ready service.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	store := newStore(t)
	f := &fixture{store: store, now: time.Date(2026, 8, 19, 6, 0, 0, 0, time.UTC)}
	f.svc = worklog.NewService(store, func() time.Time { return f.now })

	ctx := context.Background()
	user, err := f.svc.Register(ctx, worklog.User{ID: 7, ChatID: 7, FirstName: "Oleg"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, err := f.svc.SetTimezone(ctx, 7, "Europe/Kyiv"); err != nil {
		t.Fatalf("set timezone: %v", err)
	}
	f.user, _ = f.svc.User(ctx, 7)
	_ = user
	return f
}

// project creates a project paid hourly at the given cents per hour.
func (f *fixture) project(t *testing.T, name, rate string) worklog.Project {
	t.Helper()
	ctx := context.Background()
	p, err := f.svc.CreateProject(ctx, 7, name)
	if err != nil {
		t.Fatalf("create project %s: %v", name, err)
	}
	if rate == "" {
		return p
	}
	p, err = f.svc.SetProjectHourlyRate(ctx, 7, p.ID, rate)
	if err != nil {
		t.Fatalf("set rate on %s: %v", name, err)
	}
	return p
}

// reload returns the user with current settings.
func (f *fixture) reload(t *testing.T) worklog.User {
	t.Helper()
	u, err := f.svc.User(context.Background(), 7)
	if err != nil {
		t.Fatalf("reload user: %v", err)
	}
	return u
}

func TestStartStopComputesDurationAndPay(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	p := f.project(t, "Acme", "25")

	if _, err := f.svc.Start(ctx, 7, p.ID); err != nil {
		t.Fatalf("Start: %v", err)
	}
	f.now = f.now.Add(8 * time.Hour)
	shift, err := f.svc.Stop(ctx, 7)
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if shift.DurationSeconds != 8*3600 {
		t.Errorf("duration = %d, want %d", shift.DurationSeconds, 8*3600)
	}
	if shift.EarningsCents != 20000 {
		t.Errorf("earnings = %d, want 20000", shift.EarningsCents)
	}
	if shift.ProjectName != "Acme" {
		t.Errorf("project = %q, want Acme", shift.ProjectName)
	}
	// 06:00 UTC is 09:00 in Kyiv, so the shift lands on the 19th either way.
	if got := shift.WorkDate.Format("2006-01-02"); got != "2026-08-19" {
		t.Errorf("work date = %s, want 2026-08-19", got)
	}
}

// One shift at a time, whichever project the second attempt names.
func TestSecondShiftIsRejectedEvenOnAnotherProject(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	first := f.project(t, "Acme", "25")
	second := f.project(t, "Globex", "40")

	if _, err := f.svc.Start(ctx, 7, first.ID); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	if _, err := f.svc.Start(ctx, 7, first.ID); !errors.Is(err, worklog.ErrShiftAlreadyOpen) {
		t.Errorf("restart on the same project = %v, want ErrShiftAlreadyOpen", err)
	}
	if _, err := f.svc.Start(ctx, 7, second.ID); !errors.Is(err, worklog.ErrShiftAlreadyOpen) {
		t.Errorf("start on another project = %v, want ErrShiftAlreadyOpen", err)
	}

	// Stopping frees the slot for either project.
	f.now = f.now.Add(time.Hour)
	if _, err := f.svc.Stop(ctx, 7); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if _, err := f.svc.Start(ctx, 7, second.ID); err != nil {
		t.Errorf("start after stop: %v", err)
	}
}

func TestStopWithoutStart(t *testing.T) {
	f := newFixture(t)
	if _, err := f.svc.Stop(context.Background(), 7); !errors.Is(err, worklog.ErrNoOpenShift) {
		t.Errorf("Stop error = %v, want ErrNoOpenShift", err)
	}
}

func TestStartRequiresARateOnThatProject(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	unpaid := f.project(t, "Unpaid", "")
	paid := f.project(t, "Paid", "25")

	if _, err := f.svc.Start(ctx, 7, unpaid.ID); !errors.Is(err, worklog.ErrRateNotSet) {
		t.Errorf("Start on a rateless project = %v, want ErrRateNotSet", err)
	}
	// A rate on a sibling project must not make the first one startable.
	if _, err := f.svc.Start(ctx, 7, paid.ID); err != nil {
		t.Errorf("Start on the paid project: %v", err)
	}
}

func TestStartRejectsAnotherUsersProject(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	// A second account with its own project.
	if _, err := f.svc.Register(ctx, worklog.User{ID: 8, ChatID: 8, FirstName: "Other"}); err != nil {
		t.Fatalf("register other: %v", err)
	}
	theirs, err := f.svc.CreateProject(ctx, 8, "Theirs")
	if err != nil {
		t.Fatalf("create their project: %v", err)
	}
	if _, err := f.svc.SetProjectHourlyRate(ctx, 8, theirs.ID, "99"); err != nil {
		t.Fatalf("set their rate: %v", err)
	}

	if _, err := f.svc.Start(ctx, 7, theirs.ID); !errors.Is(err, worklog.ErrProjectNotFound) {
		t.Errorf("Start on another user's project = %v, want ErrProjectNotFound", err)
	}
}

// Each project keeps its own rate, and each shift is paid its project's rate.
func TestProjectsCarryIndependentRates(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	hourly := f.project(t, "Acme", "25")
	salaried, err := f.svc.CreateProject(ctx, 7, "Day job")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	if _, err := f.svc.SetProjectMonthlyRate(ctx, 7, salaried.ID, "3360"); err != nil {
		t.Fatalf("set monthly rate: %v", err)
	}

	// Two hours on the hourly project: 50.00.
	if _, err := f.svc.Start(ctx, 7, hourly.ID); err != nil {
		t.Fatalf("Start hourly: %v", err)
	}
	f.now = f.now.Add(2 * time.Hour)
	first, err := f.svc.Stop(ctx, 7)
	if err != nil {
		t.Fatalf("Stop hourly: %v", err)
	}
	if first.EarningsCents != 5000 {
		t.Errorf("hourly shift = %d, want 5000", first.EarningsCents)
	}

	// Eight hours on the salaried project: 155.08.
	if _, err := f.svc.Start(ctx, 7, salaried.ID); err != nil {
		t.Fatalf("Start salaried: %v", err)
	}
	f.now = f.now.Add(8 * time.Hour)
	second, err := f.svc.Stop(ctx, 7)
	if err != nil {
		t.Fatalf("Stop salaried: %v", err)
	}
	if second.EarningsCents != 15508 {
		t.Errorf("salaried shift = %d, want 15508", second.EarningsCents)
	}
	if !second.Rate.IsMonthly() {
		t.Errorf("snapshotted rate = %+v, want monthly", second.Rate)
	}

	summary, err := f.svc.Summarize(ctx, f.reload(t))
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if summary.Today.EarningsCents != 5000+15508 {
		t.Errorf("day total = %d, want 20508", summary.Today.EarningsCents)
	}
	if len(summary.ByProject) != 2 {
		t.Fatalf("per project rows = %d, want 2", len(summary.ByProject))
	}
	// Busiest first: the eight hour salaried project leads.
	if summary.ByProject[0].Project.Name != "Day job" {
		t.Errorf("first row = %s, want Day job", summary.ByProject[0].Project.Name)
	}
	if summary.ByProject[1].Totals.EarningsCents != 5000 {
		t.Errorf("second row earned %d, want 5000", summary.ByProject[1].Totals.EarningsCents)
	}
}

// Raising a project's rate must not repay shifts already finished.
func TestRateChangeDoesNotRewriteHistory(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	p := f.project(t, "Acme", "25")

	if _, err := f.svc.Start(ctx, 7, p.ID); err != nil {
		t.Fatalf("Start: %v", err)
	}
	f.now = f.now.Add(2 * time.Hour)
	if _, err := f.svc.Stop(ctx, 7); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if _, err := f.svc.SetProjectHourlyRate(ctx, 7, p.ID, "50"); err != nil {
		t.Fatalf("raise rate: %v", err)
	}
	f.now = f.now.Add(time.Hour)
	if _, err := f.svc.Start(ctx, 7, p.ID); err != nil {
		t.Fatalf("second Start: %v", err)
	}
	f.now = f.now.Add(2 * time.Hour)
	second, err := f.svc.Stop(ctx, 7)
	if err != nil {
		t.Fatalf("second Stop: %v", err)
	}
	if second.EarningsCents != 10000 {
		t.Errorf("second shift = %d, want 10000", second.EarningsCents)
	}

	summary, err := f.svc.Summarize(ctx, f.reload(t))
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	// 2h at 25 plus 2h at 50, not 4h at either rate.
	if summary.Today.EarningsCents != 15000 {
		t.Errorf("day total = %d, want 15000", summary.Today.EarningsCents)
	}
}

// A shift running past local midnight stays on the day it began.
func TestOvernightShiftCountsOnStartDay(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	p := f.project(t, "Acme", "25")

	f.now = time.Date(2026, 8, 19, 19, 0, 0, 0, time.UTC) // 22:00 Kyiv
	if _, err := f.svc.Start(ctx, 7, p.ID); err != nil {
		t.Fatalf("Start: %v", err)
	}
	f.now = f.now.Add(5 * time.Hour) // 03:00 Kyiv the next day
	shift, err := f.svc.Stop(ctx, 7)
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if got := shift.WorkDate.Format("2006-01-02"); got != "2026-08-19" {
		t.Errorf("work date = %s, want 2026-08-19", got)
	}

	summary, err := f.svc.Summarize(ctx, f.reload(t))
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	// "Today" is now the 20th in Kyiv, so the shift belongs to yesterday.
	if summary.Today.Shifts != 0 {
		t.Errorf("today shifts = %d, want 0", summary.Today.Shifts)
	}
	if summary.Week.Seconds != 5*3600 {
		t.Errorf("week seconds = %d, want %d", summary.Week.Seconds, 5*3600)
	}
}

func TestSummarizeReportsRunningShift(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	p := f.project(t, "Acme", "25")

	if _, err := f.svc.Start(ctx, 7, p.ID); err != nil {
		t.Fatalf("Start: %v", err)
	}
	f.now = f.now.Add(90 * time.Minute)

	summary, err := f.svc.Summarize(ctx, f.reload(t))
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if summary.Open == nil {
		t.Fatal("Open = nil, want the running shift")
	}
	if summary.Open.ProjectName != "Acme" {
		t.Errorf("running project = %q, want Acme", summary.Open.ProjectName)
	}
	if summary.OpenFor != 90*time.Minute {
		t.Errorf("OpenFor = %v, want 1h30m", summary.OpenFor)
	}
	// An unfinished shift must not be counted in the totals yet.
	if summary.Today.Seconds != 0 || len(summary.ByProject) != 0 {
		t.Errorf("running shift leaked into totals: %+v", summary.Today)
	}
}

func TestProjectNamesAreUniquePerUser(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.project(t, "Acme", "25")

	if _, err := f.svc.CreateProject(ctx, 7, "Acme"); !errors.Is(err, worklog.ErrProjectExists) {
		t.Errorf("duplicate name = %v, want ErrProjectExists", err)
	}
	// The check ignores case, so near duplicates cannot confuse the picker.
	if _, err := f.svc.CreateProject(ctx, 7, "  acme  "); !errors.Is(err, worklog.ErrProjectExists) {
		t.Errorf("case insensitive duplicate = %v, want ErrProjectExists", err)
	}
	// Another user may still use the name.
	if _, err := f.svc.Register(ctx, worklog.User{ID: 8, ChatID: 8}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, err := f.svc.CreateProject(ctx, 8, "Acme"); err != nil {
		t.Errorf("same name for another user: %v", err)
	}
}

func TestCreateProjectRejectsBadNames(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for _, name := range []string{"", "   ", "line\nbreak", string(make([]rune, 41))} {
		if _, err := f.svc.CreateProject(ctx, 7, name); !errors.Is(err, worklog.ErrInvalidName) {
			t.Errorf("CreateProject(%q) = %v, want ErrInvalidName", name, err)
		}
	}
}

func TestRenameProject(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	p := f.project(t, "Acme", "25")

	renamed, err := f.svc.RenameProject(ctx, 7, p.ID, "  Acme Corp  ")
	if err != nil {
		t.Fatalf("RenameProject: %v", err)
	}
	if renamed.Name != "Acme Corp" {
		t.Errorf("name = %q, want the trimmed Acme Corp", renamed.Name)
	}
	// The rate must survive a rename.
	if renamed.Rate.HourlyCents != 2500 {
		t.Errorf("rate = %d, want it kept at 2500", renamed.Rate.HourlyCents)
	}
}

// Deleting a project takes its shifts with it and leaves the others alone.
func TestDeleteProjectRemovesItsShiftsOnly(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	doomed := f.project(t, "Doomed", "25")
	keeper := f.project(t, "Keeper", "40")

	for _, p := range []worklog.Project{doomed, keeper} {
		if _, err := f.svc.Start(ctx, 7, p.ID); err != nil {
			t.Fatalf("Start on %s: %v", p.Name, err)
		}
		f.now = f.now.Add(time.Hour)
		if _, err := f.svc.Stop(ctx, 7); err != nil {
			t.Fatalf("Stop on %s: %v", p.Name, err)
		}
	}

	count, err := f.svc.ProjectShiftCount(ctx, 7, doomed.ID)
	if err != nil {
		t.Fatalf("ProjectShiftCount: %v", err)
	}
	if count != 1 {
		t.Errorf("shift count = %d, want 1", count)
	}

	if err := f.svc.DeleteProject(ctx, 7, doomed.ID); err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}

	projects, err := f.svc.Projects(ctx, 7)
	if err != nil {
		t.Fatalf("Projects: %v", err)
	}
	if len(projects) != 1 || projects[0].Name != "Keeper" {
		t.Fatalf("remaining projects = %+v, want only Keeper", projects)
	}

	summary, err := f.svc.Summarize(ctx, f.reload(t))
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	// Only the keeper's hour and pay remain.
	if summary.Today.Shifts != 1 || summary.Today.EarningsCents != 4000 {
		t.Errorf("totals after delete = %+v, want 1 shift paying 4000", summary.Today)
	}
}

func TestDeleteProjectRejectsAnotherUsersProject(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	p := f.project(t, "Acme", "25")

	if _, err := f.svc.Register(ctx, worklog.User{ID: 8, ChatID: 8}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := f.svc.DeleteProject(ctx, 8, p.ID); !errors.Is(err, worklog.ErrProjectNotFound) {
		t.Errorf("cross user delete = %v, want ErrProjectNotFound", err)
	}
	if projects, _ := f.svc.Projects(ctx, 7); len(projects) != 1 {
		t.Error("the project was deleted by the wrong user")
	}
}

// Reset clears one account completely and leaves other accounts untouched.
func TestResetClearsEverythingForOneUser(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	p := f.project(t, "Acme", "25")

	if _, err := f.svc.Start(ctx, 7, p.ID); err != nil {
		t.Fatalf("Start: %v", err)
	}
	f.now = f.now.Add(3 * time.Hour)
	if _, err := f.svc.Stop(ctx, 7); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if _, err := f.svc.SetCurrency(ctx, 7, "EUR"); err != nil {
		t.Fatalf("SetCurrency: %v", err)
	}

	// A second account whose data must survive.
	if _, err := f.svc.Register(ctx, worklog.User{ID: 8, ChatID: 8}); err != nil {
		t.Fatalf("register other: %v", err)
	}
	other, err := f.svc.CreateProject(ctx, 8, "Theirs")
	if err != nil {
		t.Fatalf("create their project: %v", err)
	}
	if _, err := f.svc.SetProjectHourlyRate(ctx, 8, other.ID, "10"); err != nil {
		t.Fatalf("set their rate: %v", err)
	}

	if err := f.svc.Reset(ctx, 7); err != nil {
		t.Fatalf("Reset: %v", err)
	}

	projects, err := f.svc.Projects(ctx, 7)
	if err != nil {
		t.Fatalf("Projects: %v", err)
	}
	if len(projects) != 0 {
		t.Errorf("projects after reset = %d, want 0", len(projects))
	}

	user := f.reload(t)
	if user.Currency != "USD" || user.Timezone != "UTC" {
		t.Errorf("settings after reset = %s/%s, want the USD/UTC defaults",
			user.Currency, user.Timezone)
	}
	summary, err := f.svc.Summarize(ctx, user)
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if summary.Month.Shifts != 0 || summary.Month.EarningsCents != 0 {
		t.Errorf("history survived the reset: %+v", summary.Month)
	}

	// The other account is intact.
	theirProjects, err := f.svc.Projects(ctx, 8)
	if err != nil {
		t.Fatalf("their Projects: %v", err)
	}
	if len(theirProjects) != 1 {
		t.Errorf("the reset removed another user's projects: %+v", theirProjects)
	}
}

// A reset while on the clock must also clear the running shift.
func TestResetClearsARunningShift(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	p := f.project(t, "Acme", "25")

	if _, err := f.svc.Start(ctx, 7, p.ID); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := f.svc.Reset(ctx, 7); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	open, err := f.svc.OpenShift(ctx, 7)
	if err != nil {
		t.Fatalf("OpenShift: %v", err)
	}
	if open != nil {
		t.Errorf("a shift survived the reset: %+v", open)
	}
}

func TestSetProjectRateModeKeepsBothAmounts(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	p := f.project(t, "Acme", "25")

	if _, err := f.svc.SetProjectMonthlyRate(ctx, 7, p.ID, "3360"); err != nil {
		t.Fatalf("SetProjectMonthlyRate: %v", err)
	}
	switched, err := f.svc.SetProjectRateMode(ctx, 7, p.ID, worklog.RateModeHourly)
	if err != nil {
		t.Fatalf("SetProjectRateMode: %v", err)
	}
	if switched.Rate.IsMonthly() {
		t.Error("mode did not switch back to hourly")
	}
	if switched.Rate.HourlyCents != 2500 || switched.Rate.MonthlyCents != 336000 {
		t.Errorf("amounts = %d/%d, want both kept",
			switched.Rate.HourlyCents, switched.Rate.MonthlyCents)
	}
	if _, err := f.svc.SetProjectRateMode(ctx, 7, p.ID, "weekly"); err == nil {
		t.Error("expected an error for an unknown pay mode")
	}
}

func TestSetTimezoneRejectsUnknownZone(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	if _, err := f.svc.SetTimezone(ctx, 7, "Middle/Earth"); !errors.Is(err, worklog.ErrUnknownTimezone) {
		t.Errorf("error = %v, want ErrUnknownTimezone", err)
	}
	if _, err := f.svc.SetTimezone(ctx, 7, "America/New_York"); err != nil {
		t.Errorf("valid timezone rejected: %v", err)
	}
}

// Setting a value must clear the pending prompt so the next message is not
// swallowed as another answer.
func TestSettingAValueClearsPendingState(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	p := f.project(t, "Acme", "")

	for _, step := range []struct {
		name string
		set  func() error
	}{
		{"hourly rate", func() error {
			_, err := f.svc.SetProjectHourlyRate(ctx, 7, p.ID, "30")
			return err
		}},
		{"monthly rate", func() error {
			_, err := f.svc.SetProjectMonthlyRate(ctx, 7, p.ID, "3360")
			return err
		}},
		{"currency", func() error {
			_, err := f.svc.SetCurrency(ctx, 7, "EUR")
			return err
		}},
		{"timezone", func() error {
			_, err := f.svc.SetTimezone(ctx, 7, "UTC")
			return err
		}},
		{"rename", func() error {
			_, err := f.svc.RenameProject(ctx, 7, p.ID, "Renamed")
			return err
		}},
	} {
		if err := f.svc.SetPending(ctx, 7, "something"); err != nil {
			t.Fatalf("SetPending: %v", err)
		}
		if err := step.set(); err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		if got := f.reload(t).PendingAction; got != "" {
			t.Errorf("after setting the %s, pending = %q, want empty", step.name, got)
		}
	}
}
