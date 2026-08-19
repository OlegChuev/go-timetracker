package worklog

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/oleg/go-timetracker/internal/money"
)

// Domain errors surfaced to the bot layer as user facing messages.
var (
	ErrShiftAlreadyOpen = errors.New("a shift is already running")
	ErrNoOpenShift      = errors.New("no shift is running")
	ErrRateNotSet       = errors.New("hourly rate is not set")
	ErrUnknownTimezone  = errors.New("unknown timezone")
)

// Repository is the persistence contract the service needs.
type Repository interface {
	EnsureUser(ctx context.Context, u User) (User, error)
	GetUser(ctx context.Context, userID int64) (User, error)
	UpdateSettings(ctx context.Context, userID int64, s SettingsPatch) error
	ResetUser(ctx context.Context, userID int64) error

	CreateProject(ctx context.Context, userID int64, name string) (Project, error)
	ListProjects(ctx context.Context, userID int64) ([]Project, error)
	GetProject(ctx context.Context, userID, projectID int64) (Project, error)
	UpdateProjectRate(ctx context.Context, userID, projectID int64, rate Rate) error
	RenameProject(ctx context.Context, userID, projectID int64, name string) error
	DeleteProject(ctx context.Context, userID, projectID int64) error

	OpenShift(ctx context.Context, userID, projectID int64, startedAt time.Time, workDate time.Time, tz string) (Shift, error)
	CloseShift(ctx context.Context, userID int64, endedAt time.Time, rate Rate, currency string) (Shift, error)
	FindOpenShift(ctx context.Context, userID int64) (*Shift, error)
	TotalsBetween(ctx context.Context, userID int64, from, to time.Time) (Totals, error)
	TotalsByProject(ctx context.Context, userID int64, from, to time.Time) ([]ProjectTotals, error)
	CountShifts(ctx context.Context, userID, projectID int64) (int, error)
}

// SettingsPatch names the settings to write. Nil fields are left untouched,
// which keeps a change to one setting from clobbering the others.
type SettingsPatch struct {
	Currency      *string
	Timezone      *string
	PendingAction *string
}

// Clock supplies the current time, replaced in tests.
type Clock func() time.Time

// Service implements the tracking use cases on top of a Repository.
type Service struct {
	repo Repository
	now  Clock
}

// NewService wires a service. A nil clock defaults to time.Now.
func NewService(repo Repository, clock Clock) *Service {
	if clock == nil {
		clock = time.Now
	}
	return &Service{repo: repo, now: clock}
}

// Register creates the user on first contact and refreshes their chat details.
func (s *Service) Register(ctx context.Context, u User) (User, error) {
	return s.repo.EnsureUser(ctx, u)
}

// User loads a user by Telegram id.
func (s *Service) User(ctx context.Context, userID int64) (User, error) {
	return s.repo.GetUser(ctx, userID)
}

// SetProjectHourlyRate stores an hourly rate on a project.
func (s *Service) SetProjectHourlyRate(ctx context.Context, userID, projectID int64, raw string) (Project, error) {
	cents, err := parsePositiveAmount(raw)
	if err != nil {
		return Project{}, err
	}
	return s.setProjectRate(ctx, userID, projectID, Rate{Mode: RateModeHourly, HourlyCents: cents})
}

// SetProjectMonthlyRate stores a monthly salary on a project. The hourly
// figure is derived from it rather than stored separately.
func (s *Service) SetProjectMonthlyRate(ctx context.Context, userID, projectID int64, raw string) (Project, error) {
	cents, err := parsePositiveAmount(raw)
	if err != nil {
		return Project{}, err
	}
	return s.setProjectRate(ctx, userID, projectID, Rate{Mode: RateModeMonthly, MonthlyCents: cents})
}

// SetProjectRateMode switches a project between hourly and monthly pay,
// keeping both amounts so neither has to be retyped.
func (s *Service) SetProjectRateMode(ctx context.Context, userID, projectID int64, mode string) (Project, error) {
	if mode != RateModeHourly && mode != RateModeMonthly {
		return Project{}, fmt.Errorf("unknown pay mode %q", mode)
	}
	project, err := s.repo.GetProject(ctx, userID, projectID)
	if err != nil {
		return Project{}, err
	}
	rate := project.Rate
	rate.Mode = mode
	return s.setProjectRate(ctx, userID, projectID, rate)
}

