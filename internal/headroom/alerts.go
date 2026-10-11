package headroom

import (
	"fmt"
	"time"
)

type AlertKind string

const (
	AlertFreeBelow  AlertKind = "free-below"
	AlertDaysToFull AlertKind = "days-to-full"
	AlertSwapUp     AlertKind = "swap-up"
	AlertFreeFall   AlertKind = "free-fall"
)

// Bands are the free-space lines, highest first.
var Bands = []int64{50 * GiB, 25 * GiB, 10 * GiB, 5 * GiB}

const (
	// LowDays is the days-to-full line.
	LowDays   = 3.0
	rearmDays = 4.0

	swapRise = 4 * GiB
	freeFall = 5 * GiB

	// acuteWindow is "within 30 minutes" plus slack for timer drift,
	// so two 15-minute ticks back still count.
	acuteWindow = 32 * time.Minute
)

type Alert struct {
	Kind    AlertKind `json:"kind"`
	Message string    `json:"message"`
	// Band is the line crossed, for free-below.
	Band int64 `json:"band,omitempty"`
	// Acute alerts are about minutes, not days. The caller names the
	// process holding the most memory with WithTop.
	Acute      bool     `json:"acute,omitempty"`
	TopProcess *Process `json:"top_process,omitempty"`
}

func (a Alert) WithTop(p Process) Alert {
	a.TopProcess = &p
	a.Message += fmt.Sprintf(" Largest process: %s (pid %d, %s resident).", p.Name, p.PID, Human(p.RSS))
	return a
}

// Human renders bytes the way every alert does.
func Human(n int64) string {
	return fmt.Sprintf("%.1f GiB", float64(n)/float64(GiB))
}

// alertState is which conditions hold after replaying a history. Each
// condition trips at its line and clears only past a wider one, so a
// value hovering at the line alerts once.
type alertState struct {
	bands            []bool
	days, swap, fall bool
}

func rearm(band int64) int64 { return band + max(band/10, GiB) }

func replay(s []Sample) alertState {
	st := alertState{bands: make([]bool, len(Bands))}
	for i := range s {
		st.step(s[:i+1])
	}
	return st
}

func (st *alertState) step(prefix []Sample) {
	cur := prefix[len(prefix)-1]
	for i, band := range Bands {
		switch {
		case cur.Free < band:
			st.bands[i] = true
		case cur.Free >= rearm(band):
			st.bands[i] = false
		}
	}

	days, ok := Forecast(prefix)
	switch {
	case ok && days < LowDays:
		st.days = true
	case !ok || days >= rearmDays:
		st.days = false
	}

	rise, drop := acuteMoves(prefix)
	switch {
	case rise >= swapRise:
		st.swap = true
	case rise < swapRise/2:
		st.swap = false
	}
	switch {
	case drop > freeFall:
		st.fall = true
	case drop < freeFall/2:
		st.fall = false
	}
}

// acuteMoves compares the newest sample with the ones inside the acute
// window before it: how far swap rose and how far free space fell.
func acuteMoves(prefix []Sample) (swapRise, freeDrop int64) {
	cur := prefix[len(prefix)-1]
	for i := len(prefix) - 2; i >= 0; i-- {
		if cur.At.Sub(prefix[i].At) > acuteWindow {
			break
		}
		swapRise = max(swapRise, cur.SwapUsed-prefix[i].SwapUsed)
		freeDrop = max(freeDrop, prefix[i].Free-cur.Free)
	}
	return swapRise, freeDrop
}

// Alerts returns what crossed between two histories: prev is the
// history before the newest sample, cur the history with it. A
// condition that already held in prev stays silent.
func Alerts(prev, cur []Sample) []Alert {
	if len(cur) == 0 {
		return nil
	}
	was, now := replay(prev), replay(cur)
	last := cur[len(cur)-1]
	rise, drop := acuteMoves(cur)

	var out []Alert
	if now.fall && !was.fall {
		out = append(out, Alert{Kind: AlertFreeFall, Acute: true,
			Message: fmt.Sprintf("Free space fell %s in the last 30 minutes; %s left.", Human(drop), Human(last.Free))})
	}
	if now.swap && !was.swap {
		out = append(out, Alert{Kind: AlertSwapUp, Acute: true,
			Message: fmt.Sprintf("Swap grew %s in the last 30 minutes, to %s. Swap files take space on the same disk.", Human(rise), Human(last.SwapUsed))})
	}
	// One step can cross several lines; only the lowest is news.
	for i := len(Bands) - 1; i >= 0; i-- {
		if now.bands[i] && !was.bands[i] {
			out = append(out, Alert{Kind: AlertFreeBelow, Band: Bands[i],
				Message: fmt.Sprintf("Free space is below %s: %s left.", Human(Bands[i]), Human(last.Free))})
			break
		}
	}
	if now.days && !was.days {
		days, _ := Forecast(cur)
		out = append(out, Alert{Kind: AlertDaysToFull,
			Message: fmt.Sprintf("At the current rate the disk fills in %.1f days; %s left.", days, Human(last.Free))})
	}
	return out
}
