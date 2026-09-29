package log

import (
	"bufio"
	"context"
	"os"
	"sync"
)

// NewExecWriters returns two *os.File suitable for passing as stdout and stderr
// FDs in an ExecRequest. Data written to stdout is emitted as Info-level log
// records; data written to stderr is emitted as Warn-level records. Call the
// returned close function after the process exits to drain remaining output.
// For best performance, close stdout and stderr (the write ends) yourself after
// the child process starts, then call closeFn to wait for goroutines to drain.
func NewExecWriters(ctx context.Context, c Component) (stdout *os.File, stderr *os.File, closeFn func()) {
	l := From(ctx, c)

	outR, outW, _ := os.Pipe()
	errR, errW, _ := os.Pipe()

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		scan := bufio.NewScanner(outR)
		for scan.Scan() {
			l.Info("%s", scan.Text())
		}
		outR.Close()
	}()

	go func() {
		defer wg.Done()
		scan := bufio.NewScanner(errR)
		for scan.Scan() {
			l.Warn("%s", scan.Text())
		}
		errR.Close()
	}()

	closeFn = func() {
		outW.Close()
		errW.Close()
		wg.Wait()
	}

	return outW, errW, closeFn
}
