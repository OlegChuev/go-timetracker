// Package bot routes Telegram updates to the time tracking service.
package bot

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/oleg/go-timetracker/internal/telegram"
	"github.com/oleg/go-timetracker/internal/worklog"
)

const (
	// longPollSeconds is how long getUpdates waits for traffic before
	// returning empty, which keeps idle polling nearly free.
	longPollSeconds = 30
	// maxConcurrent bounds how many updates are handled at once.
	maxConcurrent = 8
)

// pending_action values, naming the text input the bot is waiting for.
const (
	pendingNewProject     = "newproject"
	pendingProjectRate    = "prate"
	pendingProjectMonthly = "pmonth"
	pendingProjectRename  = "prename"
	pendingCurrency       = "currency"
	pendingTimezone       = "timezone"
)

// pending is what the bot is waiting for, and which project it concerns. It
// is stored as a string on the user row so a restart does not strand anyone
// mid-prompt.
type pending struct {
	Action    string
	ProjectID int64
}

// String encodes the state as "prate:7", or just the action when no project
// is involved.
func (p pending) String() string {
	if p.ProjectID == 0 {
		return p.Action
	}
	return p.Action + ":" + strconv.FormatInt(p.ProjectID, 10)
}

// parsePending decodes what String wrote. An unparsable id yields the action
// alone, which the handler then rejects rather than acting on the wrong row.
func parsePending(raw string) pending {
	action, rest, found := strings.Cut(raw, ":")
	if !found {
		return pending{Action: raw}
	}
	id, err := strconv.ParseInt(rest, 10, 64)
	if err != nil {
		return pending{Action: action}
	}
	return pending{Action: action, ProjectID: id}
}

// Bot owns the update loop.
type Bot struct {
	api *telegram.Client
	svc *worklog.Service
	log *slog.Logger
	now func() time.Time
}

// New builds a bot.
func New(api *telegram.Client, svc *worklog.Service, log *slog.Logger) *Bot {
	return &Bot{api: api, svc: svc, log: log, now: time.Now}
}

// Run long polls until ctx is cancelled, dispatching each update to a bounded
// pool of workers. It returns once in-flight updates have finished.
func (b *Bot) Run(ctx context.Context) error {
	if err := b.api.DeleteWebhook(ctx); err != nil {
		b.log.Warn("could not clear webhook", "error", err)
	}
	if err := b.api.SetMyCommands(ctx, botCommands()); err != nil {
		b.log.Warn("could not publish command menu", "error", err)
	}

	var (
		wg      sync.WaitGroup
		slots   = make(chan struct{}, maxConcurrent)
		offset  int64
		backoff = time.Second
	)

	for {
		if ctx.Err() != nil {
			break
		}

		updates, err := b.api.GetUpdates(ctx, offset, longPollSeconds)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			// Respect a server issued cool-down, otherwise back off gently.
			wait := backoff
			var apiErr *telegram.APIError
			if errors.As(err, &apiErr) && apiErr.RetryAfter > 0 {
				wait = time.Duration(apiErr.RetryAfter) * time.Second
			}
			b.log.Error("getUpdates failed", "error", err, "retry_in", wait)
			if !sleep(ctx, wait) {
				break
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second

		for _, upd := range updates {
			if upd.UpdateID >= offset {
				offset = upd.UpdateID + 1
			}
			wg.Add(1)
			slots <- struct{}{}
			go func(u telegram.Update) {
				defer wg.Done()
				defer func() { <-slots }()
				b.dispatch(ctx, u)
			}(upd)
		}
	}

	wg.Wait()
	return ctx.Err()
}

// dispatch handles one update, converting a panic into a log line so a single
// bad message cannot take the bot down.
func (b *Bot) dispatch(ctx context.Context, upd telegram.Update) {
	defer func() {
		if r := recover(); r != nil {
			b.log.Error("panic while handling update", "update_id", upd.UpdateID, "panic", r)
		}
	}()

	// Handling must survive shutdown of the poll loop long enough to reply.
	handleCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
	defer cancel()

	switch {
	case upd.Message != nil:
		b.handleMessage(handleCtx, upd.Message)
	case upd.CallbackQuery != nil:
		b.handleCallback(handleCtx, upd.CallbackQuery)
	}
}

// send delivers a reply with the persistent keyboard attached.
func (b *Bot) send(ctx context.Context, chatID int64, text string, markup any) {
	if markup == nil {
		markup = mainKeyboard()
	}
	_, err := b.api.SendMessage(ctx, telegram.SendMessageRequest{
		ChatID:      chatID,
		Text:        text,
		ParseMode:   "HTML",
		ReplyMarkup: markup,
	})
	if err != nil {
		b.log.Error("sendMessage failed", "chat_id", chatID, "error", err)
	}
}

// sleep waits for d unless ctx is cancelled first. It reports whether the full
// duration elapsed.
func sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// splitCommand separates "/rate 25.50" into "/rate" and "25.50", dropping any
// "@botname" suffix Telegram adds in groups.
func splitCommand(text string) (cmd, args string) {
	trimmed := strings.TrimSpace(text)
	cmd, args, _ = strings.Cut(trimmed, " ")
	if at := strings.IndexByte(cmd, '@'); at >= 0 {
		cmd = cmd[:at]
	}
	return strings.ToLower(cmd), strings.TrimSpace(args)
}
