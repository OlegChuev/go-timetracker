package worklog

import (
	"testing"
	"time"
)

func mustLoad(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("load %s: %v", name, err)
	}
	return loc
}

func TestWeekRangeStartsOnMonday(t *testing.T) {
	utc := time.UTC
	cases := []struct {
		name             string
		now              time.Time
		wantFrom, wantTo string
	}{
		// Wednesday 2026-08-19 sits in the week of Mon 17th to Sun 23rd.
		{"midweek", time.Date(2026, 8, 19, 12, 0, 0, 0, utc), "2026-08-17", "2026-08-23"},
		// Sunday must close the week it is in, not open the next one.
		{"sunday", time.Date(2026, 8, 23, 23, 59, 0, 0, utc), "2026-08-17", "2026-08-23"},
		{"monday", time.Date(2026, 8, 17, 0, 0, 0, 0, utc), "2026-08-17", "2026-08-23"},
		// A week spanning a month boundary must not be clipped.
		{"across months", time.Date(2026, 9, 1, 9, 0, 0, 0, utc), "2026-08-31", "2026-09-06"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			from, to := WeekRange(tc.now, utc)
			if got := from.Format("2006-01-02"); got != tc.wantFrom {
				t.Errorf("from = %s, want %s", got, tc.wantFrom)
			}
			if got := to.Format("2006-01-02"); got != tc.wantTo {
				t.Errorf("to = %s, want %s", got, tc.wantTo)
			}
		})
	}
}

func TestMonthRange(t *testing.T) {
	utc := time.UTC
	cases := []struct {
		now              time.Time
		wantFrom, wantTo string
	}{
		{time.Date(2026, 8, 19, 12, 0, 0, 0, utc), "2026-08-01", "2026-08-31"},
		{time.Date(2026, 2, 10, 12, 0, 0, 0, utc), "2026-02-01", "2026-02-28"},
		{time.Date(2028, 2, 10, 12, 0, 0, 0, utc), "2028-02-01", "2028-02-29"}, // leap year
		{time.Date(2026, 12, 31, 23, 0, 0, 0, utc), "2026-12-01", "2026-12-31"},
	}
	for _, tc := range cases {
		from, to := MonthRange(tc.now, utc)
		if got := from.Format("2006-01-02"); got != tc.wantFrom {
			t.Errorf("from = %s, want %s", got, tc.wantFrom)
		}
		if got := to.Format("2006-01-02"); got != tc.wantTo {
			t.Errorf("to = %s, want %s", got, tc.wantTo)
		}
	}
}

// The user's timezone, not UTC, decides which day a moment belongs to.
func TestDayRangeUsesUserTimezone(t *testing.T) {
	kyiv := mustLoad(t, "Europe/Kyiv")
	// 22:30 UTC is already the next day in Kyiv (UTC+3 in August).
	now := time.Date(2026, 8, 19, 22, 30, 0, 0, time.UTC)

	utcDay, _ := DayRange(now, time.UTC)
	if got := utcDay.Format("2006-01-02"); got != "2026-08-19" {
		t.Errorf("UTC day = %s, want 2026-08-19", got)
	}
	kyivDay, _ := DayRange(now, kyiv)
	if got := kyivDay.Format("2006-01-02"); got != "2026-08-20" {
		t.Errorf("Kyiv day = %s, want 2026-08-20", got)
	}
}

func TestFormatDuration(t *testing.T) {
	cases := map[time.Duration]string{
		0:                                "0s",
		45 * time.Second:                 "45s",
		90 * time.Second:                 "1m 30s",
		8 * time.Hour:                    "8h 00m",
		7*time.Hour + 32*time.Minute:     "7h 32m",
		25*time.Hour + 5*time.Minute:     "25h 05m",
		-3 * time.Hour:                   "0s",
		time.Hour + 59*time.Minute + 999: "1h 59m",
	}
	for in, want := range cases {
		if got := FormatDuration(in); got != want {
			t.Errorf("FormatDuration(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestFormatHours(t *testing.T) {
	cases := map[int64]string{0: "0.00 h", 3600: "1.00 h", 27120: "7.53 h", 1800: "0.50 h"}
	for in, want := range cases {
		if got := FormatHours(in); got != want {
			t.Errorf("FormatHours(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestLocationFallsBackToUTC(t *testing.T) {
	for _, tz := range []string{"", "Mars/Olympus_Mons", "not a zone"} {
		if got := (User{Timezone: tz}).Location(); got != time.UTC {
			t.Errorf("Location() for %q = %v, want UTC", tz, got)
		}
	}
	if got := (User{Timezone: "Europe/Kyiv"}).Location().String(); got != "Europe/Kyiv" {
		t.Errorf("Location() = %s, want Europe/Kyiv", got)
	}
}
