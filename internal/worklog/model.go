// Package worklog holds the time tracking domain: users, shifts and the
// aggregation of worked time into daily, weekly and monthly totals.
package worklog

import (
	"fmt"
	"time"
)

// User is a tracked Telegram account together with its settings. Pay rates
// live on projects, not here.
type User struct {
	ID            int64
	ChatID        int64
	Username      string
	FirstName     string
	Currency      string
	Timezone      string
	PendingAction string
}

// Location resolves the user's IANA timezone, falling back to UTC when the
// stored name is not present in the tz database.
func (u User) Location() *time.Location {
	if u.Timezone == "" {
		return time.UTC
	}
	loc, err := time.LoadLocation(u.Timezone)
	if err != nil {
		return time.UTC
	}
	return loc
}

// Shift is one work session on one project. EndedAt is nil while it runs.
type Shift struct {
	ID     int64
	UserID int64
	// ProjectID and ProjectName identify the work; the name is carried along
	// so a shift can be displayed without a second lookup.
	ProjectID       int64
	ProjectName     string
	StartedAt       time.Time
	EndedAt         *time.Time
	WorkDate        time.Time
	Timezone        string
	DurationSeconds int64
	// Rate is snapshotted when the shift closes, so later rate changes never
	// rewrite what a finished shift paid.
	Rate          Rate
	EarningsCents int64
	Currency      string
}

// IsOpen reports whether the shift is still running.
func (s Shift) IsOpen() bool { return s.EndedAt == nil }

// Totals is the aggregate of a set of shifts over some period.
type Totals struct {
	Seconds       int64
	EarningsCents int64
	Shifts        int
}

// Summary bundles the three reporting periods plus the running shift, if any.
type Summary struct {
	Today Totals
	Week  Totals
	Month Totals
	// ByProject breaks this month down per project, busiest first.
	ByProject []ProjectTotals
	Open      *Shift
	OpenFor   time.Duration
}

// DayRange returns the single day containing now in loc.
func DayRange(now time.Time, loc *time.Location) (from, to time.Time) {
	local := now.In(loc)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	return day, day
}

// WeekRange returns the Monday-to-Sunday week containing now in loc.
func WeekRange(now time.Time, loc *time.Location) (from, to time.Time) {
	local := now.In(loc)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	// time.Weekday counts Sunday as 0; shift so Monday is the first day.
	offset := (int(day.Weekday()) + 6) % 7
	monday := day.AddDate(0, 0, -offset)
	return monday, monday.AddDate(0, 0, 6)
}

// MonthRange returns the first and last day of the month containing now in loc.
func MonthRange(now time.Time, loc *time.Location) (from, to time.Time) {
	local := now.In(loc)
	first := time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, loc)
	return first, first.AddDate(0, 1, -1)
}

// FormatDuration renders a duration as "7h 32m", dropping empty leading units
// and falling back to seconds for very short spans.
func FormatDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	total := int64(d / time.Second)
	hours, minutes, seconds := total/3600, (total%3600)/60, total%60
	switch {
	case hours > 0:
		return fmt.Sprintf("%dh %02dm", hours, minutes)
	case minutes > 0:
		return fmt.Sprintf("%dm %02ds", minutes, seconds)
	default:
		return fmt.Sprintf("%ds", seconds)
	}
}

// FormatHours renders worked seconds as a decimal hour count, e.g. "7.53 h".
func FormatHours(seconds int64) string {
	return fmt.Sprintf("%.2f h", float64(seconds)/3600)
}
