package bot

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/oleg/go-timetracker/internal/storage"
	"github.com/oleg/go-timetracker/internal/telegram"
	"github.com/oleg/go-timetracker/internal/worklog"
)

// fakeAPI stands in for the Bot API and records what the bot sends.
type fakeAPI struct {
	mu   sync.Mutex
	sent []telegram.SendMessageRequest
	srv  *httptest.Server
}

func newFakeAPI(t *testing.T) *fakeAPI {
	t.Helper()
	f := &fakeAPI{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.HasSuffix(r.URL.Path, "/sendMessage") {
			var req telegram.SendMessageRequest
			if err := json.Unmarshal(body, &req); err != nil {
				t.Errorf("sendMessage payload: %v", err)
			}
			f.mu.Lock()
			f.sent = append(f.sent, req)
			f.mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"ok":true,"result":{"message_id":1}}`)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// lastText returns the most recent outgoing message body.
func (f *fakeAPI) lastText(t *testing.T) string {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sent) == 0 {
		t.Fatal("bot sent no messages")
	}
	return f.sent[len(f.sent)-1].Text
}

func (f *fakeAPI) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

// newStore opens a throwaway SQLite database with the real schema, so the
// handlers run against the same storage that ships.
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

// harness wires a bot against the fake API and real storage, with a clock the
// test advances.
type harness struct {
	bot   *Bot
	api   *fakeAPI
	store *storage.Store
	svc   *worklog.Service
	now   time.Time
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	api := newFakeAPI(t)
	store := newStore(t)
	h := &harness{api: api, store: store, now: time.Date(2026, 8, 19, 8, 0, 0, 0, time.UTC)}

	client := telegram.New("test-token", slog.New(slog.DiscardHandler), telegram.WithBaseURL(api.srv.URL))
	h.svc = worklog.NewService(store, func() time.Time { return h.now })
	h.bot = New(client, h.svc, slog.New(slog.DiscardHandler))
	h.bot.now = func() time.Time { return h.now }
	return h
}

// user returns the stored row for the test account.
func (h *harness) user(t *testing.T) worklog.User {
	t.Helper()
	u, err := h.svc.User(context.Background(), 42)
	if err != nil {
		t.Fatalf("load user: %v", err)
	}
	return u
}

// projects returns the test account's projects.
func (h *harness) projects(t *testing.T) []worklog.Project {
	t.Helper()
	p, err := h.svc.Projects(context.Background(), 42)
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}
	return p
}

// project returns the one project the test created, failing if there are more.
func (h *harness) project(t *testing.T) worklog.Project {
	t.Helper()
	projects := h.projects(t)
	if len(projects) != 1 {
		t.Fatalf("expected exactly one project, found %d", len(projects))
	}
	return projects[0]
}

// projectNamed looks a project up by name.
func (h *harness) projectNamed(t *testing.T, name string) worklog.Project {
	t.Helper()
	for _, p := range h.projects(t) {
		if p.Name == name {
			return p
		}
	}
	t.Fatalf("no project named %q", name)
	return worklog.Project{}
}

// newProject creates a project with an hourly rate, bypassing the chat flow so
// tests that are not about setup stay short.
func (h *harness) newProject(t *testing.T, name, rate string) worklog.Project {
	t.Helper()
	ctx := context.Background()
	if _, err := h.svc.Register(ctx, worklog.User{ID: 42, ChatID: 42, FirstName: "Oleg"}); err != nil {
		t.Fatalf("register: %v", err)
	}
	p, err := h.svc.CreateProject(ctx, 42, name)
	if err != nil {
		t.Fatalf("create project %s: %v", name, err)
	}
	p, err = h.svc.SetProjectHourlyRate(ctx, 42, p.ID, rate)
	if err != nil {
		t.Fatalf("set rate on %s: %v", name, err)
	}
	return p
}

// shiftCount is how many shifts the test account has, running one included.
func (h *harness) shiftCount(t *testing.T) int {
	t.Helper()
	total := 0
	for _, p := range h.projects(t) {
		n, err := h.svc.ProjectShiftCount(context.Background(), 42, p.ID)
		if err != nil {
			t.Fatalf("count shifts: %v", err)
		}
		total += n
	}
	return total
}

// send feeds a text message from user 42 through the update dispatcher.
func (h *harness) send(text string) {
	h.bot.handleMessage(context.Background(), &telegram.Message{
		MessageID: 1,
		From:      &telegram.User{ID: 42, FirstName: "Oleg", Username: "oleg"},
		Chat:      &telegram.Chat{ID: 42, Type: "private"},
		Text:      text,
	})
}

// tap simulates pressing an inline button.
func (h *harness) tap(data string) {
	h.bot.handleCallback(context.Background(), &telegram.CallbackQuery{
		ID:      "cb1",
		From:    &telegram.User{ID: 42, FirstName: "Oleg"},
		Message: &telegram.Message{MessageID: 1, Chat: &telegram.Chat{ID: 42}},
		Data:    data,
	})
}

func mustContain(t *testing.T, got string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Errorf("message missing %q:\n%s", w, got)
		}
	}
}

// The full happy path: add a project, set its rate, clock in, clock out.
func TestFullShiftFlow(t *testing.T) {
	h := newHarness(t)

	h.send("/start")
	mustContain(t, h.api.lastText(t), "Hi Oleg", "newproject")

	h.send("/newproject Acme")
	mustContain(t, h.api.lastText(t), "Acme", "set what it pays")

	project := h.project(t)
	h.tap(withID(cbProjectHourly, project.ID))
	mustContain(t, h.api.lastText(t), "hourly rate for this project")

	h.send("25.50")
	mustContain(t, h.api.lastText(t), "Acme", "25.50 USD")

	// With a single project there is nothing to choose, so work starts at once.
	h.send(btnStart)
	mustContain(t, h.api.lastText(t), "Shift started", "Acme", "08:00")

	h.now = h.now.Add(4 * time.Hour)
	h.send(btnStop)
	mustContain(t, h.api.lastText(t), "Shift finished", "Acme", "4h 00m", "102.00 USD")

	h.send(btnToday)
	mustContain(t, h.api.lastText(t), "4.00 h", "102.00 USD")

	h.send(btnStats)
	mustContain(t, h.api.lastText(t), "Today", "This week", "This month", "by project", "Acme")
}

// With several projects the bot asks which one, and only starts once told.
func TestStartAsksWhichProject(t *testing.T) {
	h := newHarness(t)
	acme := h.newProject(t, "Acme", "25")
	globex := h.newProject(t, "Globex", "40")

	h.send(btnStart)
	last := h.api.lastText(t)
	mustContain(t, last, "Which project")
	if open, _ := h.svc.OpenShift(context.Background(), 42); open != nil {
		t.Fatal("a shift started before a project was chosen")
	}
	// Both projects must be offered.
	keyboard := projectPickerKeyboard(h.projects(t))
	for _, p := range []worklog.Project{acme, globex} {
		if !hasCallback(keyboard, withID(cbStartProject, p.ID)) {
			t.Errorf("project %s was not offered", p.Name)
		}
	}

	h.tap(withID(cbStartProject, globex.ID))
	mustContain(t, h.api.lastText(t), "Shift started", "Globex", "40.00 USD")

	h.now = h.now.Add(2 * time.Hour)
	h.send(btnStop)
	mustContain(t, h.api.lastText(t), "Globex", "80.00 USD")
}

// One shift at a time, whichever project the second attempt names.
func TestCannotStartASecondShift(t *testing.T) {
	h := newHarness(t)
	acme := h.newProject(t, "Acme", "25")
	globex := h.newProject(t, "Globex", "40")

	h.tap(withID(cbStartProject, acme.ID))
	mustContain(t, h.api.lastText(t), "Shift started", "Acme")

	h.now = h.now.Add(30 * time.Minute)

	// Tapping start again reports the running shift instead of offering a pick.
	h.send(btnStart)
	mustContain(t, h.api.lastText(t), "already on the clock", "Acme", "30m")

	// And naming another project directly is refused too.
	h.tap(withID(cbStartProject, globex.ID))
	mustContain(t, h.api.lastText(t), "already on the clock", "Acme")

	if count := h.shiftCount(t); count != 1 {
		t.Errorf("shift count = %d, want 1", count)
	}
}

func TestStartWithNoProjects(t *testing.T) {
	h := newHarness(t)
	h.send("/start")

	h.send(btnStart)
	mustContain(t, h.api.lastText(t), "no projects yet", "newproject")

	if open, _ := h.svc.OpenShift(context.Background(), 42); open != nil {
		t.Error("a shift started with no projects")
	}
}

func TestStartOnAProjectWithNoRate(t *testing.T) {
	h := newHarness(t)
	h.send("/newproject Acme")
	project := h.project(t)

	h.send(btnStart)
	mustContain(t, h.api.lastText(t), "Acme", "no rate yet")

	if open, _ := h.svc.OpenShift(context.Background(), 42); open != nil {
		t.Error("a shift started on a project with no rate")
	}

	// Setting the rate from that very prompt then lets work begin.
	h.tap(withID(cbProjectHourly, project.ID))
	h.send("25")
	h.send(btnStart)
	mustContain(t, h.api.lastText(t), "Shift started", "Acme")
}

func TestStopWithoutStart(t *testing.T) {
	h := newHarness(t)
	h.newProject(t, "Acme", "25")
	h.send(btnStop)
	mustContain(t, h.api.lastText(t), "not on the clock")
}

// A monthly salary pays the annualised slice of the month, not a rounded hour.
func TestMonthlyRateProject(t *testing.T) {
	h := newHarness(t)
	h.send("/newproject Day job")
	project := h.project(t)

	h.tap(withID(cbProjectMonthly, project.ID))
	mustContain(t, h.api.lastText(t), "monthly salary for this project")

	h.send("3360")
	mustContain(t, h.api.lastText(t), "3 360.00 USD", "19.38 USD", "173.33")

	h.send(btnStart)
	h.now = h.now.Add(8 * time.Hour)
	h.send(btnStop)
	// 8h of a 3360.00 month is 155.08, not the 155.04 a rounded 19.38/h gives.
	mustContain(t, h.api.lastText(t), "Shift finished", "Day job", "155.08 USD")
}

// Each project keeps its own rate and its own mode.
func TestProjectsHaveIndependentRates(t *testing.T) {
	h := newHarness(t)
	hourly := h.newProject(t, "Acme", "25")
	h.send("/newproject Day job")
	salaried := h.projectNamed(t, "Day job")
	h.tap(withID(cbProjectMonthly, salaried.ID))
	h.send("3360")

	h.tap(withID(cbStartProject, hourly.ID))
	h.now = h.now.Add(2 * time.Hour)
	h.send(btnStop)
	mustContain(t, h.api.lastText(t), "Acme", "50.00 USD")

	h.tap(withID(cbStartProject, salaried.ID))
	h.now = h.now.Add(8 * time.Hour)
	h.send(btnStop)
	mustContain(t, h.api.lastText(t), "Day job", "155.08 USD")

	h.send(btnStats)
	last := h.api.lastText(t)
	mustContain(t, last, "by project", "Acme", "Day job", "205.08 USD")
}

// Switching a project's mode keeps both amounts so neither is retyped.
func TestSwitchingProjectRateMode(t *testing.T) {
	h := newHarness(t)
	project := h.newProject(t, "Acme", "25")

	h.tap(withID(cbProjectMonthly, project.ID))
	h.send("3360")
	mustContain(t, h.api.lastText(t), "3 360.00 USD/month")

	h.tap(withID(cbProjectMode, project.ID))
	mustContain(t, h.api.lastText(t), "25.00 USD/h")

	updated := h.projectNamed(t, "Acme")
	if updated.Rate.IsMonthly() {
		t.Error("mode did not switch back to hourly")
	}
	if updated.Rate.HourlyCents != 2500 || updated.Rate.MonthlyCents != 336000 {
		t.Errorf("amounts = %d/%d, want both kept",
			updated.Rate.HourlyCents, updated.Rate.MonthlyCents)
	}
}

func TestRenameProject(t *testing.T) {
	h := newHarness(t)
	project := h.newProject(t, "Acme", "25")

	h.tap(withID(cbProjectRename, project.ID))
	mustContain(t, h.api.lastText(t), "new name")

	h.send("Acme Corp")
	mustContain(t, h.api.lastText(t), "Renamed to", "Acme Corp")

	if got := h.project(t).Name; got != "Acme Corp" {
		t.Errorf("name = %q, want Acme Corp", got)
	}
}

func TestDuplicateProjectNameIsRefused(t *testing.T) {
	h := newHarness(t)
	h.newProject(t, "Acme", "25")

	h.send("/newproject Acme")
	mustContain(t, h.api.lastText(t), "already have a project with that name")

	if len(h.projects(t)) != 1 {
		t.Error("a duplicate project was created")
	}
}

// Deleting a project takes its shifts, and asks first.
func TestDeleteProjectRequiresConfirmation(t *testing.T) {
	h := newHarness(t)
	project := h.newProject(t, "Acme", "25")

	h.send(btnStart)
	h.now = h.now.Add(3 * time.Hour)
	h.send(btnStop)

	h.tap(withID(cbProjectDelete, project.ID))
	mustContain(t, h.api.lastText(t), "Delete Acme", "1 shift", "cannot be undone")
	if len(h.projects(t)) != 1 {
		t.Fatal("the project was deleted before confirmation")
	}

	h.tap(withID(cbProjectConfirm, project.ID))
	mustContain(t, h.api.lastText(t), "deleted")
	if len(h.projects(t)) != 0 {
		t.Error("the project survived a confirmed delete")
	}
	if count := h.shiftCount(t); count != 0 {
		t.Errorf("%d shifts survived the project delete", count)
	}
}

// /reset asks before destroying anything, and cancelling leaves data intact.
func TestResetRequiresConfirmation(t *testing.T) {
	h := newHarness(t)
	h.newProject(t, "Acme", "25")
	h.send(btnStart)
	h.now = h.now.Add(2 * time.Hour)
	h.send(btnStop)

	h.send("/reset")
	mustContain(t, h.api.lastText(t), "Reset everything", "1 project", "cannot be undone")
	if len(h.projects(t)) != 1 {
		t.Fatal("data was deleted before confirmation")
	}

	h.tap(cbCancel)
	if len(h.projects(t)) != 1 || h.shiftCount(t) != 1 {
		t.Fatal("cancelling the reset still deleted data")
	}

	h.send("/reset")
	h.tap(cbResetConfirm)
	mustContain(t, h.api.lastText(t), "Everything deleted")

	if len(h.projects(t)) != 0 {
		t.Error("projects survived the reset")
	}
	// Counting per project would be vacuous now the projects are gone, so ask
	// the summary instead: it reads shifts directly.
	user := h.user(t)
	summary, err := h.svc.Summarize(context.Background(), user)
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if summary.Month.Shifts != 0 || summary.Month.EarningsCents != 0 {
		t.Errorf("shifts survived the reset: %+v", summary.Month)
	}
	if user.Currency != "USD" || user.Timezone != "UTC" {
		t.Errorf("settings after reset = %s/%s, want the defaults", user.Currency, user.Timezone)
	}
}

// A reset while on the clock must clear the running shift too.
func TestResetWhileOnTheClock(t *testing.T) {
	h := newHarness(t)
	h.newProject(t, "Acme", "25")
	h.send(btnStart)

	h.send("/reset")
	h.tap(cbResetConfirm)

	if open, _ := h.svc.OpenShift(context.Background(), 42); open != nil {
		t.Error("a shift survived the reset")
	}
	// And the bot is usable again straight away.
	h.send(btnStop)
	mustContain(t, h.api.lastText(t), "not on the clock")
}

// A keyboard button pressed while a prompt is pending must act as a button,
// not be swallowed as the answer to the prompt.
func TestButtonEscapesPendingPrompt(t *testing.T) {
	h := newHarness(t)
	h.newProject(t, "Acme", "25")
	h.tap(cbSetTimezone)

	h.send(btnStart)
	mustContain(t, h.api.lastText(t), "Shift started")

	if got := h.user(t).Timezone; got != "UTC" {
		t.Errorf("timezone = %q, want it unchanged at UTC", got)
	}
}

func TestInvalidSettingValues(t *testing.T) {
	h := newHarness(t)
	project := h.newProject(t, "Acme", "25")

	h.tap(withID(cbProjectHourly, project.ID))
	h.send("abc")
	mustContain(t, h.api.lastText(t), "does not look like an amount")

	h.send("/timezone Middle/Earth")
	mustContain(t, h.api.lastText(t), "do not know that timezone")

	if got := h.projectNamed(t, "Acme").Rate.HourlyCents; got != 2500 {
		t.Errorf("rate = %d, want it unchanged at 2500", got)
	}
	if got := h.user(t).Timezone; got != "UTC" {
		t.Errorf("timezone = %q, want it unchanged", got)
	}
}

// The timezone decides which calendar day a shift lands on.
func TestTimezoneShiftsTheReportingDay(t *testing.T) {
	h := newHarness(t)
	h.newProject(t, "Acme", "10")
	h.send("/timezone Europe/Kyiv")

	h.now = time.Date(2026, 8, 19, 22, 0, 0, 0, time.UTC) // 01:00 on the 20th in Kyiv
	h.send(btnStart)
	h.now = h.now.Add(2 * time.Hour)
	h.send(btnStop)

	summary, err := h.svc.Summarize(context.Background(), h.user(t))
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if summary.Today.Seconds != 2*3600 {
		t.Errorf("today = %d seconds, want the shift on the Kyiv day", summary.Today.Seconds)
	}
}

func TestUnknownTextGetsGuidance(t *testing.T) {
	h := newHarness(t)
	h.send("/start")
	before := h.api.count()

	h.send("hello there")
	if h.api.count() != before+1 {
		t.Fatal("bot did not reply to unknown text")
	}
	mustContain(t, h.api.lastText(t), "/help")
}

// A stale button from a deleted project must be reported, not acted on.
func TestCallbackForAMissingProject(t *testing.T) {
	h := newHarness(t)
	project := h.newProject(t, "Acme", "25")
	h.tap(withID(cbProjectDelete, project.ID))
	h.tap(withID(cbProjectConfirm, project.ID))

	for _, data := range []string{
		withID(cbStartProject, project.ID),
		withID(cbOpenProject, project.ID),
		withID(cbProjectMode, project.ID),
		withID(cbProjectDelete, project.ID),
	} {
		h.tap(data)
		mustContain(t, h.api.lastText(t), "no longer exists")
	}
}

func TestSplitCommand(t *testing.T) {
	cases := []struct{ in, cmd, args string }{
		{"/newproject Acme", "/newproject", "Acme"},
		{"/newproject", "/newproject", ""},
		{"/RESET", "/reset", ""},
		{"/stop@my_bot", "/stop", ""},
		{"  /stop  ", "/stop", ""},
		{"/timezone Europe/Kyiv", "/timezone", "Europe/Kyiv"},
	}
	for _, tc := range cases {
		cmd, args := splitCommand(tc.in)
		if cmd != tc.cmd || args != tc.args {
			t.Errorf("splitCommand(%q) = (%q, %q), want (%q, %q)", tc.in, cmd, args, tc.cmd, tc.args)
		}
	}
}

func TestPendingRoundTrip(t *testing.T) {
	cases := []pending{
		{Action: pendingNewProject},
		{Action: pendingProjectRate, ProjectID: 7},
		{Action: pendingProjectMonthly, ProjectID: 1234567890},
		{Action: pendingCurrency},
	}
	for _, want := range cases {
		if got := parsePending(want.String()); got != want {
			t.Errorf("parsePending(%q) = %+v, want %+v", want.String(), got, want)
		}
	}
	// A malformed id must not resolve to project zero and act on the wrong row.
	if got := parsePending("prate:notanumber"); got.ProjectID != 0 || got.Action != pendingProjectRate {
		t.Errorf("parsePending of a bad id = %+v", got)
	}
}

func TestCallbackID(t *testing.T) {
	if prefix, id, ok := callbackID("prate:7"); !ok || prefix != "prate" || id != 7 {
		t.Errorf("callbackID(\"prate:7\") = %q, %d, %v", prefix, id, ok)
	}
	if _, _, ok := callbackID("proj:new"); ok {
		t.Error("callbackID accepted a non numeric suffix")
	}
	if prefix, _, ok := callbackID("reset"); ok || prefix != "reset" {
		t.Errorf("callbackID of a bare action = %q, %v", prefix, ok)
	}
}

// hasCallback reports whether a keyboard contains a button with this payload.
func hasCallback(markup telegram.InlineKeyboardMarkup, data string) bool {
	for _, row := range markup.InlineKeyboard {
		for _, button := range row {
			if button.CallbackData == data {
				return true
			}
		}
	}
	return false
}
