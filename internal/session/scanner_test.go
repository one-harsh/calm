// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

//go:build mocks

package session

import (
	"context"
	"errors"
	"math"
	"sync"
	"testing"
	"time"

	logging "github.com/one-harsh/context-logging"
	"github.com/stretchr/testify/mock"

	"github.com/one-harsh/calm/internal/db"
)

// runScanner spins the scanner in a goroutine and returns a cancel func +
// a done channel; tests cancel when they've observed the assertions they
// need and wait on done to make sure Run actually returned.
func runScanner(t *testing.T, scanner *Scanner) (context.CancelFunc, <-chan struct{}) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = scanner.Run(ctx)
		close(done)
	}()
	return cancel, done
}

func waitForDone(t *testing.T, done <-chan struct{}, within time.Duration) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(within):
		t.Fatalf("scanner did not exit within %v", within)
	}
}

// ---------- disabled ----------

func TestScanner_DisabledIntervalReturnsImmediately(t *testing.T) {
	source := NewMockexpirySource(t)
	scanner := NewScanner(source, ScannerConfig{Interval: 0}, logging.Nop())

	done := make(chan struct{})
	go func() {
		_ = scanner.Run(context.Background())
		close(done)
	}()
	waitForDone(t, done, 200*time.Millisecond)
}

// ---------- tick + delete orchestration ----------

func TestScanner_TickInvokesScanExpired(t *testing.T) {
	source := NewMockexpirySource(t)
	called := make(chan struct{}, 1)
	source.EXPECT().ScanExpired(mock.Anything, mock.Anything).
		RunAndReturn(func(context.Context, time.Time) ([]db.SessionRef, error) {
			select {
			case called <- struct{}{}:
			default:
			}
			return nil, nil
		}).Maybe()

	scanner := NewScanner(source, ScannerConfig{Interval: time.Millisecond}, logging.Nop())
	cancel, done := runScanner(t, scanner)
	defer waitForDone(t, done, 200*time.Millisecond)
	defer cancel()

	select {
	case <-called:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("ScanExpired never invoked within 200ms (interval=1ms)")
	}
}

func TestScanner_DeletesEachReturnedRef(t *testing.T) {
	source := NewMockexpirySource(t)
	refs := []db.SessionRef{
		{ID: 1, Namespace: "ns-a"},
		{ID: 2, Namespace: "ns-a"},
		{ID: 3, Namespace: "ns-b"},
	}

	// Return the refs once, then empty on subsequent ticks.
	var scanCount int
	var mu sync.Mutex
	source.EXPECT().ScanExpired(mock.Anything, mock.Anything).
		RunAndReturn(func(context.Context, time.Time) ([]db.SessionRef, error) {
			mu.Lock()
			defer mu.Unlock()
			scanCount++
			if scanCount == 1 {
				return refs, nil
			}
			return nil, nil
		}).Maybe()

	deleted := make(chan db.SessionRef, len(refs))
	source.EXPECT().DeleteByID(mock.Anything, mock.Anything, mock.Anything).
		RunAndReturn(func(_ context.Context, ns string, id int64) (db.DeleteSessionResult, error) {
			deleted <- db.SessionRef{ID: id, Namespace: ns}
			return db.DeleteSessionResult{ID: id}, nil
		}).Times(len(refs))

	scanner := NewScanner(source, ScannerConfig{Interval: time.Millisecond}, logging.Nop())
	cancel, done := runScanner(t, scanner)
	defer waitForDone(t, done, 500*time.Millisecond)
	defer cancel()

	seen := map[db.SessionRef]bool{}
	for range refs {
		select {
		case ref := <-deleted:
			seen[ref] = true
		case <-time.After(500 * time.Millisecond):
			t.Fatalf("only saw %d/%d deletes", len(seen), len(refs))
		}
	}
	for _, r := range refs {
		if !seen[r] {
			t.Errorf("ref %+v never deleted", r)
		}
	}
}

// ---------- error handling ----------

