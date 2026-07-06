package actioncable

import "time"

// PeriodicTimer models a channel's periodic timer (Channel#periodically). It is
// driven by a deterministic virtual clock via [Scheduler.Advance] rather than by
// wall-clock goroutines, so tests are reproducible and nothing leaks.
type PeriodicTimer struct {
	interval time.Duration
	fn       func()
	elapsed  time.Duration
}

// Scheduler is a deterministic virtual clock holding a channel's (and a
// connection's) periodic timers. Advancing it fires every timer that has come
// due; it never spawns a goroutine.
type Scheduler struct {
	timers []*PeriodicTimer
}

// Every registers fn to run once per interval and returns its handle. A
// non-positive interval registers an inert timer that never fires.
func (s *Scheduler) Every(interval time.Duration, fn func()) *PeriodicTimer {
	t := &PeriodicTimer{interval: interval, fn: fn}
	s.timers = append(s.timers, t)
	return t
}

// Advance moves the virtual clock forward by d, firing each due timer once per
// whole interval elapsed.
func (s *Scheduler) Advance(d time.Duration) {
	for _, t := range s.timers {
		if t.interval <= 0 {
			continue
		}
		t.elapsed += d
		for t.elapsed >= t.interval {
			t.elapsed -= t.interval
			t.fn()
		}
	}
}

// Remove detaches a timer so it no longer fires.
func (s *Scheduler) Remove(t *PeriodicTimer) {
	for i, x := range s.timers {
		if x == t {
			s.timers = append(s.timers[:i], s.timers[i+1:]...)
			return
		}
	}
}
