package bot

import (
	"fmt"
	"html"
	"strings"
	"time"

	"github.com/oleg/go-timetracker/internal/money"
	"github.com/oleg/go-timetracker/internal/worklog"
)

// timeLayout is how instants are shown back to the user, in their timezone.
const timeLayout = "15:04"

// esc makes arbitrary text safe for HTML parse mode.
func esc(s string) string { return html.EscapeString(s) }

// describeRate renders a pay rate, showing the derived hourly figure when pay
// is named per month so the user can see what an hour is worth.
func describeRate(rate worklog.Rate, currency string) string {
	if !rate.IsSet() {
		return "<i>no rate set</i>"
	}
	if rate.IsMonthly() {
		return fmt.Sprintf("%s/month <i>(≈ %s/h)</i>",
			esc(money.Format(rate.MonthlyCents, currency)),
			esc(money.Format(rate.Hourly(), currency)))
	}
	return esc(money.Format(rate.HourlyCents, currency)) + "/h"
}

// renderShiftClosed is the receipt shown when a shift ends: what this shift
// paid, followed by the running total for the same day across all projects.
func renderShiftClosed(shift worklog.Shift, today worklog.Totals, user worklog.User) string {
	loc := user.Location()
	var b strings.Builder

	fmt.Fprintf(&b, "⏹ <b>Shift finished</b> — %s\n\n", esc(shift.ProjectName))
	fmt.Fprintf(&b, "🕒 %s → %s\n",
		shift.StartedAt.In(loc).Format(timeLayout),
		shift.EndedAt.In(loc).Format(timeLayout))
	fmt.Fprintf(&b, "⏱ Worked: <b>%s</b> (%s)\n",
		worklog.FormatDuration(time.Duration(shift.DurationSeconds)*time.Second),
		worklog.FormatHours(shift.DurationSeconds))
	fmt.Fprintf(&b, "💵 Rate: %s\n", describeRate(shift.Rate, shift.Currency))
	fmt.Fprintf(&b, "💰 This shift: <b>%s</b>\n\n", esc(money.Format(shift.EarningsCents, shift.Currency)))

	fmt.Fprintf(&b, "📅 <b>Today, %s</b> (all projects)\n", shift.WorkDate.Format("Mon 02 Jan"))
	fmt.Fprintf(&b, "   %s across %s\n",
		worklog.FormatHours(today.Seconds), pluralShifts(today.Shifts))
	fmt.Fprintf(&b, "   Earned: <b>%s</b>", esc(money.Format(today.EarningsCents, user.Currency)))
	return b.String()
}

// renderStats is the day, week and month report, followed by a per project
// breakdown of the month.
func renderStats(user worklog.User, s worklog.Summary, now time.Time) string {
	loc := user.Location()
	weekFrom, weekTo := worklog.WeekRange(now, loc)
	monthFrom, _ := worklog.MonthRange(now, loc)

	var b strings.Builder
	b.WriteString("📈 <b>Your statistics</b>\n")

	if s.Open != nil {
		fmt.Fprintf(&b, "\n🟢 <b>On the clock</b> — %s\n", esc(s.Open.ProjectName))
		fmt.Fprintf(&b, "   since %s, %s so far\n",
			s.Open.StartedAt.In(loc).Format(timeLayout),
			worklog.FormatDuration(s.OpenFor))
	}

	writePeriod(&b, "📅 Today", now.In(loc).Format("Mon 02 Jan"), s.Today, user)
	writePeriod(&b, "🗓 This week",
		fmt.Sprintf("%s – %s", weekFrom.Format("02 Jan"), weekTo.Format("02 Jan")), s.Week, user)
	writePeriod(&b, "📆 This month", monthFrom.Format("January 2006"), s.Month, user)

	if len(s.ByProject) > 0 {
		fmt.Fprintf(&b, "\n<b>📁 This month by project</b>\n")
		for _, pt := range s.ByProject {
			fmt.Fprintf(&b, "   <b>%s</b> — %s · %s\n",
				esc(pt.Project.Name),
				worklog.FormatHours(pt.Totals.Seconds),
				esc(money.Format(pt.Totals.EarningsCents, user.Currency)))
		}
	}

	fmt.Fprintf(&b, "\n<i>Totals cover finished shifts only.</i>")
	return b.String()
}

// writePeriod appends one titled block of hours, shift count and earnings.
func writePeriod(b *strings.Builder, title, subtitle string, t worklog.Totals, user worklog.User) {
	fmt.Fprintf(b, "\n<b>%s</b> <i>(%s)</i>\n", title, esc(subtitle))
	fmt.Fprintf(b, "   ⏱ %s  ·  %s\n",
		worklog.FormatHours(t.Seconds), pluralShifts(t.Shifts))
	fmt.Fprintf(b, "   💰 %s\n", esc(money.Format(t.EarningsCents, user.Currency)))
}

