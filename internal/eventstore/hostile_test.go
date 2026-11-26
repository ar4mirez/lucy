package eventstore

import (
	"context"
	"testing"
)

var hostileStrings = []string{
	`'; DROP TABLE events; --`,
	`" OR "1"="1`,
	`it's`,
	`say "hi"`,
	`back\slash\'\"`,
	`x'); DELETE FROM streams; SELECT ('`,
	`%_*?[]`,
	`ünïcødé 名前 🚀 ‮rtl`,
}

// TestHostileInputIsData appends, reads and claims with stream, actor, consumer and payload values
// built to break out of a SQL string, and checks they come back exactly as given and leave the
// schema and the log untouched (LUCY-EVT-005: values are bound, never spliced into SQL).
func TestHostileInputIsData(t *testing.T) {
	ctx := context.Background()
	s := openTest(t, "", &tally{})
	schema := schemaOf(t, s.writer)

	for i, h := range hostileStrings {
		evs, err := s.Append(ctx, h, NoStream, h, taskCreated{Title: h, Org: h}, freeform{Fields: map[string]string{h: h}})
		if err != nil {
			t.Fatalf("append %q: %v", h, err)
		}
		if len(evs) != 2 || evs[0].Stream != h || evs[0].Actor != h {
			t.Fatalf("append %q returned %+v", h, evs)
		}

		got, err := s.ReadStream(ctx, h, 0, 0)
		if err != nil || len(got) != 2 {
			t.Fatalf("read stream %q = %d events, %v", h, len(got), err)
		}
		if got[0].Stream != h || got[0].Actor != h {
			t.Fatalf("stream %q read back as stream %q actor %q", h, got[0].Stream, got[0].Actor)
		}
		tc, err := DecodeAs[taskCreated](testRegistry(t), got[0])
		if err != nil || tc.Title != h || tc.Org != h {
			t.Fatalf("payload %q read back as %+v, %v", h, tc, err)
		}
		ff, err := DecodeAs[freeform](testRegistry(t), got[1])
		if err != nil || len(ff.Fields) != 1 || ff.Fields[h] != h {
			t.Fatalf("map payload %q read back as %+v, %v", h, ff, err)
		}
		if v, err := s.StreamVersion(ctx, h); err != nil || v != 2 {
			t.Fatalf("stream version of %q = %d, %v", h, v, err)
		}

		l, err := s.Claim(ctx, h)
		if err != nil || l.Consumer != h || l.Generation != 1 {
			t.Fatalf("claim %q = %+v, %v", h, l, err)
		}
		if err := s.Ack(ctx, h, evs[1].Seq, l.Generation); err != nil {
			t.Fatalf("ack %q: %v", h, err)
		}
		if p, err := s.Pending(ctx, h, 0); err != nil || len(p) != 0 {
			t.Fatalf("pending for %q = %d, %v", h, len(p), err)
		}
		if head, err := s.Head(ctx); err != nil || head != int64(2*(i+1)) {
			t.Fatalf("head after %q = %d, %v; want %d", h, head, err, 2*(i+1))
		}
	}

	if got := schemaOf(t, s.writer); got != schema {
		t.Fatalf("schema changed:\n%s\nwant\n%s", got, schema)
	}
	all, err := s.ReadAfter(ctx, 0, 0)
	if err != nil || len(all) != 2*len(hostileStrings) {
		t.Fatalf("log holds %d events, %v; want %d", len(all), err, 2*len(hostileStrings))
	}
	if rows := tallyRows(t, s); len(rows) != len(hostileStrings) {
		t.Fatalf("projection has %d streams, want %d", len(rows), len(hostileStrings))
	}
}
