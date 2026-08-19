package bot

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/oleg/go-timetracker/internal/money"
	"github.com/oleg/go-timetracker/internal/telegram"
	"github.com/oleg/go-timetracker/internal/worklog"
)

// handleMessage resolves the sender, then routes on command, button label or
// an answer to something the bot asked for.
func (b *Bot) handleMessage(ctx context.Context, msg *telegram.Message) {
	if msg.From == nil || msg.Chat == nil || msg.From.IsBot {
		return
	}
	user, err := b.svc.Register(ctx, worklog.User{
		ID:        msg.From.ID,
		ChatID:    msg.Chat.ID,
		Username:  msg.From.Username,
		FirstName: msg.From.FirstName,
	})
	if err != nil {
		b.log.Error("register user failed", "user_id", msg.From.ID, "error", err)
		b.send(ctx, msg.Chat.ID, "⚠️ Something went wrong on my side. Please try again.", nil)
		return
	}

	text := strings.TrimSpace(msg.Text)
	switch {
	case text == "":
		return
	case strings.HasPrefix(text, "/"):
		cmd, args := splitCommand(text)
		b.handleCommand(ctx, user, cmd, args)
	case b.handleButton(ctx, user, text):
		// A button label always wins, so the user can escape a prompt.
	case user.PendingAction != "":
		b.consumePending(ctx, user, text)
	default:
		b.send(ctx, user.ChatID,
			"I did not catch that. Use the buttons below, or /help to see what I can do.", nil)
	}
}

// handleCommand runs a slash command. Unknown commands fall through to help.
func (b *Bot) handleCommand(ctx context.Context, user worklog.User, cmd, args string) {
	switch cmd {
	case "/start":
		b.greet(ctx, user)
	case "/work", "/begin":
		b.startWork(ctx, user)
	case "/stop", "/end":
		b.stopWork(ctx, user)
	case "/today":
		b.showToday(ctx, user)
	case "/stats", "/statistics":
		b.showStats(ctx, user)
	case "/projects":
		b.showProjects(ctx, user)
	case "/newproject", "/addproject":
		b.addProject(ctx, user, args)
	case "/settings":
		b.showSettings(ctx, user)
	case "/currency":
		b.applySetting(ctx, user, pending{Action: pendingCurrency}, args)
	case "/timezone", "/tz":
		b.applySetting(ctx, user, pending{Action: pendingTimezone}, args)
	case "/reset":
		b.askReset(ctx, user)
	case "/help":
		b.send(ctx, user.ChatID, helpText, nil)
	default:
		b.send(ctx, user.ChatID, "Unknown command.\n\n"+helpText, nil)
	}
}

// handleButton reacts to a persistent keyboard label, reporting whether the
// text matched one.
func (b *Bot) handleButton(ctx context.Context, user worklog.User, text string) bool {
	switch text {
	case btnStart:
		b.startWork(ctx, user)
	case btnStop:
		b.stopWork(ctx, user)
	case btnToday:
		b.showToday(ctx, user)
	case btnStats:
		b.showStats(ctx, user)
	case btnProjects:
		b.showProjects(ctx, user)
	case btnSettings:
		b.showSettings(ctx, user)
	case btnHelp:
		b.send(ctx, user.ChatID, helpText, nil)
	default:
		return false
	}
	return true
}

// greet welcomes the user and installs the keyboard.
func (b *Bot) greet(ctx context.Context, user worklog.User) {
	name := user.FirstName
	if name == "" {
		name = "there"
	}
	msg := fmt.Sprintf("👋 Hi %s! I track your working hours and what they earn you.\n\n", esc(name))

	projects, err := b.svc.Projects(ctx, user.ID)
	if err != nil || len(projects) == 0 {
		msg += "Work is tracked per project, and each project has its own rate — " +
			"hourly or monthly.\n\nStart with <code>/newproject Acme</code>, " +
			"then tap ▶️ Start work."
		b.send(ctx, user.ChatID, msg, mainKeyboard())
		return
	}
	msg += fmt.Sprintf("You have <b>%d</b> %s set up. Tap ▶️ Start work whenever you begin.",
		len(projects), pluralProjects(len(projects)))
	b.send(ctx, user.ChatID, msg, mainKeyboard())
}

