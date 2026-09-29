package log

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

// TestLogPrinterSmoke runs a LogPrinter end-to-end against a real BufferTarget
// and confirms that:
//   - records emitted before Run() are picked up,
//   - records emitted while the printer is idle (waiting on cond.Wait) are
//     also drained,
//   - Stop() unblocks the goroutine and returns,
//   - the printed bytes contain the original messages.
//
// LogPrinter is 100 lines of goroutine + signaling code. This test does not
// pin formatting; it pins that the printer is actually live and drains the
// buffer rather than e.g. spinning forever.
func TestLogPrinterSmoke(t *testing.T) {
	stdout, stderr, restore := redirectStdio(t)
	defer restore()

	bt := NewBufferTarget()
	bt.Emit(Record{Level: Info, Component: Build, Msg: "before-run"})

	p := NewLogPrinter(bt)
	// Force monochrome output so the assertion below isn't sensitive to
	// whether the test runner is attached to a TTY.
	p.colorErr = false

	p.Run()

	// Emit while the printer is running. After the first record is
	// consumed, the loop will be sitting in cond.Wait, and Emit's
	// Broadcast must wake it.
	bt.Emit(Record{Level: Warn, Component: Importer, Msg: "after-run-warn"})
	bt.Emit(Record{Level: Error, Component: Engine, Msg: "after-run-error"})
	bt.Emit(Record{Level: Debug, Component: Container, Msg: "after-run-debug"})

	// Give the goroutine a beat to drain. Stop's documented contract drains
	// remaining records, so this is belt-and-suspenders, but it shortens
	// the window where we'd fail because the test ran faster than a
	// goroutine context switch.
	time.Sleep(20 * time.Millisecond)

	// Stop must return — if it hangs, the test will time out.
	done := make(chan struct{})
	go func() {
		p.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("LogPrinter.Stop did not return within 2s — printer goroutine likely deadlocked")
	}

	// Stop is idempotent — calling again must not block or panic.
	p.Stop()

	out := stdout.String()
	err := stderr.String()
	combined := out + err

	for _, want := range []string{"before-run", "after-run-warn", "after-run-error", "after-run-debug"} {
		if !strings.Contains(combined, want) {
			t.Errorf("printer output missing %q\nstdout:\n%s\nstderr:\n%s", want, out, err)
		}
	}

	// Info goes to stdout; Warn/Error/Debug to stderr. Cross-check at
	// least one of each direction so a regression that swaps streams is
	// caught.
	if !strings.Contains(out, "before-run") {
		t.Errorf("Info record should be on stdout, got stdout=%q", out)
	}
	if !strings.Contains(err, "after-run-warn") {
		t.Errorf("Warn record should be on stderr, got stderr=%q", err)
	}
}

// redirectStdio replaces os.Stdout and os.Stderr with pipes for the duration
// of a test. Returns buffers receiving the captured bytes plus a restore
// func. Read goroutines drain the pipes so the writers don't block when the
// kernel pipe buffer fills.
func redirectStdio(t *testing.T) (stdout, stderr *bytes.Buffer, restore func()) {
	t.Helper()
	origStdout := os.Stdout
	origStderr := os.Stderr

	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}

	os.Stdout = outW
	os.Stderr = errW

	stdout = &bytes.Buffer{}
	stderr = &bytes.Buffer{}

	doneOut := make(chan struct{})
	doneErr := make(chan struct{})
	go func() {
		_, _ = io.Copy(stdout, outR)
		close(doneOut)
	}()
	go func() {
		_, _ = io.Copy(stderr, errR)
		close(doneErr)
	}()

	restore = func() {
		outW.Close()
		errW.Close()
		<-doneOut
		<-doneErr
		os.Stdout = origStdout
		os.Stderr = origStderr
	}
	return stdout, stderr, restore
}
