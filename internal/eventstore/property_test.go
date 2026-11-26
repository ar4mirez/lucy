package eventstore

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"reflect"
	"sync"
	"testing"
)

// TestPropertyConcurrentAppends runs writers that append to a few shared streams with random
// expected versions and random batch sizes, then checks the log's invariants:
//
//   - every successful append is in the log exactly once, with the sequence numbers it was given;
//   - sequence numbers increase in commit order without gaps;
//   - each stream's versions are 1..n with no gaps, in sequence order;
//   - conflicts and refused appends write nothing;
//   - the incrementally maintained projection equals a rebuild from the log, and a full replay
//     returns exactly the events the writers saw committed.
func TestPropertyConcurrentAppends(t *testing.T) {
	for seed := uint64(1); seed <= 4; seed++ {
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) { checkConcurrentAppends(t, seed) })
	}
}

func checkConcurrentAppends(t *testing.T, seed uint64) {
	s := openTest(t, "", &tally{})
	ctx := context.Background()
	streams := []string{"task-a", "task-b", "task-c"}
	const writers, opsPerWriter = 6, 25

	var mu sync.Mutex
	var committed []Event
	conflicts := 0

	var wg sync.WaitGroup
	for w := range writers {
		wg.Go(func() {
			rng := rand.New(rand.NewPCG(seed, uint64(w)))
			for range opsPerWriter {
				stream := streams[rng.IntN(len(streams))]
				known, err := s.StreamVersion(ctx, stream)
				if err != nil {
					t.Error(err)
					return
				}
				expected := known
				switch rng.IntN(4) {
				case 0:
					expected = AnyVersion
				case 1:
					expected = known + int64(rng.IntN(3)) - 1 // often stale or ahead
					if expected < 0 {
						expected = NoStream
					}
				}
				batch := make([]Payload, 1+rng.IntN(3))
				for i := range batch {
					batch[i] = taskNoted{Note: fmt.Sprintf("w%d", w), Tokens: rng.IntN(100)}
				}
				evs, err := s.Append(ctx, stream, expected, fmt.Sprintf("writer-%d", w), batch...)
				mu.Lock()
				switch {
				case err == nil:
					committed = append(committed, evs...)
				case errors.Is(err, ErrConflict):
					conflicts++
				default:
					t.Errorf("append: %v", err)
				}
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if t.Failed() {
		return
	}

	log, err := s.ReadAfter(ctx, 0, MaxReadLimit)
	if err != nil {
		t.Fatal(err)
	}
	if len(log) != len(committed) {
		t.Fatalf("log has %d events, writers committed %d", len(log), len(committed))
	}
	bySeq := map[int64]Event{}
	for _, e := range committed {
		bySeq[e.Seq] = e
	}
	next := map[string]int64{}
	counts := map[string]int64{}
	for i, e := range log {
		if e.Seq != int64(i+1) {
			t.Fatalf("log[%d].Seq = %d, want %d", i, e.Seq, i+1)
		}
		if !reflect.DeepEqual(bySeq[e.Seq], e) {
			t.Fatalf("seq %d: log has %+v, writer saw %+v", e.Seq, e, bySeq[e.Seq])
		}
		next[e.Stream]++
		if e.StreamVersion != next[e.Stream] {
			t.Fatalf("stream %s: version %d at seq %d, want %d", e.Stream, e.StreamVersion, e.Seq, next[e.Stream])
		}
		counts[e.Stream]++
	}
	for stream, n := range next {
		if v, _ := s.StreamVersion(ctx, stream); v != n {
			t.Fatalf("stream %s version = %d, log says %d", stream, v, n)
		}
	}
	if conflicts == 0 {
		t.Log("no conflicts this seed; the random expected versions should usually produce some")
	}

	incremental := tallyRows(t, s)
	if !reflect.DeepEqual(incremental, counts) {
		t.Fatalf("incremental projection %v != log counts %v", incremental, counts)
	}
	if err := s.Rebuild(ctx, "tally"); err != nil {
		t.Fatal(err)
	}
	if rebuilt := tallyRows(t, s); !reflect.DeepEqual(rebuilt, incremental) {
		t.Fatalf("rebuilt projection %v != incremental %v", rebuilt, incremental)
	}

	var replayed []Event
	if err := s.Replay(ctx, 0, func(e Event) error { replayed = append(replayed, e); return nil }); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(replayed, log) {
		t.Fatal("Replay differs from ReadAfter")
	}
}

// TestPropertyStreamReadsMatchLog checks, over random single-writer histories, that reading a
// stream from any version returns the log's events for that stream in order.
func TestPropertyStreamReadsMatchLog(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 7))
	s := openTest(t, "")
	ctx := context.Background()
	want := map[string][]Event{}
	for range 200 {
		stream := fmt.Sprintf("s%d", rng.IntN(5))
		evs := mustAppend(t, s, stream, int64(len(want[stream])), taskNoted{Tokens: rng.IntN(10)})
		want[stream] = append(want[stream], evs...)
	}
	for stream, evs := range want {
		for from := range len(evs) + 1 {
			got, err := s.ReadStream(ctx, stream, int64(from), 0)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, evs[from:]) && !(len(got) == 0 && len(evs[from:]) == 0) {
				t.Fatalf("ReadStream(%s, %d) = %d events, want %d", stream, from, len(got), len(evs[from:]))
			}
		}
	}
}
