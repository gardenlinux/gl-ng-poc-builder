package log

import (
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/term"
)

// LogPrinter reads from a BufferTarget and prints all records to stdio,
// using the same formatting and color scheme as ConsoleTarget. It runs as
// a goroutine, starting from position 0 and waiting for new records when
// it reaches the end of the buffer.
type LogPrinter struct {
	buf      *BufferTarget
	reader   *BufferReader
	colorErr bool
	start    time.Time

	stopFlag atomic.Bool
	done     chan struct{}
	once     sync.Once
}

// NewLogPrinter creates a new LogPrinter for the given buffer. It does NOT
// start reading — call Run() to begin.
func NewLogPrinter(buf *BufferTarget) *LogPrinter {
	return &LogPrinter{
		buf:      buf,
		reader:   buf.Reader(),
		colorErr: term.IsTerminal(int(os.Stderr.Fd())),
		start:    startTime,
		done:     make(chan struct{}),
	}
}

// Run starts the printer goroutine. It reads all records from the beginning
// of the buffer, prints them, waits for more when caught up, and exits when
// Stop() has been called and all remaining records have been drained.
func (p *LogPrinter) Run() {
	go p.loop()
}

// Stop signals the printer to terminate after draining remaining records.
// It blocks until the printer goroutine has exited.
func (p *LogPrinter) Stop() {
	p.once.Do(func() {
		p.stopFlag.Store(true)
		p.buf.Notify()
	})
	<-p.done
}

func (p *LogPrinter) loop() {
	defer close(p.done)

	for {
		rec, err := p.reader.Read()
		if err == ErrNoMore {
			if p.stopFlag.Load() {
				return
			}
			p.waitForData()
			continue
		}
		p.printRecord(rec)
	}
}

func (p *LogPrinter) waitForData() {
	p.buf.mu.Lock()
	for p.reader.pos >= len(p.buf.records) {
		if p.stopFlag.Load() {
			p.buf.mu.Unlock()
			return
		}
		p.buf.cond.Wait()
	}
	p.buf.mu.Unlock()
}

func (p *LogPrinter) printRecord(r Record) {
	comp := r.Component.String()
	ts := time.Since(p.start).Truncate(time.Millisecond)

	switch r.Level {
	case Info:
		fmt.Fprintf(os.Stdout, "%s [INFO] %s: %s\n", ts, comp, r.Msg)
	case Debug:
		if p.colorErr {
			fmt.Fprintf(os.Stderr, "%s%s [DEBUG] %s: %s%s\n", ansiDim, ts, comp, r.Msg, ansiReset)
		} else {
			fmt.Fprintf(os.Stderr, "%s [DEBUG] %s: %s\n", ts, comp, r.Msg)
		}
	case Warn:
		if p.colorErr {
			fmt.Fprintf(os.Stderr, "%s%s [WARN] %s: %s%s\n", ansiYellow, ts, comp, r.Msg, ansiReset)
		} else {
			fmt.Fprintf(os.Stderr, "%s [WARN] %s: %s\n", ts, comp, r.Msg)
		}
	case Error:
		if p.colorErr {
			fmt.Fprintf(os.Stderr, "%s%s [ERROR] %s: %s%s\n", ansiRed, ts, comp, r.Msg, ansiReset)
		} else {
			fmt.Fprintf(os.Stderr, "%s [ERROR] %s: %s\n", ts, comp, r.Msg)
		}
	}
}
