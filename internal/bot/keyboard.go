package bot

import (
	"strconv"
	"strings"

	"github.com/oleg/go-timetracker/internal/telegram"
	"github.com/oleg/go-timetracker/internal/worklog"
)

// Reply keyboard button labels. Incoming text is matched against these, so the
// labels double as the command vocabulary.
const (
	btnStart    = "▶️ Start work"
	btnStop     = "⏹ Stop work"
	btnToday    = "📊 Today"
	btnStats    = "📈 Statistics"
	btnProjects = "📁 Projects"
	btnSettings = "⚙️ Settings"
	btnHelp     = "❓ Help"
)

// Callback payloads. Those naming a project carry its id after a colon, which
// callbackID reads back out.
const (
	cbStartProject   = "start"
	cbOpenProject    = "proj"
	cbNewProject     = "proj:new"
	cbListProjects   = "proj:list"
	cbProjectHourly  = "prate"
	cbProjectMonthly = "pmonth"
	cbProjectMode    = "pmode"
	cbProjectRename  = "prename"
	cbProjectDelete  = "pdel"
	cbProjectConfirm = "pdelyes"
	cbSetCurrency    = "set:currency"
	cbSetTimezone    = "set:timezone"
	cbCancel         = "set:cancel"
	cbReset          = "reset:ask"
	cbResetConfirm   = "reset:yes"
)

// callbackID splits "prate:7" into its prefix and the project id it names.
func callbackID(data string) (prefix string, id int64, ok bool) {
	prefix, rest, found := strings.Cut(data, ":")
	if !found {
		return data, 0, false
	}
	id, err := strconv.ParseInt(rest, 10, 64)
	if err != nil {
		return prefix, 0, false
	}
	return prefix, id, true
}

// withID builds a callback payload naming a project.
func withID(prefix string, id int64) string {
	return prefix + ":" + strconv.FormatInt(id, 10)
}

// mainKeyboard is the persistent keyboard shown under the message box.
func mainKeyboard() telegram.ReplyKeyboardMarkup {
	return telegram.ReplyKeyboardMarkup{
		Keyboard: [][]telegram.KeyboardButton{
			{{Text: btnStart}, {Text: btnStop}},
			{{Text: btnToday}, {Text: btnStats}},
			{{Text: btnProjects}, {Text: btnSettings}},
			{{Text: btnHelp}},
		},
		ResizeKeyboard: true,
		IsPersistent:   true,
	}
}

// projectPickerKeyboard asks which project a shift is starting on. Projects
// without a rate are still listed, so tapping one leads to setting its rate
// rather than leaving the user wondering where it went.
func projectPickerKeyboard(projects []worklog.Project) telegram.InlineKeyboardMarkup {
	rows := make([][]telegram.InlineKeyboardButton, 0, len(projects)+1)
	for _, p := range projects {
		label := p.Name
		if !p.Rate.IsSet() {
			label += " — no rate yet"
		}
		rows = append(rows, []telegram.InlineKeyboardButton{
			{Text: label, CallbackData: withID(cbStartProject, p.ID)},
		})
	}
	rows = append(rows, []telegram.InlineKeyboardButton{
		{Text: "➕ New project", CallbackData: cbNewProject},
	})
	return telegram.InlineKeyboardMarkup{InlineKeyboard: rows}
}

// projectListKeyboard opens each project for editing.
func projectListKeyboard(projects []worklog.Project) telegram.InlineKeyboardMarkup {
	rows := make([][]telegram.InlineKeyboardButton, 0, len(projects)+1)
	for _, p := range projects {
		rows = append(rows, []telegram.InlineKeyboardButton{
			{Text: "⚙️ " + p.Name, CallbackData: withID(cbOpenProject, p.ID)},
		})
	}
	rows = append(rows, []telegram.InlineKeyboardButton{
		{Text: "➕ New project", CallbackData: cbNewProject},
	})
	return telegram.InlineKeyboardMarkup{InlineKeyboard: rows}
}

