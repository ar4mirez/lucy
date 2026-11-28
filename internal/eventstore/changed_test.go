package eventstore

import (
	"context"
	"errors"
	"testing"
)

func closed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// Changed is closed by the next commit that appends, and by nothing else: not a transaction that
// rolls back, nor one that appends nothing (LUCY-SPEC-008).
func TestChangedWakesOnAnAppendingCommit(t *testing.T) {
	ctx := context.Background()
	s := openTest(t, "")
	ch := s.Changed()
	if closed(ch) {
		t.Fatal("closed before anything happened")
	}
	if err := s.Update(ctx, func(*Tx) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if closed(ch) {
		t.Fatal("a transaction that appended nothing woke it")
	}
	boom := errors.New("boom")
	err := s.Update(ctx, func(tx *Tx) error {
		if _, err := tx.Append(ctx, "task-1", NoStream, "test", taskCreated{Title: "t", Org: "acme"}); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) || closed(ch) {
		t.Fatalf("a rolled-back append woke it: %v", err)
	}
	if _, err := s.Append(ctx, "task-1", NoStream, "test", taskCreated{Title: "t", Org: "acme"}); err != nil {
		t.Fatal(err)
	}
	if !closed(ch) {
		t.Fatal("a committed append did not wake it")
	}
	if next := s.Changed(); closed(next) {
		t.Fatal("the next channel is closed already")
	}
}
