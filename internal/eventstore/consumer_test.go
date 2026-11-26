package eventstore

import (
	"context"
	"errors"
	"testing"
)

func TestConsumerCursorAndGenerations(t *testing.T) {
	s := openTest(t, "")
	ctx := context.Background()
	mustAppend(t, s, "a", NoStream, taskCreated{}, taskNoted{}, taskNoted{})

	if _, err := s.Pending(ctx, "sup", 0); !errors.Is(err, ErrUnknownConsumer) {
		t.Fatalf("Pending before claim = %v", err)
	}
	if err := s.Ack(ctx, "sup", 1, 1); !errors.Is(err, ErrUnknownConsumer) {
		t.Fatalf("Ack before claim = %v", err)
	}

	l1, err := s.Claim(ctx, "sup")
	if err != nil || l1.Generation != 1 || l1.Cursor != 0 {
		t.Fatalf("first claim = %+v, %v", l1, err)
	}
	pend, _ := s.Pending(ctx, "sup", 0)
	if len(pend) != 3 {
		t.Fatalf("pending = %d, want 3", len(pend))
	}
	if err := s.Ack(ctx, "sup", 2, l1.Generation); err != nil {
		t.Fatal(err)
	}
	pend, _ = s.Pending(ctx, "sup", 0)
	if len(pend) != 1 || pend[0].Seq != 3 {
		t.Fatalf("pending after ack 2 = %+v", pend)
	}
	// Repeated or lower acks are no-ops; the cursor never moves back.
	if err := s.Ack(ctx, "sup", 1, l1.Generation); err != nil {
		t.Fatal(err)
	}
	if err := s.Ack(ctx, "sup", 9, l1.Generation); !errors.Is(err, ErrAckBeyondHead) {
		t.Fatalf("ack past head = %v", err)
	}

	// A restart claims a new generation; the old one's ack is refused and the work stays pending.
	l2, err := s.Claim(ctx, "sup")
	if err != nil || l2.Generation != 2 || l2.Cursor != 2 {
		t.Fatalf("second claim = %+v, %v", l2, err)
	}
	if err := s.Ack(ctx, "sup", 3, l1.Generation); !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("stale ack = %v, want ErrStaleGeneration", err)
	}
	pend, _ = s.Pending(ctx, "sup", 0)
	if len(pend) != 1 {
		t.Fatalf("pending after stale ack = %d, want 1", len(pend))
	}
	if err := s.Ack(ctx, "sup", 3, l2.Generation); err != nil {
		t.Fatal(err)
	}
	if pend, _ = s.Pending(ctx, "sup", 0); len(pend) != 0 {
		t.Fatalf("pending after current ack = %d", len(pend))
	}

	// Consumers are independent.
	other, _ := s.Claim(ctx, "other")
	if pend, _ = s.Pending(ctx, "other", 0); len(pend) != 3 || other.Generation != 1 {
		t.Fatalf("other consumer pending = %d, gen %d", len(pend), other.Generation)
	}
	if _, err := s.Claim(ctx, ""); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("empty consumer name = %v", err)
	}
}

func TestAckAndAppendTogether(t *testing.T) {
	s := openTest(t, "")
	ctx := context.Background()
	in := mustAppend(t, s, "inbox", NoStream, taskCreated{Title: "do it"})
	l, _ := s.Claim(ctx, "worker")

	// The work's result and the ack commit together, or neither does.
	err := s.Update(ctx, func(tx *Tx) error {
		if _, err := tx.Append(ctx, "results", NoStream, "worker", taskNoted{Note: "done"}); err != nil {
			return err
		}
		return tx.Ack(ctx, "worker", in[0].Seq, l.Generation+1) // stale: refused
	})
	if !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("err = %v", err)
	}
	if v, _ := s.StreamVersion(ctx, "results"); v != 0 {
		t.Fatal("result committed without its ack")
	}
	err = s.Update(ctx, func(tx *Tx) error {
		if _, err := tx.Append(ctx, "results", NoStream, "worker", taskNoted{Note: "done"}); err != nil {
			return err
		}
		return tx.Ack(ctx, "worker", in[0].Seq, l.Generation)
	})
	if err != nil {
		t.Fatal(err)
	}
	pend, _ := s.Pending(ctx, "worker", 0)
	if len(pend) != 1 || pend[0].Stream != "results" {
		t.Fatalf("pending = %+v: only the new result should remain", pend)
	}
}
