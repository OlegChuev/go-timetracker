package worklog

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
)

// MaxProjectNameLen keeps names short enough to sit on an inline button.
const MaxProjectNameLen = 40

// Project errors surfaced to the bot layer.
var (
	ErrProjectNotFound = errors.New("project not found")
	ErrProjectExists   = errors.New("a project with that name already exists")
	ErrInvalidName     = errors.New("invalid project name")
)

// Project is a stream of work with its own pay rate.
type Project struct {
	ID        int64
	UserID    int64
	Name      string
	Rate      Rate
	CreatedAt time.Time
}

// ProjectTotals pairs a project with what it earned over some period.
type ProjectTotals struct {
	Project Project
	Totals  Totals
}

// CleanProjectName trims and validates a name typed by the user. Control
// characters are rejected outright since they would corrupt the message
// markup, and the length cap keeps the picker readable.
func CleanProjectName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", fmt.Errorf("%w: the name is empty", ErrInvalidName)
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return "", fmt.Errorf("%w: no line breaks or control characters", ErrInvalidName)
		}
	}
	if len([]rune(name)) > MaxProjectNameLen {
		return "", fmt.Errorf("%w: keep it to %d characters", ErrInvalidName, MaxProjectNameLen)
	}
	return name, nil
}
