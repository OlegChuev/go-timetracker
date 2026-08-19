package worklog

import "github.com/oleg/go-timetracker/internal/money"

// Pay modes. A user names their pay either by the hour or by the month, and
// the mode decides which of the two stored amounts is in force.
const (
	RateModeHourly  = "hourly"
	RateModeMonthly = "monthly"
)

// Rate is a pay rate in one of the two modes. Both amounts are kept so that
// switching modes back and forth does not lose the other figure.
type Rate struct {
	Mode         string
	HourlyCents  int64
	MonthlyCents int64
}

// NewRate builds a Rate from a mode and the single amount recorded for it,
// which is how a shift stores the rate it was paid at.
func NewRate(mode string, cents int64) Rate {
	if mode == RateModeMonthly {
		return Rate{Mode: RateModeMonthly, MonthlyCents: cents}
	}
	return Rate{Mode: RateModeHourly, HourlyCents: cents}
}

// IsMonthly reports whether pay is named per month.
func (r Rate) IsMonthly() bool { return r.Mode == RateModeMonthly }

// Amount is the figure the user actually typed, in whichever mode is active.
// This is what gets snapshotted onto a shift.
func (r Rate) Amount() int64 {
	if r.IsMonthly() {
		return r.MonthlyCents
	}
	return r.HourlyCents
}

// IsSet reports whether the active mode has a usable amount.
func (r Rate) IsSet() bool { return r.Amount() > 0 }

// Hourly is the hourly rate implied by this rate, rounded to whole cents. Use
// it for display only; Earnings does not round this early.
func (r Rate) Hourly() int64 {
	if r.IsMonthly() {
		return money.HourlyFromMonthly(r.MonthlyCents)
	}
	return r.HourlyCents
}

// Earnings is the pay for the given worked seconds under this rate.
func (r Rate) Earnings(seconds int64) int64 {
	if r.IsMonthly() {
		return money.EarningsFromMonthly(r.MonthlyCents, seconds)
	}
	return money.Earnings(r.HourlyCents, seconds)
}
