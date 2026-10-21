package transferlimit

import (
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"
)

type fakeClock struct{ now time.Time }

func clockAtStart() *fakeClock       { return &fakeClock{time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)} }
func (c *fakeClock) read() time.Time { return c.now }

func TestStartupAndExactInterval(t *testing.T) {
	clock := clockAtStart()
	gate := newWithClock(clock.read)
	part := []byte{1}
	decision, err := gate.TryAcquire(part)
	if err != nil || decision.Allowed || decision.RetryAfter != 3*time.Second {
		t.Fatalf("startup: %+v, %v", decision, err)
	}
	clock.now = clock.now.Add(MinInterval - time.Nanosecond)
	decision, err = gate.TryAcquire(part)
	if err != nil || decision.Allowed || decision.RetryAfter != time.Nanosecond {
		t.Fatalf("early boundary: %+v, %v", decision, err)
	}
	clock.now = clock.now.Add(time.Nanosecond)
	decision, err = gate.TryAcquire(part)
	if err != nil || !decision.Allowed || decision.RetryAfter != 0 {
		t.Fatalf("exact boundary: %+v, %v", decision, err)
	}
	decision, err = gate.TryAcquire(part)
	if err != nil || decision.Allowed || decision.RetryAfter != MinInterval {
		t.Fatalf("same instant reused: %+v, %v", decision, err)
	}
}

func TestSerializedSizeBoundariesAndRejectedPartsDoNotConsumeSlot(t *testing.T) {
	for _, size := range []int{0, 249_999, 250_000, 250_001} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			clock := clockAtStart()
			gate := newWithClock(clock.read)
			clock.now = clock.now.Add(MinInterval)
			decision, err := gate.TryAcquire(make([]byte, size))
			switch size {
			case 0:
				if !errors.Is(err, ErrEmptyPart) || decision.Allowed {
					t.Fatalf("empty: %+v, %v", decision, err)
				}
			case 250_001:
				if !errors.Is(err, ErrPartTooLarge) || decision.Allowed {
					t.Fatalf("oversize: %+v, %v", decision, err)
				}
			default:
				if err != nil || !decision.Allowed {
					t.Fatalf("valid size %d: %+v, %v", size, decision, err)
				}
				return
			}
			decision, err = gate.TryAcquire([]byte{1})
			if err != nil || !decision.Allowed {
				t.Fatalf("rejected size consumed a slot: %+v, %v", decision, err)
			}
		})
	}
}

func TestIdleTimeDoesNotAccumulateBurst(t *testing.T) {
	clock := clockAtStart()
	gate := newWithClock(clock.read)
	clock.now = clock.now.Add(24 * time.Hour)
	for attempt := 0; attempt < 100; attempt++ {
		decision, err := gate.TryAcquire(make([]byte, MaxPartBytes))
		if err != nil || decision.Allowed != (attempt == 0) {
			t.Fatalf("idle attempt %d: %+v, %v", attempt, decision, err)
		}
		if attempt > 0 && decision.RetryAfter != MinInterval {
			t.Fatalf("idle retry: %+v", decision)
		}
	}
}

func TestConcurrentCallsShareOneAdmissionSlot(t *testing.T) {
	clock := clockAtStart()
	gate := newWithClock(clock.read)
	clock.now = clock.now.Add(MinInterval)
	const callers = 64
	results := make(chan Decision, callers)
	errorsFound := make(chan error, callers)
	var group sync.WaitGroup
	for i := 0; i < callers; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			decision, err := gate.TryAcquire([]byte{1})
			results <- decision
			errorsFound <- err
		}()
	}
	group.Wait()
	close(results)
	close(errorsFound)
	allowed := 0
	for decision := range results {
		if decision.Allowed {
			allowed++
		} else if decision.RetryAfter != MinInterval {
			t.Fatal(decision)
		}
	}
	for err := range errorsFound {
		if err != nil {
			t.Fatal(err)
		}
	}
	if allowed != 1 {
		t.Fatalf("concurrent admissions: %d", allowed)
	}
}

func TestRollingMinuteBoundWithVariableSizesAndAttempts(t *testing.T) {
	clock := clockAtStart()
	start := clock.now
	gate := newWithClock(clock.read)
	type event struct {
		at    time.Time
		bytes int
	}
	var accepted []event
	part := make([]byte, MaxPartBytes)
	// Attempts at irregular gaps and multiple calls per instant exercise pacing
	// independently from the byte-sum assertion over actual accepted events.
	for step := 0; step < 1200; step++ {
		clock.now = start.Add(time.Duration(step) * 400 * time.Millisecond)
		if step >= 400 {
			clock.now = clock.now.Add(5 * time.Minute)
		}
		for duplicate := 0; duplicate < 3; duplicate++ {
			size := 1 + (step*97_531+duplicate*13_001)%MaxPartBytes
			if step < 200 {
				size = MaxPartBytes
			}
			decision, err := gate.TryAcquire(part[:size])
			if err != nil {
				t.Fatal(err)
			}
			if decision.Allowed {
				accepted = append(accepted, event{clock.now, size})
			}
		}
	}
	if len(accepted) < 100 {
		t.Fatalf("insufficient admitted events: %d", len(accepted))
	}
	for index, current := range accepted {
		total, count := 0, 0
		for _, previous := range accepted[:index+1] {
			if previous.at.After(current.at.Add(-time.Minute)) {
				total += previous.bytes
				count++
			}
		}
		if total > LimitBytesPerMinute || count > 20 {
			t.Fatalf("rolling minute exceeded at %v: %d bytes, %d parts", current.at, total, count)
		}
		if index > 0 && current.at.Sub(accepted[index-1].at) < MinInterval {
			t.Fatal("adjacent parts admitted too quickly")
		}
	}
}

