// Package config loads runtime settings from the environment.
package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"
)

// Config holds everything the process needs to start.
type Config struct {
	BotToken     string
	DatabasePath string
	LogLevel     string
}

// defaultDatabasePath is used when DATABASE_PATH is not set.
const defaultDatabasePath = "data/timetracker.db"

// Load reads configuration from the environment, after merging in any .env
// file next to the binary so local development needs no exporting.
func Load(envFile string) (Config, error) {
	if err := loadDotEnv(envFile); err != nil {
		return Config{}, err
	}

	cfg := Config{
		BotToken:     strings.TrimSpace(os.Getenv("TELEGRAM_BOT_TOKEN")),
		DatabasePath: strings.TrimSpace(os.Getenv("DATABASE_PATH")),
		LogLevel:     strings.TrimSpace(os.Getenv("LOG_LEVEL")),
	}

	if cfg.BotToken == "" {
		return Config{}, fmt.Errorf("TELEGRAM_BOT_TOKEN is not set (see .env.example)")
	}
	if cfg.DatabasePath == "" {
		cfg.DatabasePath = defaultDatabasePath
	}
	if cfg.LogLevel == "" {
		cfg.LogLevel = "info"
	}
	return cfg, nil
}

// loadDotEnv applies KEY=VALUE lines from a file. Values already present in the
// real environment win, and a missing file is not an error.
func loadDotEnv(path string) error {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for lineNo := 1; scanner.Scan(); lineNo++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, value, found := strings.Cut(line, "=")
		if !found {
			return fmt.Errorf("%s line %d: expected KEY=VALUE", path, lineNo)
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		// Strip one layer of matching quotes, which DSNs often carry.
		if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
			value = value[1 : len(value)-1]
		}
		if _, exists := os.LookupEnv(key); !exists {
			if err := os.Setenv(key, value); err != nil {
				return fmt.Errorf("set %s: %w", key, err)
			}
		}
	}
	return scanner.Err()
}