// startWork opens a shift, asking which project when there is a choice.
func (b *Bot) startWork(ctx context.Context, user worklog.User) {
	// A running shift blocks a second one whatever the project, so say so
	// before asking the user to pick.
	if open, err := b.svc.OpenShift(ctx, user.ID); err == nil && open != nil {
		b.reportAlreadyRunning(ctx, user, open)
		return
	}

	projects, err := b.svc.Projects(ctx, user.ID)
	if err != nil {
		b.log.Error("list projects failed", "user_id", user.ID, "error", err)
		b.send(ctx, user.ChatID, "⚠️ I could not load your projects. Please try again.", nil)
		return
	}
	switch len(projects) {
	case 0:
		b.send(ctx, user.ChatID,
			"📁 You have no projects yet.\n\nEvery shift is tracked against one, and each "+
				"carries its own rate. Send <code>/newproject Acme</code> to add the first.",
			projectPickerKeyboard(nil))
	case 1:
		b.startOn(ctx, user, projects[0].ID)
	default:
		b.send(ctx, user.ChatID, "▶️ <b>Which project?</b>", projectPickerKeyboard(projects))
	}
}

// startOn opens a shift on one project.
func (b *Bot) startOn(ctx context.Context, user worklog.User, projectID int64) {
	shift, err := b.svc.Start(ctx, user.ID, projectID)
	switch {
	case errors.Is(err, worklog.ErrRateNotSet):
		project, loadErr := b.svc.Project(ctx, user.ID, projectID)
		if loadErr != nil {
			b.send(ctx, user.ChatID, "⚠️ That project no longer exists.", nil)
			return
		}
		b.send(ctx, user.ChatID,
			fmt.Sprintf("💵 <b>%s</b> has no rate yet.\n\nSet one before clocking in.",
				esc(project.Name)),
			projectCardKeyboard(project))
		return
	case errors.Is(err, worklog.ErrProjectNotFound):
		b.send(ctx, user.ChatID, "⚠️ That project no longer exists.", nil)
		return
	case errors.Is(err, worklog.ErrShiftAlreadyOpen):
		open, _ := b.svc.OpenShift(ctx, user.ID)
		b.reportAlreadyRunning(ctx, user, open)
		return
	case err != nil:
		b.log.Error("start shift failed", "user_id", user.ID, "error", err)
		b.send(ctx, user.ChatID, "⚠️ I could not start the shift. Please try again.", nil)
		return
	}

	project, err := b.svc.Project(ctx, user.ID, projectID)
	rate := "your rate"
	if err == nil {
		rate = describeRate(project.Rate, user.Currency)
	}
	b.send(ctx, user.ChatID, fmt.Sprintf(
		"▶️ <b>Shift started</b> — %s\n\n"+
			"🕒 Since <b>%s</b>\n"+
			"💵 Rate: %s\n\n"+
			"Tap ⏹ Stop work when you are done.",
		esc(shift.ProjectName),
		shift.StartedAt.In(user.Location()).Format(timeLayout),
		rate), nil)
}

// reportAlreadyRunning tells the user about the shift that is already open.
func (b *Bot) reportAlreadyRunning(ctx context.Context, user worklog.User, open *worklog.Shift) {
	if open == nil {
		b.send(ctx, user.ChatID, "⏳ A shift is already running.", nil)
		return
	}
	elapsed := b.now().UTC().Sub(open.StartedAt)
	b.send(ctx, user.ChatID, fmt.Sprintf(
		"⏳ You are already on the clock for <b>%s</b>, since <b>%s</b> — %s so far.\n\n"+
			"Stop it before starting another.",
		esc(open.ProjectName),
		open.StartedAt.In(user.Location()).Format(timeLayout),
		worklog.FormatDuration(elapsed)), nil)
}

