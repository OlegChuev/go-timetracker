// Package money handles monetary amounts as integer minor units (cents) so that
// rates and earnings never suffer floating point drift.
package money

import (
	"errors"
	"fmt"
	"strings"
)

// MaxCents caps a rate at 10,000,000.00 per hour, which is high enough for any
// real currency while keeping rate*seconds far inside int64.
const MaxCents int64 = 1_000_000_000

var ErrInvalidAmount = errors.New("invalid amount")

// ParseCents reads a human typed amount such as "25", "25.5", "1 200,75" or
// "$25.50" and returns the value in cents. Both '.' and ',' are accepted as the
// decimal separator, and spaces are treated as thousand separators.
func ParseCents(raw string) (int64, error) {
	s := strings.TrimSpace(raw)
	for _, cut := range []string{" ", " ", "_", "'"} {
		s = strings.ReplaceAll(s, cut, "")
	}
	s = strings.TrimFunc(s, func(r rune) bool {
		return r != '.' && r != ',' && r != '-' && (r < '0' || r > '9')
	})
	s = strings.ReplaceAll(s, ",", ".")
	if s == "" {
		return 0, ErrInvalidAmount
	}
	if strings.HasPrefix(s, "-") {
		return 0, fmt.Errorf("%w: must not be negative", ErrInvalidAmount)
	}

	whole, frac, hasFrac := strings.Cut(s, ".")
	if strings.Contains(frac, ".") {
		return 0, ErrInvalidAmount
	}
	if whole == "" {
		whole = "0"
	}
	if hasFrac {
		switch {
		case len(frac) == 0:
			frac = "00"
		case len(frac) == 1:
			frac += "0"
		case len(frac) > 2:
			return 0, fmt.Errorf("%w: at most 2 decimal places", ErrInvalidAmount)
		}
	} else {
		frac = "00"
	}

	var cents int64
	for _, part := range []string{whole, frac} {
		for _, r := range part {
			if r < '0' || r > '9' {
				return 0, ErrInvalidAmount
			}
			cents = cents*10 + int64(r-'0')
			if cents > MaxCents {
				return 0, fmt.Errorf("%w: amount is too large", ErrInvalidAmount)
			}
		}
	}
	return cents, nil
}

// Earnings converts worked seconds at rateCents per hour into cents, rounding
// half up. Integer math throughout keeps the result exact.
func Earnings(rateCents int64, seconds int64) int64 {
	if rateCents <= 0 || seconds <= 0 {
		return 0
	}
	return (rateCents*seconds + 1800) / 3600
}

// A month of full time work is 52 weeks x 40 hours / 12 months, which is
// 173 1/3 hours, held here as the exact fraction 520/3 so no precision is lost
// converting between monthly and hourly pay.
const (
	monthlyHoursNum = 520
	monthlyHoursDen = 3
	// secondsPerMonth is 520/3 hours expressed in seconds: 624000.
	secondsPerMonth = monthlyHoursNum * 3600 / monthlyHoursDen
)

// MonthlyHours is the norm used to convert a monthly salary, for display.
const MonthlyHours = float64(monthlyHoursNum) / float64(monthlyHoursDen)

// HourlyFromMonthly converts a monthly salary to the equivalent hourly rate,
// rounded to whole cents. It is meant for display: pay is computed straight
// from the monthly figure by EarningsFromMonthly, which does not round early.
func HourlyFromMonthly(monthlyCents int64) int64 {
	if monthlyCents <= 0 {
		return 0
	}
	return (monthlyCents*monthlyHoursDen + monthlyHoursNum/2) / monthlyHoursNum
}

// EarningsFromMonthly converts worked seconds on a monthly salary into cents.
// Deriving an hourly rate first would round away up to a cent per hour, so the
// monthly figure is used directly and a full 173 1/3 hour month pays exactly
// the salary.
func EarningsFromMonthly(monthlyCents int64, seconds int64) int64 {
	if monthlyCents <= 0 || seconds <= 0 {
		return 0
	}
	return (monthlyCents*seconds + secondsPerMonth/2) / secondsPerMonth
}

// Format renders cents as "1234.50 USD" with thousand separators.
func Format(cents int64, currency string) string {
	sign := ""
	if cents < 0 {
		sign, cents = "-", -cents
	}
	whole := fmt.Sprintf("%d", cents/100)
	var b strings.Builder
	for i, r := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			b.WriteByte(' ')
		}
		b.WriteRune(r)
	}
	if currency == "" {
		currency = "USD"
	}
	return fmt.Sprintf("%s%s.%02d %s", sign, b.String(), cents%100, currency)
}