// setProjectRate merges a new rate into the project, preserving the amount for
// the mode that is not being set, and clears any pending prompt.
func (s *Service) setProjectRate(ctx context.Context, userID, projectID int64, rate Rate) (Project, error) {
	project, err := s.repo.GetProject(ctx, userID, projectID)
	if err != nil {
		return Project{}, err
	}
	merged := project.Rate
	merged.Mode = rate.Mode
	if rate.HourlyCents > 0 {
		merged.HourlyCents = rate.HourlyCents
	}
	if rate.MonthlyCents > 0 {
		merged.MonthlyCents = rate.MonthlyCents
	}
	if err := s.repo.UpdateProjectRate(ctx, userID, projectID, merged); err != nil {
		return Project{}, err
	}
	if err := s.SetPending(ctx, userID, ""); err != nil {
		return Project{}, err
	}
	project.Rate = merged
	return project, nil
}

// CreateProject adds a project with no rate yet.
func (s *Service) CreateProject(ctx context.Context, userID int64, raw string) (Project, error) {
	name, err := CleanProjectName(raw)
	if err != nil {
		return Project{}, err
	}
	project, err := s.repo.CreateProject(ctx, userID, name)
	if err != nil {
		return Project{}, err
	}
	if err := s.SetPending(ctx, userID, ""); err != nil {
		return Project{}, err
	}
	return project, nil
}

// RenameProject changes a project's name.
func (s *Service) RenameProject(ctx context.Context, userID, projectID int64, raw string) (Project, error) {
	name, err := CleanProjectName(raw)
	if err != nil {
		return Project{}, err
	}
	if err := s.repo.RenameProject(ctx, userID, projectID, name); err != nil {
		return Project{}, err
	}
	if err := s.SetPending(ctx, userID, ""); err != nil {
		return Project{}, err
	}
	return s.repo.GetProject(ctx, userID, projectID)
}

// DeleteProject removes a project and every shift recorded against it.
func (s *Service) DeleteProject(ctx context.Context, userID, projectID int64) error {
	return s.repo.DeleteProject(ctx, userID, projectID)
}

// Projects lists a user's projects.
func (s *Service) Projects(ctx context.Context, userID int64) ([]Project, error) {
	return s.repo.ListProjects(ctx, userID)
}

// Project loads one project belonging to the user.
func (s *Service) Project(ctx context.Context, userID, projectID int64) (Project, error) {
	return s.repo.GetProject(ctx, userID, projectID)
}

// ProjectShiftCount is how many shifts a project would take with it if it were
// deleted, which the confirmation prompt shows before anything is removed.
func (s *Service) ProjectShiftCount(ctx context.Context, userID, projectID int64) (int, error) {
	return s.repo.CountShifts(ctx, userID, projectID)
}

// Reset deletes every project, shift and setting a user has, returning them to
// a clean slate. The user row itself survives so the chat keeps working.
func (s *Service) Reset(ctx context.Context, userID int64) error {
	return s.repo.ResetUser(ctx, userID)
}

// parsePositiveAmount reads a money amount that must be greater than zero.
func parsePositiveAmount(raw string) (int64, error) {
	cents, err := money.ParseCents(raw)
	if err != nil {
		return 0, err
	}
	if cents == 0 {
		return 0, fmt.Errorf("%w: must be greater than zero", money.ErrInvalidAmount)
	}
	return cents, nil
}

// SetCurrency stores a short currency label such as "USD" or "€".
func (s *Service) SetCurrency(ctx context.Context, userID int64, raw string) (string, error) {
	code := []rune(raw)
	if len(code) == 0 || len(code) > 8 {
		return "", errors.New("currency must be 1 to 8 characters")
	}
	value := string(code)
	cleared := ""
	if err := s.repo.UpdateSettings(ctx, userID, SettingsPatch{Currency: &value, PendingAction: &cleared}); err != nil {
		return "", err
	}
	return value, nil
}