// stopWork closes the running shift and reports the payout for it and the day.
func (b *Bot) stopWork(ctx context.Context, user worklog.User) {
	shift, err := b.svc.Stop(ctx, user.ID)
	switch {
	case errors.Is(err, worklog.ErrNoOpenShift):
		b.send(ctx, user.ChatID,
			"🔴 You are not on the clock right now.\n\nTap ▶️ Start work to begin a shift.", nil)
		return
	case err != nil:
		b.log.Error("stop shift failed", "user_id", user.ID, "error", err)
		b.send(ctx, user.ChatID, "⚠️ I could not stop the shift. Please try again.", nil)
		return
	}

	summary, err := b.svc.Summarize(ctx, user)
	if err != nil {
		// The shift is safely closed; only the daily total is missing.
		b.log.Error("summary after stop failed", "user_id", user.ID, "error", err)
		b.send(ctx, user.ChatID, fmt.Sprintf(
			"⏹ <b>Shift finished</b> — %s, earned <b>%s</b>.",
			worklog.FormatDuration(time.Duration(shift.DurationSeconds)*time.Second),
			esc(money.Format(shift.EarningsCents, shift.Currency))), nil)
		return
	}
	b.send(ctx, user.ChatID, renderShiftClosed(shift, summary.Today, user), nil)
}

// showToday renders the single day view.
func (b *Bot) showToday(ctx context.Context, user worklog.User) {
	summary, err := b.svc.Summarize(ctx, user)
	if err != nil {
		b.log.Error("today summary failed", "user_id", user.ID, "error", err)
		b.send(ctx, user.ChatID, "⚠️ I could not load your totals. Please try again.", nil)
		return
	}
	b.send(ctx, user.ChatID, renderToday(user, summary, b.now().UTC()), nil)
}

// showStats renders day, week and month totals.
func (b *Bot) showStats(ctx context.Context, user worklog.User) {
	summary, err := b.svc.Summarize(ctx, user)
	if err != nil {
		b.log.Error("stats summary failed", "user_id", user.ID, "error", err)
		b.send(ctx, user.ChatID, "⚠️ I could not load your statistics. Please try again.", nil)
		return
	}
	b.send(ctx, user.ChatID, renderStats(user, summary, b.now().UTC()), nil)
}

// showProjects lists the projects with their rates.
func (b *Bot) showProjects(ctx context.Context, user worklog.User) {
	projects, err := b.svc.Projects(ctx, user.ID)
	if err != nil {
		b.log.Error("list projects failed", "user_id", user.ID, "error", err)
		b.send(ctx, user.ChatID, "⚠️ I could not load your projects. Please try again.", nil)
		return
	}
	b.send(ctx, user.ChatID, renderProjectList(user, projects), projectListKeyboard(projects))
}

// showProjectCard opens one project for editing.
func (b *Bot) showProjectCard(ctx context.Context, user worklog.User, projectID int64) {
	project, err := b.svc.Project(ctx, user.ID, projectID)
	if err != nil {
		b.send(ctx, user.ChatID, "⚠️ That project no longer exists.", nil)
		return
	}
	b.send(ctx, user.ChatID, renderProjectCard(user, project), projectCardKeyboard(project))
}

// addProject creates a project, or asks for a name when given none.
func (b *Bot) addProject(ctx context.Context, user worklog.User, name string) {
	if name == "" {
		b.promptFor(ctx, user, pending{Action: pendingNewProject})
		return
	}
	project, err := b.svc.CreateProject(ctx, user.ID, name)
	if err != nil {
		b.send(ctx, user.ChatID, projectError(err), cancelKeyboard())
		return
	}
	b.send(ctx, user.ChatID,
		fmt.Sprintf("✅ Project <b>%s</b> created.\n\nNow set what it pays.", esc(project.Name)),
		projectCardKeyboard(project))
}

