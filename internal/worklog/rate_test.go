package worklog

import "testing"

func TestRateAmountAndHourly(t *testing.T) {
	hourly := Rate{Mode: RateModeHourly, HourlyCents: 2550, MonthlyCents: 336000}
	if hourly.Amount() != 2550 || hourly.Hourly() != 2550 || hourly.IsMonthly() {
		t.Errorf("hourly rate misread: %+v", hourly)
	}

	monthly := Rate{Mode: RateModeMonthly, HourlyCents: 2550, MonthlyCents: 336000}
	if monthly.Amount() != 336000 || monthly.Hourly() != 1938 || !monthly.IsMonthly() {
		t.Errorf("monthly rate misread: %+v", monthly)
	}
}

func TestNewRateFromSnapshot(t *testing.T) {
	if got := NewRate(RateModeMonthly, 336000); got.Amount() != 336000 || !got.IsMonthly() {
		t.Errorf("NewRate monthly = %+v", got)
	}
	if got := NewRate(RateModeHourly, 2550); got.Amount() != 2550 || got.IsMonthly() {
		t.Errorf("NewRate hourly = %+v", got)
	}
	// An unset or unknown mode must behave as hourly rather than panic.
	if got := NewRate("", 2550); got.Amount() != 2550 || got.IsMonthly() {
		t.Errorf("NewRate with an empty mode = %+v", got)
	}
}

func TestRateIsSet(t *testing.T) {
	cases := []struct {
		name string
		rate Rate
		want bool
	}{
		{"zero value", Rate{}, false},
		{"hourly set", Rate{Mode: RateModeHourly, HourlyCents: 1}, true},
		{"monthly set", Rate{Mode: RateModeMonthly, MonthlyCents: 1}, true},
		// The inactive amount must not make a rate look usable.
		{"monthly mode with only an hourly amount",
			Rate{Mode: RateModeMonthly, HourlyCents: 2500}, false},
		{"hourly mode with only a monthly amount",
			Rate{Mode: RateModeHourly, MonthlyCents: 336000}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.rate.IsSet(); got != tc.want {
				t.Errorf("IsSet() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRateEarnings(t *testing.T) {
	hourly := Rate{Mode: RateModeHourly, HourlyCents: 2500}
	if got := hourly.Earnings(8 * 3600); got != 20000 {
		t.Errorf("hourly earnings = %d, want 20000", got)
	}
	monthly := Rate{Mode: RateModeMonthly, MonthlyCents: 336000}
	if got := monthly.Earnings(8 * 3600); got != 15508 {
		t.Errorf("monthly earnings = %d, want 15508", got)
	}
	// A full norm month pays exactly the salary.
	if got := monthly.Earnings(624000); got != 336000 {
		t.Errorf("norm month = %d, want 336000", got)
	}
}

func TestCleanProjectName(t *testing.T) {
	valid := map[string]string{
		"Acme":       "Acme",
		"  Acme  ":   "Acme",
		"Day job #2": "Day job #2",
		"Клиент":     "Клиент",
	}
	for input, want := range valid {
		got, err := CleanProjectName(input)
		if err != nil {
			t.Errorf("CleanProjectName(%q) returned error: %v", input, err)
			continue
		}
		if got != want {
			t.Errorf("CleanProjectName(%q) = %q, want %q", input, got, want)
		}
	}

	invalid := []string{"", "   ", "line\nbreak", "tab\there", string(make([]rune, MaxProjectNameLen+1))}
	for _, input := range invalid {
		if got, err := CleanProjectName(input); err == nil {
			t.Errorf("CleanProjectName(%q) = %q, want an error", input, got)
		}
	}

	// Exactly at the cap is fine.
	atCap := make([]rune, MaxProjectNameLen)
	for i := range atCap {
		atCap[i] = 'a'
	}
	if _, err := CleanProjectName(string(atCap)); err != nil {
		t.Errorf("a name of exactly %d characters was rejected: %v", MaxProjectNameLen, err)
	}
}
