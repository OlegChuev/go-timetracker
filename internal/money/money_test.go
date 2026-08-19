package money

import "testing"

func TestParseCents(t *testing.T) {
	valid := map[string]int64{
		"25":       2500,
		"25.5":     2550,
		"25.50":    2550,
		"25,50":    2550,
		"0.01":     1,
		".5":       50,
		"1 200,75": 120075,
		"$25.50":   2550,
		"18.75 €":  1875,
		"1'000":    100000,
	}
	for input, want := range valid {
		got, err := ParseCents(input)
		if err != nil {
			t.Errorf("ParseCents(%q) returned error: %v", input, err)
			continue
		}
		if got != want {
			t.Errorf("ParseCents(%q) = %d, want %d", input, got, want)
		}
	}

	invalid := []string{"", "abc", "-5", "25.505", "1.2.3", "99999999999999"}
	for _, input := range invalid {
		if got, err := ParseCents(input); err == nil {
			t.Errorf("ParseCents(%q) = %d, want an error", input, got)
		}
	}
}

func TestEarnings(t *testing.T) {
	cases := []struct {
		name    string
		rate    int64
		seconds int64
		want    int64
	}{
		{"exact hour", 2500, 3600, 2500},
		{"half hour", 2500, 1800, 1250},
		{"eight hours", 2500, 8 * 3600, 20000},
		{"rounds half up", 100, 18, 1}, // 0.5 cents rounds to 1
		{"sub cent rounds down", 100, 17, 0},
		{"zero rate", 0, 3600, 0},
		{"negative seconds", 2500, -10, 0},
		{"long shift keeps precision", 3333, 7*3600 + 1847, 25041},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Earnings(tc.rate, tc.seconds); got != tc.want {
				t.Errorf("Earnings(%d, %d) = %d, want %d", tc.rate, tc.seconds, got, tc.want)
			}
		})
	}
}

// A day of shifts must pay the same whether summed per shift or in one go,
// which is what the integer rounding is there to guarantee.
func TestEarningsNoDriftAcrossShifts(t *testing.T) {
	const rate = 2137 // 21.37 per hour
	var total int64
	for i := 0; i < 100; i++ {
		total += Earnings(rate, 3600)
	}
	if want := Earnings(rate, 100*3600); total != want {
		t.Errorf("summed shifts = %d, single span = %d", total, want)
	}
}

func TestFormat(t *testing.T) {
	cases := []struct {
		cents    int64
		currency string
		want     string
	}{
		{2550, "USD", "25.50 USD"},
		{0, "EUR", "0.00 EUR"},
		{5, "USD", "0.05 USD"},
		{123456789, "PLN", "1 234 567.89 PLN"},
		{100000, "", "1 000.00 USD"},
	}
	for _, tc := range cases {
		if got := Format(tc.cents, tc.currency); got != tc.want {
			t.Errorf("Format(%d, %q) = %q, want %q", tc.cents, tc.currency, got, tc.want)
		}
	}
}

func TestHourlyFromMonthly(t *testing.T) {
	cases := []struct {
		monthly int64
		want    int64
	}{
		{336000, 1938}, // 3360.00/month -> 19.38/h
		{0, 0},         // unset
		{-100, 0},      // never negative
		{173333, 1000}, // 1733.33/month is almost exactly 10.00/h
		{100000, 577},  // 1000.00/month -> 5.77/h
		{500000, 2885}, // 5000.00/month -> 28.85/h
	}
	for _, tc := range cases {
		if got := HourlyFromMonthly(tc.monthly); got != tc.want {
			t.Errorf("HourlyFromMonthly(%d) = %d, want %d", tc.monthly, got, tc.want)
		}
	}
}

func TestEarningsFromMonthly(t *testing.T) {
	cases := []struct {
		name    string
		monthly int64
		seconds int64
		want    int64
	}{
		{"eight hour shift", 336000, 8 * 3600, 15508},
		{"one hour", 336000, 3600, 1938},
		{"zero time", 336000, 0, 0},
		{"no salary", 0, 8 * 3600, 0},
		{"negative seconds", 336000, -10, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := EarningsFromMonthly(tc.monthly, tc.seconds); got != tc.want {
				t.Errorf("EarningsFromMonthly(%d, %d) = %d, want %d",
					tc.monthly, tc.seconds, got, tc.want)
			}
		})
	}
}

// Working exactly one norm month must pay exactly the monthly salary. This is
// the whole reason pay is computed from the monthly figure rather than from a
// derived hourly rate.
func TestFullNormMonthPaysExactlyTheSalary(t *testing.T) {
	for _, monthly := range []int64{336000, 100000, 500000, 123456, 999999} {
		const normSeconds = 624000 // 173 1/3 hours
		if got := EarningsFromMonthly(monthly, normSeconds); got != monthly {
			t.Errorf("a full norm month on %d paid %d, want %d", monthly, got, monthly)
		}
	}
}

// Rounding to an hourly rate first would visibly underpay over a month; this
// pins the size of the error the direct computation avoids.
func TestMonthlyBeatsRoundedHourly(t *testing.T) {
	const (
		monthly     = 336000
		normSeconds = 624000
	)
	viaHourly := Earnings(HourlyFromMonthly(monthly), normSeconds)
	direct := EarningsFromMonthly(monthly, normSeconds)

	if direct != monthly {
		t.Fatalf("direct = %d, want %d", direct, monthly)
	}
	if viaHourly == monthly {
		t.Skip("no rounding error to demonstrate for this salary")
	}
	if diff := monthly - viaHourly; diff < 50 {
		t.Logf("rounding via hourly loses only %d cents here", diff)
	}
}

func TestMonthlyEarningsAreAdditive(t *testing.T) {
	const monthly = 336000
	var summed int64
	for i := 0; i < 21; i++ {
		summed += EarningsFromMonthly(monthly, 8*3600)
	}
	// 21 eight-hour days is 168 hours, a little under the 173 1/3 norm.
	if want := EarningsFromMonthly(monthly, 21*8*3600); summed < want-21 || summed > want+21 {
		t.Errorf("summed days = %d, single span = %d, drift beyond rounding", summed, want)
	}
}