// showSettings displays the account wide configuration.
func (b *Bot) showSettings(ctx context.Context, user worklog.User) {
	projects, err := b.svc.Projects(ctx, user.ID)
	if err != nil {
		b.log.Error("list projects failed", "user_id", user.ID, "error", err)
		b.send(ctx, user.ChatID, "⚠️ I could not load your settings. Please try again.", nil)
		return
	}
	b.send(ctx, user.ChatID, renderSettings(user, projects), settingsKeyboard())
}

// askReset explains exactly what a reset destroys and asks for confirmation.
func (b *Bot) askReset(ctx context.Context, user worklog.User) {
	summary, err := b.svc.Summarize(ctx, user)
	if err != nil {
		b.log.Error("summary before reset failed", "user_id", user.ID, "error", err)
		b.send(ctx, user.ChatID, "⚠️ I could not check your data. Please try again.", nil)
		return
	}
	projects, err := b.svc.Projects(ctx, user.ID)
	if err != nil {
		b.log.Error("list projects failed", "user_id", user.ID, "error", err)
		b.send(ctx, user.ChatID, "⚠️ I could not check your data. Please try again.", nil)
		return
	}

	rates := "their rates"
	if len(projects) == 1 {
		rates = "its rate"
	}
	msg := "🗑 <b>Reset everything?</b>\n\nThis permanently deletes:\n" +
		fmt.Sprintf("· <b>%d %s</b> and %s\n", len(projects), pluralProjects(len(projects)), rates) +
		"· every shift you have ever recorded\n" +
		"· your currency and timezone\n\n"
	if summary.Month.Shifts > 0 {
		msg += fmt.Sprintf("This month alone that is %s across %s.\n\n",
			worklog.FormatHours(summary.Month.Seconds), pluralShifts(summary.Month.Shifts))
	}
	msg += "<b>This cannot be undone.</b>"
	b.send(ctx, user.ChatID, msg, confirmKeyboard("🗑 Yes, delete everything", cbResetConfirm))
}

// doReset wipes the account after confirmation.
func (b *Bot) doReset(ctx context.Context, user worklog.User) {
	if err := b.svc.Reset(ctx, user.ID); err != nil {
		b.log.Error("reset failed", "user_id", user.ID, "error", err)
		b.send(ctx, user.ChatID, "⚠️ I could not reset your data. Please try again.", nil)
		return
	}
	b.send(ctx, user.ChatID,
		"🗑 <b>Everything deleted.</b>\n\nProjects, rates and history are gone, and "+
			"currency and timezone are back to their defaults.\n\n"+
			"Start again with <code>/newproject Acme</code>.", mainKeyboard())
}

// askDeleteProject shows what deleting a project would take with it.
func (b *Bot) askDeleteProject(ctx context.Context, user worklog.User, projectID int64) {
	project, err := b.svc.Project(ctx, user.ID, projectID)
	if err != nil {
		b.send(ctx, user.ChatID, "⚠️ That project no longer exists.", nil)
		return
	}
	count, err := b.svc.ProjectShiftCount(ctx, user.ID, projectID)
	if err != nil {
		b.log.Error("count shifts failed", "user_id", user.ID, "error", err)
		b.send(ctx, user.ChatID, "⚠️ I could not check that project. Please try again.", nil)
		return
	}
	msg := fmt.Sprintf("🗑 <b>Delete %s?</b>\n\n", esc(project.Name))
	if count > 0 {
		msg += fmt.Sprintf("This also deletes %s recorded against it.\n\n", pluralShifts(count))
	}
	msg += "<b>This cannot be undone.</b>"
	b.send(ctx, user.ChatID, msg,
		confirmKeyboard("🗑 Yes, delete it", withID(cbProjectConfirm, projectID)))
}

