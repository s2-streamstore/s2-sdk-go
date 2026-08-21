package s2

import "time"

const (
	// Max consecutive advised reconnects before delaying the next one.
	maxImmediateAdvisedReconnects = 3

	// Delay applied past maxImmediateAdvisedReconnects.
	advisedReconnectDelay = 100 * time.Millisecond

	// If no reconnect advice arrives for this long, the consecutive count resets.
	advisedReconnectIdle = 10 * time.Second
)

// advisedReconnects counts consecutive reconnects driven by server advice. A
// draining server keeps acknowledging work, so progress cannot tell a storm
// from an ordinary handover; how rapidly advice repeats can.
type advisedReconnects struct {
	count int
	last  time.Time
}

// record registers an advised reconnect and reports how long to wait before
// opening the next connection.
func (a *advisedReconnects) record(now time.Time) time.Duration {
	if !a.last.IsZero() && now.Sub(a.last) > advisedReconnectIdle {
		a.count = 0
	}
	a.last = now
	a.count++
	if a.count > maxImmediateAdvisedReconnects {
		return advisedReconnectDelay
	}
	return 0
}
