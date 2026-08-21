package s2

import "time"

const (
	// Advised reconnects allowed without delay before pacing kicks in.
	maxImmediateAdvisedReconnects = 3

	// Delay applied past maxImmediateAdvisedReconnects.
	advisedReconnectDelay = 100 * time.Millisecond

	// Gap after which advice starts a fresh streak rather than continuing one.
	adviceStreakWindow = 10 * time.Second
)

// adviceStreak paces reconnects driven by server advice. A draining server
// keeps acknowledging work, so progress cannot tell a storm from an ordinary
// handover; how quickly advice returns can.
type adviceStreak struct {
	count int
	last  time.Time
}

// record registers an advised reconnect and reports how long to wait before
// opening the next connection.
func (s *adviceStreak) record(now time.Time) time.Duration {
	if !s.last.IsZero() && now.Sub(s.last) > adviceStreakWindow {
		s.count = 0
	}
	s.last = now
	s.count++
	if s.count > maxImmediateAdvisedReconnects {
		return advisedReconnectDelay
	}
	return 0
}