// doDeleteProject removes a project after confirmation.
func (b *Bot) doDeleteProject(ctx context.Context, user worklog.User, projectID int64) {
	if err := b.svc.DeleteProject(ctx, user.ID, projectID); err != nil {
		if !errors.Is(err, worklog.ErrProjectNotFound) {
			b.log.Error("delete project failed", "user_id", user.ID, "error", err)
		}
		b.send(ctx, user.ChatID, "⚠️ That project no longer exists.", nil)
		return
	}
	projects, _ := b.svc.Projects(ctx, user.ID)
	b.send(ctx, user.ChatID,
		"🗑 Project deleted.\n\n"+renderProjectList(user, projects),
		projectListKeyboard(projects))
}

// applySetting stores a setting given inline with a command. With no argument
// it asks for the value instead and remembers what it is waiting for.
func (b *Bot) applySetting(ctx context.Context, user worklog.User, p pending, value string) {
	if value == "" {
		b.promptFor(ctx, user, p)
		return
	}
	b.consumeSetting(ctx, user, p, value)
}

// consumePending interprets a plain message as the answer to an earlier prompt.
func (b *Bot) consumePending(ctx context.Context, user worklog.User, value string) {
	b.consumeSetting(ctx, user, parsePending(user.PendingAction), value)
}

// consumeSetting validates and stores one answer, then shows the result.
func (b *Bot) consumeSetting(ctx context.Context, user worklog.User, p pending, value string) {
	switch p.Action {
	case pendingNewProject:
		b.addProject(ctx, user, value)
	case pendingProjectRate:
		b.applyProjectRate(ctx, user, p.ProjectID, value, false)
	case pendingProjectMonthly:
		b.applyProjectRate(ctx, user, p.ProjectID, value, true)
	case pendingProjectRename:
		project, err := b.svc.RenameProject(ctx, user.ID, p.ProjectID, value)
		if err != nil {
			b.send(ctx, user.ChatID, projectError(err), cancelKeyboard())
			return
		}
		b.send(ctx, user.ChatID,
			fmt.Sprintf("✅ Renamed to <b>%s</b>.\n\n", esc(project.Name))+
				renderProjectCard(user, project), projectCardKeyboard(project))
	case pendingCurrency:
		code, err := b.svc.SetCurrency(ctx, user.ID, value)
		if err != nil {
			b.send(ctx, user.ChatID, settingError(p.Action, err), cancelKeyboard())
			return
		}
		user.Currency = code
		b.confirmSetting(ctx, user, fmt.Sprintf("✅ Currency set to <b>%s</b>.", esc(code)))
	case pendingTimezone:
		tz, err := b.svc.SetTimezone(ctx, user.ID, value)
		if err != nil {
			b.send(ctx, user.ChatID, settingError(p.Action, err), cancelKeyboard())
			return
		}
		user.Timezone = tz
		b.confirmSetting(ctx, user, fmt.Sprintf(
			"✅ Timezone set to <b>%s</b>, local time is now %s.",
			esc(tz), time.Now().In(user.Location()).Format(timeLayout)))
	default:
		b.send(ctx, user.ChatID, "I am not waiting for anything. Use the buttons below.", nil)
	}
}

// applyProjectRate stores an hourly or monthly rate on a project.
func (b *Bot) applyProjectRate(ctx context.Context, user worklog.User, projectID int64, value string, monthly bool) {
	var (
		project worklog.Project
		err     error
	)
	if monthly {
		project, err = b.svc.SetProjectMonthlyRate(ctx, user.ID, projectID, value)
	} else {
		project, err = b.svc.SetProjectHourlyRate(ctx, user.ID, projectID, value)
	}
	if err != nil {
		if errors.Is(err, worklog.ErrProjectNotFound) {
			b.send(ctx, user.ChatID, "⚠️ That project no longer exists.", nil)
			return
		}
		b.send(ctx, user.ChatID, settingError(pendingProjectRate, err), cancelKeyboard())
		return
	}

	confirmation := fmt.Sprintf("✅ <b>%s</b> now pays %s.",
		esc(project.Name), describeRate(project.Rate, user.Currency))
	if project.Rate.IsMonthly() {
		confirmation = fmt.Sprintf(
			"✅ <b>%s</b> now pays %s, which is <b>%s/h</b> over %.2f hours a month.",
			esc(project.Name),
			esc(money.Format(project.Rate.MonthlyCents, user.Currency)),
			esc(money.Format(project.Rate.Hourly(), user.Currency)),
			money.MonthlyHours)
	}
	b.send(ctx, user.ChatID, confirmation+"\n\n"+renderProjectCard(user, project),
		projectCardKeyboard(project))
}

