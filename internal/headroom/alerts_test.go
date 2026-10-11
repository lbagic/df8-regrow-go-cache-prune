package headroom

import (
	"strings"
	"testing"
	"time"
)

// alertsAt replays readings one tick at a time, the way the watch loop
// calls Alerts, and returns what fired at each tick.
func alertsAt(s []Sample) [][]Alert {
	out := make([][]Alert, len(s))
	for i := range s {
		out[i] = Alerts(s[:i], s[:i+1])
	}
	return out
}

func kinds(as []Alert) string {
	var ks []string
	for _, a := range as {
		ks = append(ks, string(a.Kind))
	}
	return strings.Join(ks, ",")
}

func TestFreeBandAlertsOnceOnTheWayDown(t *testing.T) {
	// Spaced two hours apart so nothing here is an acute fall.
	fired := alertsAt(history(2*time.Hour, 60, 49, 48, 47, 24, 23))
	want := []int64{0, 50 * GiB, 0, 0, 25 * GiB, 0}
	for i, as := range fired {
		var band int64
		for _, a := range as {
			if a.Kind == AlertFreeBelow {
				band = a.Band
			}
		}
		if band != want[i] {
			t.Errorf("tick %d: free-below band = %s, want %s (alerts %v)", i, Human(band), Human(want[i]), kinds(as))
		}
	}
}

func TestFreeBandCrossingSeveralLinesAlertsTheLowest(t *testing.T) {
	as := Alerts(history(2*time.Hour, 60), history(2*time.Hour, 60, 8))
	if len(as) != 1 || as[0].Kind != AlertFreeBelow || as[0].Band != 10*GiB {
		t.Fatalf("60 → 8 GiB must raise exactly the 10 GiB line, got %+v", as)
	}
	if as[0].Message != "Free space is below 10.0 GiB: 8.0 GiB left." {
		t.Errorf("message = %q", as[0].Message)
	}
}

func TestFirstSampleUnderALineAlerts(t *testing.T) {
	as := Alerts(nil, history(time.Hour, 46))
	if len(as) != 1 || as[0].Kind != AlertFreeBelow || as[0].Band != 50*GiB {
		t.Fatalf("a first sample at 46 GiB must raise the 50 GiB line, got %+v", as)
	}
}

func TestFreeBandHysteresis(t *testing.T) {
	tests := []struct {
		name     string
		readings []float64
		want     []bool // free-below fired at this tick
	}{
		// 51 is back over the line but inside the margin: 49 again is not news.
		{"hovering at the line", []float64{60, 49, 51, 49, 51, 49}, []bool{false, true, false, false, false, false}},
		// 55 clears the 50 GiB line (line + 10%), so the next dip alerts.
		{"recovered past the margin", []float64{60, 49, 55, 49}, []bool{false, true, false, true}},
		// The 5 GiB line re-arms a full GiB up, not 10%.
		{"small line, inside margin", []float64{7, 4.9, 5.9, 4.9}, []bool{true, true, false, false}},
		{"small line, past margin", []float64{7, 4.9, 6, 4.9}, []bool{true, true, false, true}},
	}
	for _, tt := range tests {
		fired := alertsAt(history(2*time.Hour, tt.readings...))
		for i, as := range fired {
			if got := strings.Contains(kinds(as), string(AlertFreeBelow)); got != tt.want[i] {
				t.Errorf("%s: tick %d (%.1f GiB): free-below fired = %v, want %v", tt.name, i, tt.readings[i], got, tt.want[i])
			}
		}
	}
}

func daysFiredAt(s []Sample) []int {
	var ticks []int
	for i, as := range alertsAt(s) {
		if strings.Contains(kinds(as), string(AlertDaysToFull)) {
			ticks = append(ticks, i)
		}
	}
	return ticks
}

func TestDaysToFullAlertsOnCrossingThree(t *testing.T) {
	// 100 GiB falling 1 GiB/h: the forecast is free/24 days, so it
	// drops under 3 at the 71 GiB reading, tick 29, and only there.
	s := history(time.Hour, ramp(100, -1, 36)...)
	if got := daysFiredAt(s); len(got) != 1 || got[0] != 29 {
		t.Fatalf("days-to-full fired at ticks %v, want [29]", got)
	}
	as := Alerts(s[:29], s[:30])
	if len(as) != 1 || !strings.Contains(as[0].Message, "fills in 3.0 days") {
		t.Errorf("alert = %+v", as)
	}
}