func TestScanner_ScanExpiredErrorContinuesLoop(t *testing.T) {
	source := NewMockexpirySource(t)

	var calls int
	var mu sync.Mutex
	source.EXPECT().ScanExpired(mock.Anything, mock.Anything).
		RunAndReturn(func(context.Context, time.Time) ([]db.SessionRef, error) {
			mu.Lock()
			defer mu.Unlock()
			calls++
			if calls == 1 {
				return nil, errors.New("simulated scan failure")
			}
			return []db.SessionRef{{ID: 1, Namespace: "ns-a"}}, nil
		}).Maybe()

	deleted := make(chan struct{}, 1)
	source.EXPECT().DeleteByID(mock.Anything, "ns-a", int64(1)).
		RunAndReturn(func(context.Context, string, int64) (db.DeleteSessionResult, error) {
			select {
			case deleted <- struct{}{}:
			default:
			}
			return db.DeleteSessionResult{ID: 1}, nil
		}).Maybe()

	scanner := NewScanner(source, ScannerConfig{Interval: time.Millisecond}, logging.Nop())
	cancel, done := runScanner(t, scanner)
	defer waitForDone(t, done, 500*time.Millisecond)
	defer cancel()

	select {
	case <-deleted:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("DeleteByID never invoked — scanner aborted after first ScanExpired error")
	}
}

func TestScanner_PerRefDeleteErrorContinuesWithRemainingRefs(t *testing.T) {
	source := NewMockexpirySource(t)
	refs := []db.SessionRef{
		{ID: 1, Namespace: "ns-a"},
		{ID: 2, Namespace: "ns-a"},
		{ID: 3, Namespace: "ns-a"},
	}
	var scanCount int
	var mu sync.Mutex
	source.EXPECT().ScanExpired(mock.Anything, mock.Anything).
		RunAndReturn(func(context.Context, time.Time) ([]db.SessionRef, error) {
			mu.Lock()
			defer mu.Unlock()
			scanCount++
			if scanCount == 1 {
				return refs, nil
			}
			return nil, nil
		}).Maybe()

	deleteCalls := make(chan int64, len(refs))
	source.EXPECT().DeleteByID(mock.Anything, mock.Anything, mock.Anything).
		RunAndReturn(func(_ context.Context, _ string, id int64) (db.DeleteSessionResult, error) {
			deleteCalls <- id
			if id == 2 {
				return db.DeleteSessionResult{}, errors.New("simulated delete failure")
			}
			return db.DeleteSessionResult{ID: id}, nil
		}).Times(len(refs))

	scanner := NewScanner(source, ScannerConfig{Interval: time.Millisecond}, logging.Nop())
	cancel, done := runScanner(t, scanner)
	defer waitForDone(t, done, 500*time.Millisecond)
	defer cancel()

	saw := map[int64]bool{}
	for range refs {
		select {
		case id := <-deleteCalls:
			saw[id] = true
		case <-time.After(500 * time.Millisecond):
			t.Fatalf("only saw %d/%d DeleteByID calls", len(saw), len(refs))
		}
	}
	for _, want := range []int64{1, 2, 3} {
		if !saw[want] {
			t.Errorf("DeleteByID(%d) skipped — per-ref failure aborted the batch", want)
		}
	}
}

func TestScanner_NotFoundDuringDeleteIsSilent(t *testing.T) {
	source := NewMockexpirySource(t)
	var scanCount int
	var mu sync.Mutex
	source.EXPECT().ScanExpired(mock.Anything, mock.Anything).
		RunAndReturn(func(context.Context, time.Time) ([]db.SessionRef, error) {
			mu.Lock()
			defer mu.Unlock()
			scanCount++
			if scanCount == 1 {
				return []db.SessionRef{{ID: 1, Namespace: "ns-a"}}, nil
			}
			return nil, nil
		}).Maybe()

	deleted := make(chan struct{}, 1)
	source.EXPECT().DeleteByID(mock.Anything, "ns-a", int64(1)).
		RunAndReturn(func(context.Context, string, int64) (db.DeleteSessionResult, error) {
			select {
			case deleted <- struct{}{}:
			default:
			}
			return db.DeleteSessionResult{}, db.ErrSessionNotFound
		}).Once()

	scanner := NewScanner(source, ScannerConfig{Interval: time.Millisecond}, logging.Nop())
	cancel, done := runScanner(t, scanner)
	defer waitForDone(t, done, 500*time.Millisecond)
	defer cancel()

	select {
	case <-deleted:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("DeleteByID never invoked")
	}
	// Loop must still be alive — let one more tick happen, scanner will call
	// ScanExpired again and return nil (handled by Maybe()). Cancel ends test.
	time.Sleep(20 * time.Millisecond)
}

