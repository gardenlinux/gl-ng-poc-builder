package log

import (
	"fmt"
	"os"
	"sync"
	"time"

	"golang.org/x/term"
)

const (
	ansiReset  = "\033[0m"
	ansiRed    = "\033[31m"
	ansiYellow = "\033[33m"
	ansiDim    = "\033[2m"
)

var startTime = time.Now()

type ConsoleTarget struct {
	mu         sync.Mutex
	colorErr   bool
	stderrOnly bool
}

func NewConsoleTarget() *ConsoleTarget {
	return &ConsoleTarget{
		colorErr: term.IsTerminal(int(os.Stderr.Fd())),
	}
}

// NewStderrConsoleTarget returns a ConsoleTarget that routes every level —
// including INFO — to stderr. Use it for commands whose stdout carries
// machine-parseable output (e.g. `gl resolve`).
func NewStderrConsoleTarget() *ConsoleTarget {
	return &ConsoleTarget{
		colorErr:   term.IsTerminal(int(os.Stderr.Fd())),
		stderrOnly: true,
	}
}

func (c *ConsoleTarget) Emit(r Record) {
	c.mu.Lock()
	defer c.mu.Unlock()

	comp := r.Component.String()
	ts := time.Since(startTime).Truncate(time.Millisecond)

	infoOut := os.Stdout
	if c.stderrOnly {
		infoOut = os.Stderr
	}

	switch r.Level {
	case Info:
		fmt.Fprintf(infoOut, "%s [INFO] %s: %s\n", ts, comp, r.Msg)
	case Debug:
		if c.colorErr {
			fmt.Fprintf(os.Stderr, "%s%s [DEBUG] %s: %s%s\n", ansiDim, ts, comp, r.Msg, ansiReset)
		} else {
			fmt.Fprintf(os.Stderr, "%s [DEBUG] %s: %s\n", ts, comp, r.Msg)
		}
	case Warn:
		if c.colorErr {
			fmt.Fprintf(os.Stderr, "%s%s [WARN] %s: %s%s\n", ansiYellow, ts, comp, r.Msg, ansiReset)
		} else {
			fmt.Fprintf(os.Stderr, "%s [WARN] %s: %s\n", ts, comp, r.Msg)
		}
	case Error:
		if c.colorErr {
			fmt.Fprintf(os.Stderr, "%s%s [ERROR] %s: %s%s\n", ansiRed, ts, comp, r.Msg, ansiReset)
		} else {
			fmt.Fprintf(os.Stderr, "%s [ERROR] %s: %s\n", ts, comp, r.Msg)
		}
	}
}
