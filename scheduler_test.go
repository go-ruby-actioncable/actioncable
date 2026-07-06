package actioncable

import (
	"testing"
	"time"
)

func TestScheduler_FiresOncePerInterval(t *testing.T) {
	s := &Scheduler{}
	n := 0
	s.Every(10*time.Millisecond, func() { n++ })
	s.Advance(5 * time.Millisecond)
	if n != 0 {
		t.Fatalf("fired early: %d", n)
	}
	s.Advance(25 * time.Millisecond) // total 30ms -> 3 fires
	if n != 3 {
		t.Fatalf("want 3 fires, got %d", n)
	}
}

func TestScheduler_ZeroIntervalInert(t *testing.T) {
	s := &Scheduler{}
	fired := false
	s.Every(0, func() { fired = true })
	s.Advance(time.Hour)
	if fired {
		t.Fatal("zero-interval timer must never fire")
	}
}

func TestScheduler_Remove(t *testing.T) {
	s := &Scheduler{}
	a := s.Every(time.Millisecond, func() {})
	b := s.Every(time.Millisecond, func() {})
	s.Remove(a)
	if len(s.timers) != 1 || s.timers[0] != b {
		t.Fatal("remove of existing timer failed")
	}
	s.Remove(a) // not found -> no-op
	if len(s.timers) != 1 {
		t.Fatal("remove of missing timer changed state")
	}
}