func TestScanner_DeleteReturnsCancellationErrorExitsBatchEarly(t *testing.T) {
	// When ctx is cancelled mid-batch, the in-flight Delete propagates
	// context.Canceled/DeadlineExceeded back. scanOnce must treat that as
	// a hard stop — not a per-row error to log-and-continue — because
	// every subsequent Delete in the batch would also fail the same way,
	// flooding logs and burning a closing-ctx budget for no reaping work.
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"canceled", context.Canceled},
		{"deadline exceeded", context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := NewMockexpirySource(t)
			refs := []db.SessionRef{
				{ID: 1, Namespace: "ns-a"},
				{ID: 2, Namespace: "ns-a"},
				{ID: 3, Namespace: "ns-a"},
				{ID: 4, Namespace: "ns-a"},
				{ID: 5, Namespace: "ns-a"},
			}

			var scanCount int
			var scanMu sync.Mutex
			source.EXPECT().ScanExpired(mock.Anything, mock.Anything).
				RunAndReturn(func(context.Context, time.Time) ([]db.SessionRef, error) {
					scanMu.Lock()
					defer scanMu.Unlock()
					scanCount++
					if scanCount == 1 {
						return refs, nil
					}
					return nil, nil
				}).Maybe()

			// First Delete succeeds, second returns the cancellation error;
			// subsequent refs MUST NOT see a Delete call.
			var deleteCalls atomicInt
			source.EXPECT().DeleteByID(mock.Anything, mock.Anything, mock.Anything).
				RunAndReturn(func(_ context.Context, _ string, id int64) (db.DeleteSessionResult, error) {
					n := deleteCalls.IncrementAndLoad()
					if n == 2 {
						return db.DeleteSessionResult{}, tc.err
					}
					if n > 2 {
						t.Errorf("DeleteByID called for id=%d after %v — must early-exit", id, tc.err)
					}
					return db.DeleteSessionResult{ID: id}, nil
				}).Maybe()

			scanner := NewScanner(source, ScannerConfig{Interval: time.Millisecond}, logging.Nop())
			cancel, done := runScanner(t, scanner)

			// Give the scanner enough time to attempt all 5 deletes if the
			// early-exit isn't working; we expect to see exactly 2.
			deadline := time.Now().Add(300 * time.Millisecond)
			for time.Now().Before(deadline) && deleteCalls.Load() < 2 {
				time.Sleep(5 * time.Millisecond)
			}
			// Pause a beat to catch any erroneous extra calls.
			time.Sleep(50 * time.Millisecond)

			cancel()
			waitForDone(t, done, 200*time.Millisecond)

			if got := deleteCalls.Load(); got != 2 {
				t.Errorf("DeleteByID call count = %d; want 2 (one success + one cancellation → early exit)", got)
			}
		})
	}
}

// ---------- lifecycle ----------

func TestScanner_ContextCancelStopsLoopBeforeFirstTick(t *testing.T) {
	source := NewMockexpirySource(t)
	// Long interval so the first tick never fires within the test budget.
	scanner := NewScanner(source, ScannerConfig{Interval: time.Hour}, logging.Nop())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = scanner.Run(ctx)
		close(done)
	}()
	cancel()
	waitForDone(t, done, 100*time.Millisecond)
	// No ScanExpired expectation — mock cleanup verifies it was never called.
}

// ---------- jitter ----------

