package eventstore

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

const crashEnv = "LUCY_EVENTSTORE_CRASH_DB"

// TestCrashHelper is the child process of TestCrashKeepsCommittedEvents. It appends until killed,
// printing each committed sequence number after Append returns.
func TestCrashHelper(t *testing.T) {
	path := os.Getenv(crashEnv)
	if path == "" {
		t.Skip("helper process for TestCrashKeepsCommittedEvents")
	}
	s, err := Open(context.Background(), path, Options{Registry: testRegistry(t), Projectors: []Projector{&tally{}}})
	if err != nil {
		fmt.Println("open:", err)
		os.Exit(2)
	}
	for i := 0; ; i++ {
		stream := fmt.Sprintf("s%d", i%3)
		evs, err := s.Append(context.Background(), stream, AnyVersion, "crash", taskNoted{Note: "n", Tokens: i})
		if err != nil {
			fmt.Println("append:", err)
			os.Exit(3)
		}
		fmt.Println(evs[0].Seq)
	}
}

// TestCrashKeepsCommittedEvents kills a writer process mid-stream with SIGKILL and checks that the
// database is intact and holds every event whose append had returned.
func TestCrashKeepsCommittedEvents(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a process")
	}
	path := filepath.Join(t.TempDir(), "lucy", DefaultFile)
	cmd := exec.Command(os.Args[0], "-test.run=^TestCrashHelper$", "-test.count=1")
	cmd.Env = append(os.Environ(), crashEnv+"="+path)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var last int64
	sc := bufio.NewScanner(out)
	deadline := time.After(60 * time.Second)
	for last < 200 {
		select {
		case <-deadline:
			cmd.Process.Kill() //nolint:errcheck // already failing
			t.Fatalf("helper committed only %d events", last)
		default:
		}
		if !sc.Scan() {
			t.Fatalf("helper exited early after seq %d: %v", last, sc.Err())
		}
		seq, err := strconv.ParseInt(sc.Text(), 10, 64)
		if err != nil {
			t.Fatalf("helper said %q", sc.Text())
		}
		last = seq
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	cmd.Wait() //nolint:errcheck // killed on purpose

	s := openTest(t, path, &tally{})
	ctx := context.Background()
	var integrity string
	if err := s.writer.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatalf("integrity_check = %q, %v", integrity, err)
	}
	h, err := s.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if h < last {
		t.Fatalf("head after crash = %d, but seq %d had committed", h, last)
	}
	var total int64
	for _, n := range tallyRows(t, s) {
		total += n
	}
	if total != h {
		t.Fatalf("projection counts %d events, log has %d", total, h)
	}
}