// confirmSetting acknowledges an account wide setting and reshows settings.
func (b *Bot) confirmSetting(ctx context.Context, user worklog.User, confirmation string) {
	projects, _ := b.svc.Projects(ctx, user.ID)
	b.send(ctx, user.ChatID, confirmation+"\n\n"+renderSettings(user, projects), settingsKeyboard())
}

// promptFor asks for a value and records the pending state so the next plain
// message is read as the answer.
func (b *Bot) promptFor(ctx context.Context, user worklog.User, p pending) {
	if err := b.svc.SetPending(ctx, user.ID, p.String()); err != nil {
		b.log.Error("set pending failed", "user_id", user.ID, "action", p.Action, "error", err)
		b.send(ctx, user.ChatID, "⚠️ Something went wrong. Please try again.", nil)
		return
	}
	b.send(ctx, user.ChatID, prompts[p.Action], cancelKeyboard())
}

// prompts is the question asked for each awaited value.
var prompts = map[string]string{
	pendingNewProject: "📁 <b>What is the project called?</b>\n\n" +
		"For example <code>Acme</code>, <code>Freelance</code> or <code>Day job</code>.",
	pendingProjectRate: "💵 <b>Send the hourly rate for this project.</b>\n\n" +
		"For example <code>25</code>, <code>25.50</code> or <code>1200</code>.",
	pendingProjectMonthly: "🗓️ <b>Send the monthly salary for this project.</b>\n\n" +
		"For example <code>3360</code> or <code>3360.00</code>.\n\n" +
		"I convert it at 173.33 hours a month (52 weeks × 40 h ÷ 12), so " +
		"<code>3360</code> works out to about <code>19.38</code> an hour.",
	pendingProjectRename: "✏️ <b>Send the new name for this project.</b>",
	pendingCurrency: "💱 <b>Send your currency.</b>\n\n" +
		"For example <code>USD</code>, <code>EUR</code>, <code>PLN</code> or <code>€</code>.",
	pendingTimezone: "🌍 <b>Send your timezone.</b>\n\n" +
		"Use an IANA name such as <code>Europe/Kyiv</code>, <code>Europe/Warsaw</code>, " +
		"<code>America/New_York</code> or <code>UTC</code>.\n\n" +
		"This decides when your day starts and ends.",
}

// settingError turns a validation failure into a message with a nudge.
func settingError(field string, err error) string {
	switch {
	case errors.Is(err, worklog.ErrUnknownTimezone):
		return "❌ I do not know that timezone.\n\n" +
			"Use an IANA name like <code>Europe/Kyiv</code> or <code>America/New_York</code>."
	case errors.Is(err, money.ErrInvalidAmount):
		return "❌ That does not look like an amount.\n\n" +
			"Send a number such as <code>25</code> or <code>25.50</code>."
	default:
		return "❌ " + esc(err.Error()) + "\n\nPlease try again."
	}
}

// projectError explains why a project could not be created or renamed.
func projectError(err error) string {
	switch {
	case errors.Is(err, worklog.ErrProjectExists):
		return "❌ You already have a project with that name.\n\nPick another."
	case errors.Is(err, worklog.ErrInvalidName):
		return "❌ " + esc(err.Error()) + "\n\nPlease try again."
	case errors.Is(err, worklog.ErrProjectNotFound):
		return "⚠️ That project no longer exists."
	default:
		return "⚠️ Something went wrong. Please try again."
	}
}