// SetTimezone validates an IANA name against the tz database and stores it.
func (s *Service) SetTimezone(ctx context.Context, userID int64, raw string) (string, error) {
	if _, err := time.LoadLocation(raw); err != nil {
		return "", fmt.Errorf("%w: %s", ErrUnknownTimezone, raw)
	}
	cleared := ""
	if err := s.repo.UpdateSettings(ctx, userID, SettingsPatch{Timezone: &raw, PendingAction: &cleared}); err != nil {
		return "", err
	}
	return raw, nil
}

// SetPending records which text input the bot is waiting for next.
func (s *Service) SetPending(ctx context.Context, userID int64, action string) error {
	return s.repo.UpdateSettings(ctx, userID, SettingsPatch{PendingAction: &action})
}

// Start opens a shift on a project. It refuses when that project has no rate,
// so every shift closes with a meaningful payout.
func (s *Service) Start(ctx context.Context, userID, projectID int64) (Shift, error) {
	user, err := s.repo.GetUser(ctx, userID)
	if err != nil {
		return Shift{}, err
	}
	project, err := s.repo.GetProject(ctx, userID, projectID)
	if err != nil {
		return Shift{}, err
	}
	if !project.Rate.IsSet() {
		return Shift{}, ErrRateNotSet
	}

	now := s.now().UTC()
	loc := user.Location()
	workDate, _ := DayRange(now, loc)
	shift, err := s.repo.OpenShift(ctx, userID, projectID, now, workDate, user.Timezone)
	if err != nil {
		return Shift{}, err
	}
	shift.ProjectName = project.Name
	return shift, nil
}

// Stop closes the running shift, snapshotting the rate its project carries
// right now so later rate changes never rewrite past earnings.
func (s *Service) Stop(ctx context.Context, userID int64) (Shift, error) {
	user, err := s.repo.GetUser(ctx, userID)
	if err != nil {
		return Shift{}, err
	}
	open, err := s.repo.FindOpenShift(ctx, userID)
	if err != nil {
		return Shift{}, err
	}
	if open == nil {
		return Shift{}, ErrNoOpenShift
	}

	// A project deleted mid-shift takes its shifts with it, so this only fails
	// if the row vanished between the two queries.
	project, err := s.repo.GetProject(ctx, userID, open.ProjectID)
	if err != nil {
		return Shift{}, err
	}
	shift, err := s.repo.CloseShift(ctx, userID, s.now().UTC(), project.Rate, user.Currency)
	if err != nil {
		return Shift{}, err
	}
	shift.ProjectName = project.Name
	return shift, nil
}

// OpenShift returns the running shift, or nil when the user is off the clock.
func (s *Service) OpenShift(ctx context.Context, userID int64) (*Shift, error) {
	return s.repo.FindOpenShift(ctx, userID)
}

// Summarize reports today, this week and this month for a user, and includes
// the elapsed time of a running shift when there is one.
func (s *Service) Summarize(ctx context.Context, user User) (Summary, error) {
	now := s.now().UTC()
	loc := user.Location()

	var (
		summary Summary
		err     error
	)
	dayFrom, dayTo := DayRange(now, loc)
	weekFrom, weekTo := WeekRange(now, loc)
	monthFrom, monthTo := MonthRange(now, loc)

	periods := []struct {
		from, to time.Time
		into     *Totals
	}{
		{dayFrom, dayTo, &summary.Today},
		{weekFrom, weekTo, &summary.Week},
		{monthFrom, monthTo, &summary.Month},
	}
	for _, p := range periods {
		totals, err := s.repo.TotalsBetween(ctx, user.ID, p.from, p.to)
		if err != nil {
			return Summary{}, err
		}
		*p.into = totals
	}

	if summary.ByProject, err = s.repo.TotalsByProject(ctx, user.ID, monthFrom, monthTo); err != nil {
		return Summary{}, err
	}

	open, err := s.repo.FindOpenShift(ctx, user.ID)
	if err != nil {
		return Summary{}, err
	}
	if open != nil {
		summary.Open = open
		summary.OpenFor = now.Sub(open.StartedAt)
	}
	return summary, nil
}

// Earnings is the payout for a duration at the given hourly rate.
func Earnings(rateCents, seconds int64) int64 { return money.Earnings(rateCents, seconds) }
