package headroom

import (
	"math"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

// history builds samples one step apart from free-space readings in GiB.
func history(step time.Duration, freeGiB ...float64) []Sample {
	out := make([]Sample, len(freeGiB))
	for i, f := range freeGiB {
		out[i] = Sample{At: t0.Add(time.Duration(i) * step), Total: 460 * GiB, Free: int64(f * float64(GiB))}
	}
	return out
}

// ramp is n readings from start, changing by perStep each time.
func ramp(start, perStep float64, n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = start + perStep*float64(i)
	}
	return out
}

func wantDays(t *testing.T, name string, s []Sample, want float64) {
	t.Helper()
	got, ok := Forecast(s)
	if !ok || math.Abs(got-want) > 1e-6 {
		t.Errorf("%s: Forecast = %v, %v; want %v", name, got, ok, want)
	}
}

func TestForecastSteadyFall(t *testing.T) {
	// 100 GiB falling 1 GiB an hour for a day: 76 left at 24 GiB a day.
	wantDays(t, "steady", history(time.Hour, ramp(100, -1, 25)...), 76.0/24)
}

func TestForecastRestartsAtAnUpwardJump(t *testing.T) {
	// A day at 1 GiB/h, then a clean frees 40 GiB, then 2 GiB/h for six
	// hours. Only the six hours after the clean say how fast the disk
	// fills now: 104 GiB left at 48 GiB a day.
	readings := append(ramp(100, -1, 25), ramp(116, -2, 7)...)
	wantDays(t, "after the jump", history(time.Hour, readings...), 104.0/48)

	// Two hours after the clean is too short a window to say.
	short := append(ramp(100, -1, 25), ramp(116, -2, 3)...)
	if d, ok := Forecast(history(time.Hour, short...)); ok {
		t.Errorf("two hours after a jump must not forecast, got %v", d)
	}
}

func TestForecastReadsAtMostTheWindow(t *testing.T) {
	// Two days at 5 GiB/h, then exactly 72 h at 0.5 GiB/h. The steep
	// stretch is older than the window and must not count.
	readings := append(ramp(435, -5, 48), ramp(199.5, -0.5, 73)...)
	wantDays(t, "window", history(time.Hour, readings...), 163.5/12)
}

func TestForecastSmallRisesAreNotJumps(t *testing.T) {
	// A 1 GiB rise is noise (a swap file deleted): the window keeps
	// its history instead of restarting, so there is a forecast.
	readings := append(ramp(100, -1, 10), ramp(92, -1, 3)...)
	if _, ok := Forecast(history(time.Hour, readings...)); !ok {
		t.Error("a 1 GiB rise must not restart the window")
	}
}

func TestForecastSaysNothingWithoutATrend(t *testing.T) {
	tests := []struct {
		name string
		s    []Sample
	}{
		{"no samples", nil},
		{"two samples", history(3*time.Hour, 100, 90)},
		{"under three hours", history(15*time.Minute, ramp(100, -1, 8)...)},
		{"flat", history(time.Hour, 80, 80, 80, 80, 80)},
		{"rising", history(time.Hour, ramp(80, 0.5, 6)...)},
	}
	for _, tt := range tests {
		if d, ok := Forecast(tt.s); ok {
			t.Errorf("%s: Forecast = %v, want no forecast", tt.name, d)
		}
	}
}
