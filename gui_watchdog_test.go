package main

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestWatchUIThreadReportsStallOnce(t *testing.T) {
	var beat atomic.Int64
	var mu sync.Mutex
	var reports []time.Duration
	report := func(stalled time.Duration, stacks []byte) {
		mu.Lock()
		defer mu.Unlock()
		if stalled > 0 && len(stacks) == 0 {
			t.Error("a stall report must carry stacks")
		}
		reports = append(reports, stalled)
	}
	done := make(chan struct{})
	defer close(done)
	beat.Store(time.Now().Add(-time.Hour).UnixNano()) // long silent
	go watchUIThread(&beat, 5*time.Millisecond, 50*time.Millisecond, report, done)

	waitFor := func(n int) []time.Duration {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			mu.Lock()
			got := append([]time.Duration(nil), reports...)
			mu.Unlock()
			if len(got) >= n {
				return got
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatalf("got %d reports, want %d", len(reports), n)
		return nil
	}
	if got := waitFor(1); got[0] <= 0 {
		t.Fatalf("first report %v, want a stall", got[0])
	}
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	if len(reports) != 1 {
		t.Fatalf("a continuing stall was reported %d times, want once", len(reports))
	}
	mu.Unlock()

	// Beating again reports recovery.
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
				beat.Store(time.Now().UnixNano())
				time.Sleep(time.Millisecond)
			}
		}
	}()
	defer close(stop)
	if got := waitFor(2); got[1] != 0 {
		t.Fatalf("second report %v, want recovery (0)", got[1])
	}
}
