package s2

import (
	"errors"
	"net/http"
	"time"
)

const (
	// Advised reconnects to attempt before staying on.
	maxAdvisedReconnects = 1

	// Gap after which the attempt count resets.
	advisedReconnectIdle = 60 * time.Second
)

// advisedReconnects tracks advised reconnects attempted lately.
type advisedReconnects struct {
	count int
	last  time.Time
}

func (a *advisedReconnects) record(now time.Time) {
	if !a.isRecent(now) {
		a.count = 0
	}
	a.last = now
	a.count++
}

// shouldReconnect reports whether to act on advice or stay until the server ends the connection.
func (a *advisedReconnects) shouldReconnect(now time.Time) bool {
	return !a.isRecent(now) || a.count < maxAdvisedReconnects
}

func (a *advisedReconnects) isRecent(now time.Time) bool {
	return !a.last.IsZero() && now.Sub(a.last) <= advisedReconnectIdle
}

func isServerDraining(err error) bool {
	var s2Err *S2Error
	return errors.As(err, &s2Err) &&
		s2Err.Status == http.StatusServiceUnavailable &&
		s2Err.Code == "server_draining"
}
