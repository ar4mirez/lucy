package converge

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// fakeLog is a Waker whose changes the test signals.
type fakeLog struct {
	mu sync.Mutex
	ch chan struct{}
}

func (f *fakeLog) Changed() <-chan struct{} {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ch == nil {
		f.ch = make(chan struct{})
	}
	return f.ch
}

func (f *fakeLog) append() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ch != nil {
		close(f.ch)
		f.ch = nil
	}
}

// loop records when it ran and returns what the test scripts.
type loop struct {
	mu   sync.Mutex
	runs []time.Time
	next func(n int) (time.Time, error)
}

func (l *loop) Name() string { return "test" }

func (l *loop) Reconcile(context.Context) (time.Time, error) {
	l.mu.Lock()
	l.runs = append(l.runs, time.Now())
	n := len(l.runs)
	l.mu.Unlock()
	if l.next == nil {
		return time.Time{}, nil
	}
	return l.next(n)
}

func (l *loop) times() []time.Time {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]time.Time(nil), l.runs...)
}

func (l *loop) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.runs)
}

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// start runs l in the bubble until the test ends.
func start(t *testing.T, l *loop, w Waker, o Options) {
	t.Helper()
	o.Log = quiet
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	go Run(ctx, l, w, o)
}

// A loop runs at start, then on every change to the log, coalescing a burst into one run within
// MinInterval, and resyncs on its own when nothing happens.
func TestWokenByTheLog(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w, l := &fakeLog{}, &loop{}
		start(t, l, w, Options{Resync: time.Hour, MinInterval: time.Second})
		synctest.Wait()
		if l.count() != 1 {
			t.Fatalf("runs at start: %d", l.count())
		}
		time.Sleep(2 * time.Second)
		w.append()
		synctest.Wait()
		if l.count() != 2 {
			t.Fatalf("runs after a change: %d", l.count())
		}
		for range 5 { // a burst
			w.append()
			synctest.Wait()
		}
		time.Sleep(time.Second)
		synctest.Wait()
		if l.count() != 3 {
			t.Fatalf("runs after a burst: %d, want one more", l.count())
		}
		time.Sleep(time.Hour)
		synctest.Wait()
		if l.count() != 4 {
			t.Fatalf("runs after the resync: %d", l.count())
		}
	})
}

// A loop runs again when the time it asked for comes, with no change to the log.
func TestRunsAtItsNext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := &loop{next: func(int) (time.Time, error) { return time.Now().Add(10 * time.Second), nil }}
		start(t, l, &fakeLog{}, Options{Resync: time.Hour, MinInterval: time.Second})
		time.Sleep(35 * time.Second)
		synctest.Wait()
		if l.count() != 4 { // at 0, 10, 20 and 30 s
			t.Fatalf("runs: %d, want 4", l.count())
		}
	})
}

// After failures a loop waits a doubling backoff, capped, and a change to the log does not cut
// it short; a success resets it.
func TestBackoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := &fakeLog{}
		fail := true
		var mu sync.Mutex
		l := &loop{next: func(int) (time.Time, error) {
			mu.Lock()
			defer mu.Unlock()
			if fail {
				return time.Time{}, errors.New("forge down")
			}
			return time.Time{}, nil
		}}
		o := Options{Resync: time.Hour, MinInterval: time.Millisecond, MinBackoff: time.Second, MaxBackoff: 4 * time.Second}
		start(t, l, w, o)
		synctest.Wait()
		w.append() // ignored during the backoff
		synctest.Wait()
		if l.count() != 1 {
			t.Fatalf("a change cut the backoff short: %d runs", l.count())
		}
		// Failures at 0, 1, 3, 7, 11, 15 s: waits of 1, 2, 4 and then 4 (the cap).
		time.Sleep(15*time.Second + time.Millisecond)
		synctest.Wait()
		runs := l.times()
		var got []time.Duration
		for _, r := range runs {
			got = append(got, r.Sub(runs[0]).Round(time.Second))
		}
		want := []time.Duration{0, time.Second, 3 * time.Second, 7 * time.Second, 11 * time.Second, 15 * time.Second}
		if len(got) != len(want) {
			t.Fatalf("runs at %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("runs at %v, want %v", got, want)
			}
		}
		mu.Lock()
		fail = false
		mu.Unlock()
		time.Sleep(4 * time.Second)
		synctest.Wait()
		n := l.count()
		w.append() // a success makes events wake it again
		synctest.Wait()
		if l.count() != n+1 {
			t.Fatalf("after a success, a change did not wake it: %d -> %d", n, l.count())
		}
	})
}

func TestBackoffValues(t *testing.T) {
	o := Options{MinBackoff: time.Second, MaxBackoff: 10 * time.Second}
	for failures, want := range map[int]time.Duration{1: time.Second, 2: 2 * time.Second, 3: 4 * time.Second, 4: 8 * time.Second, 5: 10 * time.Second, 50: 10 * time.Second} {
		if got := o.Backoff(failures); got != want {
			t.Errorf("Backoff(%d) = %v, want %v", failures, got, want)
		}
	}
}