func TestExactMinuteCanReachButNotExceedDecimalLimit(t *testing.T) {
	clock := clockAtStart()
	start := clock.now
	gate := newWithClock(clock.read)
	part := make([]byte, MaxPartBytes)
	total := 0
	for i := 1; i <= 20; i++ {
		clock.now = start.Add(time.Duration(i) * MinInterval)
		decision, err := gate.TryAcquire(part)
		if err != nil || !decision.Allowed {
			t.Fatalf("slot %d: %+v, %v", i, decision, err)
		}
		total += len(part)
	}
	if total != LimitBytesPerMinute {
		t.Fatalf("decimal minute total: %d", total)
	}
	decision, err := gate.TryAcquire(part)
	if err != nil || decision.Allowed {
		t.Fatalf("extra same-minute slot: %+v, %v", decision, err)
	}
	clock.now = clock.now.Add(MinInterval)
	decision, err = gate.TryAcquire(part)
	if err != nil || !decision.Allowed {
		t.Fatalf("next rolling slot: %+v, %v", decision, err)
	}
	// The first event at t=3s is now outside (t-60s,t], leaving twenty events.
}

func TestBackwardClockFailsClosedUntilRecovery(t *testing.T) {
	clock := clockAtStart()
	start := clock.now
	gate := newWithClock(clock.read)
	clock.now = start.Add(MinInterval)
	if decision, err := gate.TryAcquire([]byte{1}); err != nil || !decision.Allowed {
		t.Fatalf("first: %+v, %v", decision, err)
	}
	clock.now = start.Add(5 * time.Second)
	if decision, err := gate.TryAcquire([]byte{1}); err != nil || decision.Allowed {
		t.Fatalf("cooldown: %+v, %v", decision, err)
	}
	clock.now = start.Add(4 * time.Second)
	decision, err := gate.TryAcquire([]byte{1})
	if !errors.Is(err, ErrBackwardClock) || decision.Allowed || decision.RetryAfter != 2*time.Second {
		t.Fatalf("backward: %+v, %v", decision, err)
	}
	clock.now = start.Add(5 * time.Second)
	decision, err = gate.TryAcquire([]byte{1})
	if err != nil || decision.Allowed || decision.RetryAfter != time.Second {
		t.Fatalf("recovered cooldown: %+v, %v", decision, err)
	}
	clock.now = start.Add(6 * time.Second)
	decision, err = gate.TryAcquire([]byte{1})
	if err != nil || !decision.Allowed {
		t.Fatalf("recovered slot: %+v, %v", decision, err)
	}
}

func TestSequentialOwnersEachStartWithCooldown(t *testing.T) {
	clock := clockAtStart()
	old := newWithClock(clock.read)
	clock.now = clock.now.Add(MinInterval)
	if decision, err := old.TryAcquire([]byte{1}); err != nil || !decision.Allowed {
		t.Fatalf("old owner: %+v, %v", decision, err)
	}
	newOwner := newWithClock(clock.read)
	decision, err := newOwner.TryAcquire([]byte{1})
	if err != nil || decision.Allowed || decision.RetryAfter != MinInterval {
		t.Fatalf("new owner startup: %+v, %v", decision, err)
	}
	clock.now = clock.now.Add(MinInterval - time.Nanosecond)
	decision, err = newOwner.TryAcquire([]byte{1})
	if err != nil || decision.Allowed {
		t.Fatalf("new owner early: %+v, %v", decision, err)
	}
	clock.now = clock.now.Add(time.Nanosecond)
	decision, err = newOwner.TryAcquire([]byte{1})
	if err != nil || !decision.Allowed {
		t.Fatalf("new owner ready: %+v, %v", decision, err)
	}
}

func TestZeroGateStartsCooldownOnFirstUse(t *testing.T) {
	var gate Gate
	decision, err := gate.TryAcquire([]byte{1})
	if err != nil || decision.Allowed || decision.RetryAfter != MinInterval {
		t.Fatalf("zero gate: %+v, %v", decision, err)
	}
}

func TestRollbackAfterRejectedPartKeepsPositiveRecoveryDelay(t *testing.T) {
	clock := clockAtStart()
	start := clock.now
	gate := newWithClock(clock.read)
	clock.now = start.Add(time.Hour)
	if _, err := gate.TryAcquire(nil); !errors.Is(err, ErrEmptyPart) {
		t.Fatal(err)
	}
	clock.now = clock.now.Add(-time.Second)
	decision, err := gate.TryAcquire([]byte{1})
	if !errors.Is(err, ErrBackwardClock) || decision.Allowed || decision.RetryAfter != time.Second {
		t.Fatalf("rollback after invalid input: %+v, %v", decision, err)
	}
	clock.now = start.Add(time.Hour)
	decision, err = gate.TryAcquire([]byte{1})
	if err != nil || !decision.Allowed {
		t.Fatalf("recovery after invalid input: %+v, %v", decision, err)
	}
}

func TestFinalSerializedUTF8EnvelopeCountsBytes(t *testing.T) {
	clock := clockAtStart()
	gate := newWithClock(clock.read)
	clock.now = clock.now.Add(MinInterval)
	part := make([]byte, MaxPartBytes+1)
	copy(part, []byte("{\"данные\":\""))
	decision, err := gate.TryAcquire(part)
	if !errors.Is(err, ErrPartTooLarge) || decision.Allowed {
		t.Fatalf("serialized overhead must count: %+v, %v", decision, err)
	}
	decision, err = gate.TryAcquire(part[:MaxPartBytes])
	if err != nil || !decision.Allowed {
		t.Fatalf("exact byte limit: %+v, %v", decision, err)
	}
}
