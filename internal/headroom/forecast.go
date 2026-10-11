package headroom

import "time"

const (
	GiB = int64(1) << 30

	// Window is the longest history a forecast reads.
	Window = 72 * time.Hour

	// jumpBytes is a rise between two samples large enough to be a
	// clean rather than noise. The trend before it says nothing about
	// the trend after, so the forecast window restarts there.
	jumpBytes = 2 * GiB

	// minSpan keeps one burst right after a restart from reading as
	// the long-run rate.
	minSpan     = 3 * time.Hour
	minSamples  = 3
	secondsADay = 86400.0
)

// Forecast fits the fall of free space over the samples since the last
// upward jump, at most Window back, and returns how many days the
// current free space lasts at that rate. ok is false when the window
// is too short to say, or free space is not falling.
func Forecast(s []Sample) (daysToFull float64, ok bool) {
	w := forecastWindow(s)
	if len(w) < minSamples {
		return 0, false
	}
	last := w[len(w)-1]
	if last.At.Sub(w[0].At) < minSpan {
		return 0, false
	}
	perSecond := slope(w)
	if perSecond >= 0 {
		return 0, false
	}
	return float64(last.Free) / (-perSecond * secondsADay), true
}

func forecastWindow(s []Sample) []Sample {
	if len(s) == 0 {
		return nil
	}
	last := s[len(s)-1]
	start := len(s) - 1
	for i := len(s) - 1; i > 0; i-- {
		if last.At.Sub(s[i-1].At) > Window || s[i].Free-s[i-1].Free >= jumpBytes {
			break
		}
		start = i - 1
	}
	return s[start:]
}

// slope is the least-squares change of free space in bytes per second.
func slope(w []Sample) float64 {
	t0 := w[0].At
	n := float64(len(w))
	var sumT, sumF, sumTT, sumTF float64
	for _, s := range w {
		t := s.At.Sub(t0).Seconds()
		f := float64(s.Free)
		sumT += t
		sumF += f
		sumTT += t * t
		sumTF += t * f
	}
	den := n*sumTT - sumT*sumT
	if den == 0 {
		return 0
	}
	return (n*sumTF - sumT*sumF) / den
}