// renderToday is the compact single day view.
func renderToday(user worklog.User, s worklog.Summary, now time.Time) string {
	loc := user.Location()
	var b strings.Builder

	fmt.Fprintf(&b, "📊 <b>Today, %s</b>\n\n", now.In(loc).Format("Mon 02 Jan 2006"))
	fmt.Fprintf(&b, "⏱ Worked: <b>%s</b> (%s)\n",
		worklog.FormatHours(s.Today.Seconds),
		worklog.FormatDuration(time.Duration(s.Today.Seconds)*time.Second))
	fmt.Fprintf(&b, "🧾 Shifts: %d\n", s.Today.Shifts)
	fmt.Fprintf(&b, "💰 Earned: <b>%s</b>", esc(money.Format(s.Today.EarningsCents, user.Currency)))

	if s.Open != nil {
		fmt.Fprintf(&b, "\n\n🟢 <b>Still working</b> on %s — %s since %s\n",
			esc(s.Open.ProjectName),
			worklog.FormatDuration(s.OpenFor),
			s.Open.StartedAt.In(loc).Format(timeLayout))
		b.WriteString("   <i>Not counted above until you stop.</i>")
	}
	return b.String()
}

// renderProjectList shows every project with its rate.
func renderProjectList(user worklog.User, projects []worklog.Project) string {
	if len(projects) == 0 {
		return "📁 <b>Projects</b>\n\n" +
			"You have none yet. Every shift is tracked against a project, and each " +
			"one carries its own rate.\n\nTap ➕ New project below, or send " +
			"<code>/newproject Acme</code>."
	}

	var b strings.Builder
	b.WriteString("📁 <b>Projects</b>\n")
	for _, p := range projects {
		fmt.Fprintf(&b, "\n<b>%s</b>\n   %s\n", esc(p.Name), describeRate(p.Rate, user.Currency))
	}
	b.WriteString("\nTap one to change its rate, rename or delete it.")
	return b.String()
}

// renderProjectCard shows one project and what it needs next.
func renderProjectCard(user worklog.User, p worklog.Project) string {
	var b strings.Builder
	fmt.Fprintf(&b, "📁 <b>%s</b>\n\n", esc(p.Name))
	fmt.Fprintf(&b, "💵 Rate: %s\n", describeRate(p.Rate, user.Currency))
	if p.Rate.IsMonthly() {
		fmt.Fprintf(&b, "   <i>%.2f hours a month (52 weeks × 40 h ÷ 12)</i>\n", money.MonthlyHours)
	}
	if other := inactiveAmount(p, user.Currency); other != "" {
		fmt.Fprintf(&b, "   <i>%s</i>\n", other)
	}
	if !p.Rate.IsSet() {
		b.WriteString("\n⚠️ Set a rate before you can clock in on this project.")
	}
	return b.String()
}

// inactiveAmount describes the rate a project is not currently paid at, or ""
// when only one has ever been set.
func inactiveAmount(p worklog.Project, currency string) string {
	if p.Rate.IsMonthly() {
		if p.Rate.HourlyCents > 0 {
			return "Hourly rate on file: " + esc(money.Format(p.Rate.HourlyCents, currency)) + "/h"
		}
		return ""
	}
	if p.Rate.MonthlyCents > 0 {
		return "Monthly rate on file: " + esc(money.Format(p.Rate.MonthlyCents, currency)) + "/month"
	}
	return ""
}

// renderSettings shows the account wide configuration.
func renderSettings(user worklog.User, projects []worklog.Project) string {
	return fmt.Sprintf(
		"⚙️ <b>Settings</b>\n\n"+
			"📁 Projects: <b>%d</b>\n"+
			"💱 Currency: <b>%s</b>\n"+
			"🌍 Timezone: <b>%s</b> (local time %s)\n\n"+
			"Rates live on each project. Tap Projects below to change them.",
		len(projects), esc(user.Currency), esc(user.Timezone),
		time.Now().In(user.Location()).Format(timeLayout))
}

// pluralShifts renders "1 shift" or "3 shifts".
func pluralShifts(n int) string {
	if n == 1 {
		return "1 shift"
	}
	return fmt.Sprintf("%d shifts", n)
}

const helpText = `❓ <b>How this bot works</b>

<b>1. Add a project</b>
Every shift is tracked against a project, and each one carries its own rate.
Send <code>/newproject Acme</code>, then set what it pays:
· by the hour — for example <code>25.50</code>
· by the month — for example <code>3360</code>

A monthly salary is converted at 173.33 hours a month (52 weeks × 40 h ÷ 12),
so <code>3360</code> works out to about 19.38 an hour.
Set your timezone too, so days start and end where you live.

<b>2. Clock in and out</b>
Tap ▶️ <b>Start work</b> and pick the project, then ⏹ <b>Stop work</b> when you
finish. Only one shift runs at a time, whichever project it is on.

<b>3. Read your numbers</b>
Ending a shift shows what it paid and your total for the day.
📊 <b>Today</b> is the day view, 📈 <b>Statistics</b> adds week and month totals
with a per project breakdown.

<b>Commands</b>
/work · /stop · /today · /stats · /projects · /settings
/newproject Acme · /currency EUR · /timezone Europe/Kyiv
/reset — delete every project and all history

<i>A shift that runs past midnight counts on the day it started. Changing a
project's rate only affects shifts you finish afterwards.</i>`