func TestScanner_ZeroJitterProducesFixedCadence(t *testing.T) {
	source := NewMockexpirySource(t)
	const interval = 5 * time.Millisecond
	const ticks = 6

	timestamps := make(chan time.Time, ticks)
	source.EXPECT().ScanExpired(mock.Anything, mock.Anything).
		RunAndReturn(func(context.Context, time.Time) ([]db.SessionRef, error) {
			select {
			case timestamps <- time.Now():
			default:
			}
			return nil, nil
		}).Maybe()

	scanner := NewScanner(source, ScannerConfig{Interval: interval, Jitter: 0}, logging.Nop())
	cancel, done := runScanner(t, scanner)

	collected := collectTimestamps(t, timestamps, ticks, 500*time.Millisecond)
	cancel()
	waitForDone(t, done, 200*time.Millisecond)

	gaps := interTickGaps(collected)
	for _, g := range gaps {
		// Allow a generous +/- on top of interval to absorb scheduler noise;
		// the assertion is that gaps cluster, not that they're exact.
		if g < interval-2*time.Millisecond || g > interval+5*time.Millisecond {
			t.Errorf("zero-jitter gap = %v; want ~%v ±tolerance", g, interval)
		}
	}
}

func TestScanner_NonZeroJitterProducesVaryingDelays(t *testing.T) {
	source := NewMockexpirySource(t)
	const interval = 5 * time.Millisecond
	const jitter = 4 * time.Millisecond
	const ticks = 8

	timestamps := make(chan time.Time, ticks)
	source.EXPECT().ScanExpired(mock.Anything, mock.Anything).
		RunAndReturn(func(context.Context, time.Time) ([]db.SessionRef, error) {
			select {
			case timestamps <- time.Now():
			default:
			}
			return nil, nil
		}).Maybe()

	scanner := NewScanner(source, ScannerConfig{Interval: interval, Jitter: jitter}, logging.Nop())
	cancel, done := runScanner(t, scanner)

	collected := collectTimestamps(t, timestamps, ticks, 1*time.Second)
	cancel()
	waitForDone(t, done, 200*time.Millisecond)

	gaps := interTickGaps(collected)
	if stddev(gaps) == 0 {
		t.Errorf("inter-tick gaps have zero variance — jitter not applied: %v", gaps)
	}
	// All gaps must fall within the configured range (with scheduler slop).
	for _, g := range gaps {
		if g < interval-jitter-2*time.Millisecond {
			t.Errorf("gap %v below configured minimum (%v)", g, interval-jitter)
		}
		if g > interval+jitter+10*time.Millisecond {
			t.Errorf("gap %v above configured maximum (%v)", g, interval+jitter)
		}
	}
}

// ---------- helpers ----------

func collectTimestamps(t *testing.T, ch <-chan time.Time, n int, within time.Duration) []time.Time {
	t.Helper()
	out := make([]time.Time, 0, n)
	deadline := time.After(within)
	for len(out) < n {
		select {
		case ts := <-ch:
			out = append(out, ts)
		case <-deadline:
			t.Fatalf("collected only %d/%d timestamps within %v", len(out), n, within)
		}
	}
	return out
}

func interTickGaps(ts []time.Time) []time.Duration {
	gaps := make([]time.Duration, 0, len(ts)-1)
	for i := 1; i < len(ts); i++ {
		gaps = append(gaps, ts[i].Sub(ts[i-1]))
	}
	return gaps
}

func stddev(gaps []time.Duration) float64 {
	if len(gaps) == 0 {
		return 0
	}
	var sum float64
	for _, g := range gaps {
		sum += float64(g)
	}
	mean := sum / float64(len(gaps))
	var sq float64
	for _, g := range gaps {
		d := float64(g) - mean
		sq += d * d
	}
	return math.Sqrt(sq / float64(len(gaps)))
}

// atomicInt is a tiny helper because sync/atomic.Int64 would force an
// import and we only need integer ops.
type atomicInt struct {
	mu sync.Mutex
	n  int
}

func (a *atomicInt) Add(d int) {
	a.mu.Lock()
	a.n += d
	a.mu.Unlock()
}

func (a *atomicInt) Load() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.n
}

func (a *atomicInt) IncrementAndLoad() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.n++
	return a.n
}