// projectCardKeyboard edits one project. The mode switch only appears once the
// other amount exists, so it can never leave the project unable to bill.
func projectCardKeyboard(p worklog.Project) telegram.InlineKeyboardMarkup {
	rows := [][]telegram.InlineKeyboardButton{
		{
			{Text: "💵 Hourly rate", CallbackData: withID(cbProjectHourly, p.ID)},
			{Text: "🗓️ Monthly rate", CallbackData: withID(cbProjectMonthly, p.ID)},
		},
	}
	switch {
	case p.Rate.IsMonthly() && p.Rate.HourlyCents > 0:
		rows = append(rows, []telegram.InlineKeyboardButton{
			{Text: "↩️ Switch to hourly pay", CallbackData: withID(cbProjectMode, p.ID)},
		})
	case !p.Rate.IsMonthly() && p.Rate.MonthlyCents > 0:
		rows = append(rows, []telegram.InlineKeyboardButton{
			{Text: "↩️ Switch to monthly pay", CallbackData: withID(cbProjectMode, p.ID)},
		})
	}
	rows = append(rows,
		[]telegram.InlineKeyboardButton{
			{Text: "✏️ Rename", CallbackData: withID(cbProjectRename, p.ID)},
			{Text: "🗑 Delete", CallbackData: withID(cbProjectDelete, p.ID)},
		},
		[]telegram.InlineKeyboardButton{
			{Text: "⬅️ All projects", CallbackData: cbListProjects},
		},
	)
	return telegram.InlineKeyboardMarkup{InlineKeyboard: rows}
}

// confirmKeyboard asks for an explicit yes before something irreversible.
func confirmKeyboard(confirmLabel, confirmData string) telegram.InlineKeyboardMarkup {
	return telegram.InlineKeyboardMarkup{
		InlineKeyboard: [][]telegram.InlineKeyboardButton{
			{{Text: confirmLabel, CallbackData: confirmData}},
			{{Text: "✖️ Cancel", CallbackData: cbCancel}},
		},
	}
}

// settingsKeyboard offers the account wide settings.
func settingsKeyboard() telegram.InlineKeyboardMarkup {
	return telegram.InlineKeyboardMarkup{
		InlineKeyboard: [][]telegram.InlineKeyboardButton{
			{{Text: "📁 Projects and rates", CallbackData: cbListProjects}},
			{{Text: "💱 Currency", CallbackData: cbSetCurrency}},
			{{Text: "🌍 Timezone", CallbackData: cbSetTimezone}},
			{{Text: "🗑 Reset everything", CallbackData: cbReset}},
		},
	}
}

// cancelKeyboard lets the user abandon a pending text prompt.
func cancelKeyboard() telegram.InlineKeyboardMarkup {
	return telegram.InlineKeyboardMarkup{
		InlineKeyboard: [][]telegram.InlineKeyboardButton{
			{{Text: "✖️ Cancel", CallbackData: cbCancel}},
		},
	}
}

// botCommands populates the Telegram command menu.
func botCommands() []telegram.BotCommand {
	return []telegram.BotCommand{
		{Command: "start", Description: "Show the keyboard and get started"},
		{Command: "work", Description: "Start a shift"},
		{Command: "stop", Description: "Stop the running shift"},
		{Command: "today", Description: "Hours and pay for today"},
		{Command: "stats", Description: "Day, week and month totals"},
		{Command: "projects", Description: "Your projects and their rates"},
		{Command: "newproject", Description: "Add a project, e.g. /newproject Acme"},
		{Command: "settings", Description: "Currency and timezone"},
		{Command: "timezone", Description: "Set the timezone, e.g. /timezone Europe/Kyiv"},
		{Command: "reset", Description: "Delete all projects and history"},
		{Command: "help", Description: "How this bot works"},
	}
}
