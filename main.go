// Command timetracker runs a Telegram bot that tracks working hours and the
// pay they earn, storing everything in MySQL.
package main

import (
	"context"
	"embed"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	// Embed the tz database so timezone settings work on images without it.
	_ "time/tzdata"

	"github.com/oleg/go-timetracker/internal/bot"
	"github.com/oleg/go-timetracker/internal/config"
	"github.com/oleg/go-timetracker/internal/storage"
	"github.com/oleg/go-timetracker/internal/telegram"
	"github.com/oleg/go-timetracker/internal/worklog"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(".env")
	if err != nil {
		return err
	}
	log := newLogger(cfg.LogLevel)

	// Cancelled on SIGINT or SIGTERM, which unblocks the update loop.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := storage.Open(ctx, cfg.DatabasePath)
	if err != nil {
		return err
	}
	defer db.Close()

	migrations, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		return err
	}
	store := storage.New(db)
	if err := store.Migrate(ctx, migrations); err != nil {
		return err
	}
	log.Info("database ready", "path", cfg.DatabasePath)

	api := telegram.New(cfg.BotToken, log)
	meCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	me, err := api.GetMe(meCtx)
	if err != nil {
		return err
	}
	log.Info("authenticated with Telegram", "bot", me.Username, "id", me.ID)

	service := worklog.NewService(store, time.Now)
	log.Info("listening for updates")

	err = bot.New(api, service, log).Run(ctx)
	if err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	log.Info("shut down cleanly")
	return nil
}

// newLogger builds a structured logger at the configured level.
func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(level)); err != nil {
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: lvl}))
}
