package cli

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestEachAtOnce(t *testing.T) {
	names := []string{"a", "b", "c", "d", "e"}
	var running, peak atomic.Int32
	start := time.Now()
	errs := eachAtOnce(names, 3, func(n string) error {
		now := running.Add(1)
		for p := peak.Load(); now > p && !peak.CompareAndSwap(p, now); p = peak.Load() {
		}
		time.Sleep(100 * time.Millisecond)
		running.Add(-1)
		if n == "b" {
			return errors.New("boom")
		}
		return nil
	})
	if d := time.Since(start); d > 350*time.Millisecond {
		t.Errorf("not run at once: %s", d)
	}
	if p := peak.Load(); p != 3 {
		t.Errorf("peak %d at a time, want the limit 3", p)
	}
	for i, err := range errs {
		if (err != nil) != (names[i] == "b") {
			t.Errorf("%s: %v", names[i], err)
		}
	}
	// limit 1 is one by one.
	peak.Store(0)
	eachAtOnce(names, 1, func(string) error {
		now := running.Add(1)
		for p := peak.Load(); now > p && !peak.CompareAndSwap(p, now); p = peak.Load() {
		}
		time.Sleep(time.Millisecond)
		running.Add(-1)
		return nil
	})
	if p := peak.Load(); p != 1 {
		t.Errorf("limit 1: peak %d", p)
	}
}