// pluralProjects renders "1 project" or "3 projects".
func pluralProjects(n int) string {
	if n == 1 {
		return "project"
	}
	return "projects"
}

// handleCallback reacts to the inline buttons.
func (b *Bot) handleCallback(ctx context.Context, cq *telegram.CallbackQuery) {
	if cq.From == nil || cq.Message == nil || cq.Message.Chat == nil {
		return
	}
	// Acknowledge first so the button stops spinning even if the work fails.
	if err := b.api.AnswerCallbackQuery(ctx, cq.ID, ""); err != nil {
		b.log.Warn("answerCallbackQuery failed", "error", err)
	}

	user, err := b.svc.Register(ctx, worklog.User{
		ID:        cq.From.ID,
		ChatID:    cq.Message.Chat.ID,
		Username:  cq.From.Username,
		FirstName: cq.From.FirstName,
	})
	if err != nil {
		b.log.Error("register user failed", "user_id", cq.From.ID, "error", err)
		return
	}

	// Payloads without an id are plain actions; the rest name a project.
	switch cq.Data {
	case cbNewProject:
		b.promptFor(ctx, user, pending{Action: pendingNewProject})
		return
	case cbListProjects:
		b.showProjects(ctx, user)
		return
	case cbSetCurrency:
		b.promptFor(ctx, user, pending{Action: pendingCurrency})
		return
	case cbSetTimezone:
		b.promptFor(ctx, user, pending{Action: pendingTimezone})
		return
	case cbReset:
		b.askReset(ctx, user)
		return
	case cbResetConfirm:
		b.doReset(ctx, user)
		return
	case cbCancel:
		if err := b.svc.SetPending(ctx, user.ID, ""); err != nil {
			b.log.Error("clear pending failed", "user_id", user.ID, "error", err)
		}
		b.send(ctx, user.ChatID, "Cancelled.", nil)
		return
	}

	prefix, projectID, ok := callbackID(cq.Data)
	if !ok {
		return
	}
	switch prefix {
	case cbStartProject:
		b.startOn(ctx, user, projectID)
	case cbOpenProject:
		b.showProjectCard(ctx, user, projectID)
	case cbProjectHourly:
		b.promptFor(ctx, user, pending{Action: pendingProjectRate, ProjectID: projectID})
	case cbProjectMonthly:
		b.promptFor(ctx, user, pending{Action: pendingProjectMonthly, ProjectID: projectID})
	case cbProjectRename:
		b.promptFor(ctx, user, pending{Action: pendingProjectRename, ProjectID: projectID})
	case cbProjectMode:
		b.switchProjectMode(ctx, user, projectID)
	case cbProjectDelete:
		b.askDeleteProject(ctx, user, projectID)
	case cbProjectConfirm:
		b.doDeleteProject(ctx, user, projectID)
	}
}

// switchProjectMode flips a project between hourly and monthly pay.
func (b *Bot) switchProjectMode(ctx context.Context, user worklog.User, projectID int64) {
	current, err := b.svc.Project(ctx, user.ID, projectID)
	if err != nil {
		b.send(ctx, user.ChatID, "⚠️ That project no longer exists.", nil)
		return
	}
	mode := worklog.RateModeMonthly
	if current.Rate.IsMonthly() {
		mode = worklog.RateModeHourly
	}

	project, err := b.svc.SetProjectRateMode(ctx, user.ID, projectID, mode)
	if err != nil {
		b.log.Error("switch rate mode failed", "user_id", user.ID, "error", err)
		b.send(ctx, user.ChatID, "⚠️ I could not switch the pay mode. Please try again.", nil)
		return
	}
	b.send(ctx, user.ChatID,
		fmt.Sprintf("✅ <b>%s</b> now pays %s.\n\n",
			esc(project.Name), describeRate(project.Rate, user.Currency))+
			renderProjectCard(user, project),
		projectCardKeyboard(project))
}