func TestDaysToFullHysteresis(t *testing.T) {
	fall := ramp(100, -1, 30) // ends at 71 GiB, forecast just under 3 days

	// Twelve flat hours lift the forecast to between 3 and 4 days; a
	// second fall then takes it under 3 again. It never cleared, so
	// the second crossing is not news.
	hover := history(time.Hour, append(append(fall, ramp(71, 0, 12)...), ramp(69.5, -1.5, 12)...)...)
	if d, ok := Forecast(hover[:42]); !ok || d < LowDays || d >= rearmDays {
		t.Fatalf("fixture: after the flat stretch the forecast must sit between the lines, got %v %v", d, ok)
	}
	if d, ok := Forecast(hover); !ok || d >= LowDays {
		t.Fatalf("fixture: the second fall must end under %v days, got %v %v", LowDays, d, ok)
	}
	if got := daysFiredAt(hover); len(got) != 1 || got[0] != 29 {
		t.Errorf("hovering between the lines: fired at ticks %v, want [29] only", got)
	}

	// Thirty flat hours take the forecast past 4 days, which clears
	// the alert; the next fall under 3 is a new crossing.
	cleared := history(time.Hour, append(append(fall, ramp(71, 0, 30)...), ramp(68, -3, 12)...)...)
	if d, ok := Forecast(cleared[:60]); !ok || d < rearmDays {
		t.Fatalf("fixture: the long flat stretch must lift the forecast past %v days, got %v %v", rearmDays, d, ok)
	}
	got := daysFiredAt(cleared)
	if len(got) != 2 || got[0] != 29 || got[1] < 60 {
		t.Errorf("cleared then crossed again: fired at ticks %v, want tick 29 and one tick in the second fall", got)
	}
}

func TestAcuteAlerts(t *testing.T) {
	at := func(min int, freeGiB, swapGiB float64) Sample {
		return Sample{At: t0.Add(time.Duration(min) * time.Minute), Total: 460 * GiB,
			Free: int64(freeGiB * float64(GiB)), SwapUsed: int64(swapGiB * float64(GiB))}
	}
	tests := []struct {
		name string
		s    []Sample
		want []string // kinds fired per tick
	}{
		{"free falls 6 GiB in 15 minutes",
			[]Sample{at(0, 90, 2), at(15, 84, 2), at(30, 83.5, 2)},
			[]string{"", "free-fall", ""}},
		{"free falls 6 GiB across two ticks",
			[]Sample{at(0, 90, 2), at(15, 87, 2), at(30, 84, 2)},
			[]string{"", "", "free-fall"}},
		{"exactly 5 GiB is not over the line",
			[]Sample{at(0, 90, 2), at(15, 85, 2)},
			[]string{"", ""}},
		{"the same 6 GiB over two hours is not acute",
			[]Sample{at(0, 90, 2), at(60, 87, 2), at(120, 84, 2)},
			[]string{"", "", ""}},
		{"swap up 4.5 GiB in 15 minutes, then still high",
			[]Sample{at(0, 90, 4), at(15, 90, 8.5), at(30, 90, 8.6)},
			[]string{"", "swap-up", ""}},
		{"swap up 4.5 GiB over two hours is not acute",
			[]Sample{at(0, 90, 4), at(60, 90, 6), at(120, 90, 8.5)},
			[]string{"", "", ""}},
		{"a second swap jump after the first settled alerts again",
			[]Sample{at(0, 90, 2), at(15, 90, 7), at(60, 90, 7), at(75, 90, 12)},
			[]string{"", "swap-up", "", "swap-up"}},
		{"a runaway: swap and free move together",
			[]Sample{at(0, 90, 4), at(15, 80, 14)},
			[]string{"", "free-fall,swap-up"}},
	}
	for _, tt := range tests {
		for i, as := range alertsAt(tt.s) {
			if got := kinds(as); got != tt.want[i] {
				t.Errorf("%s: tick %d fired %q, want %q", tt.name, i, got, tt.want[i])
			}
			for _, a := range as {
				if !a.Acute {
					t.Errorf("%s: %s must be marked acute", tt.name, a.Kind)
				}
			}
		}
	}
}

func TestWithTopNamesTheProcess(t *testing.T) {
	a := Alert{Kind: AlertFreeFall, Acute: true, Message: "Free space fell 6.0 GiB in the last 30 minutes; 84.0 GiB left."}
	got := a.WithTop(Process{PID: 412, Name: "runaway", RSS: 28 * GiB})
	if got.TopProcess == nil || got.TopProcess.PID != 412 || !strings.HasSuffix(got.Message, "Largest process: runaway (pid 412, 28.0 GiB resident).") {
		t.Errorf("WithTop = %+v", got)
	}
	if a.TopProcess != nil {
		t.Error("WithTop must not change its receiver")
	}
}
