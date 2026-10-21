// Package transferlimit provides a pure admission policy for serialized parts.
// It neither transfers bytes nor measures network traffic. Every project and
// direction must share one owner/gate; process ownership and restart persistence
// belong to a separate integration boundary.
package transferlimit

import (
	"errors"
	"sync"
	"time"
)

const (
	LimitBytesPerMinute = 5_000_000
	MaxPartBytes        = 250_000
	MinInterval         = 3 * time.Second
)

// These sentinel errors are stable machine-readable admission failures.
var (
	ErrEmptyPart     = errors.New("EMPTY_PART")
	ErrPartTooLarge  = errors.New("PART_TOO_LARGE")
	ErrBackwardClock = errors.New("BACKWARD_CLOCK")
)

type Decision struct {
	Allowed    bool
	RetryAfter time.Duration
}

// Gate admits at most one serialized part per three seconds. The zero value is
// usable and starts its initial cooldown on the first TryAcquire call. A Gate
// must not be copied after first use. It never retains the part's contents.
type Gate struct {
	mu          sync.Mutex
	now         func() time.Time
	initialized bool
	highWater   time.Time
	nextAllowed time.Time
}

// New starts the initial three-second cooldown immediately using time.Now's
// monotonic clock. The integration must construct it AFTER acquiring exclusive
// device ownership, so waiting for ownership cannot spend the startup cooldown.
func New() *Gate { return newWithClock(time.Now) }

func newWithClock(now func() time.Time) *Gate {
	g := &Gate{now: now}
	g.initialize(now())
	return g
}

func (g *Gate) initialize(now time.Time) {
	g.initialized = true
	g.highWater = now
	g.nextAllowed = now.Add(MinInterval)
}

// TryAcquire only authorizes the next handoff; it does not wait, schedule work,
// or reserve a future slot. Callers count the final serialized bytes, including
// envelope/encoding overhead, by passing that complete part. An accepted part
// consumes its slot permanently even if a later handoff fails or is canceled.
//
// No idle time accumulates credit. At most twenty parts of 250,000 bytes can be
// admitted in any rolling interval (t-60s, t], totaling at most 5,000,000 bytes.
// RetryAfter is advisory: competing calls still share the same admission slot.
func (g *Gate) TryAcquire(serializedPart []byte) (Decision, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.now == nil {
		g.now = time.Now
	}
	now := g.now()
	if !g.initialized {
		g.initialize(now)
	}
	if now.Before(g.highWater) {
		retryAt := g.highWater
		if g.nextAllowed.After(retryAt) {
			retryAt = g.nextAllowed
		}
		return Decision{RetryAfter: retryAt.Sub(now)}, ErrBackwardClock
	}
	g.highWater = now
	if len(serializedPart) == 0 {
		return Decision{}, ErrEmptyPart
	}
	if len(serializedPart) > MaxPartBytes {
		return Decision{}, ErrPartTooLarge
	}
	if now.Before(g.nextAllowed) {
		return Decision{RetryAfter: g.nextAllowed.Sub(now)}, nil
	}
	g.nextAllowed = now.Add(MinInterval)
	return Decision{Allowed: true}, nil
}
