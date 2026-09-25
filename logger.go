// logger.go - slog setup for --debug and --trace.
//
// --debug writes debug-level logs to stderr; --trace writes everything down
// to trace level (raw PTY chunks) to a file truncated on every start. The TUI
// owns the terminal, so there --debug goes to the trace file as well. With no
// flags only warnings and errors are logged.
package main

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"syscall"
)

// LevelTrace is below Debug — logs raw PTY byte chunks for deep inspection.
const LevelTrace = slog.Level(-8)

// defaultTraceFile is where --trace writes unless log_file says otherwise.
const defaultTraceFile = "/tmp/bunker.log"

// L is the package-level structured logger. It discards until initLogger
// runs.
var L *slog.Logger = slog.Default()

// logOptions says what to log where.
type logOptions struct {
	Debug     bool      // --debug
	Trace     bool      // --trace
	TracePath string    // file for --trace; "" (log_file = "") turns it off
	Level     string    // level when neither flag is set (log_level)
	Stderr    io.Writer // nil when the terminal belongs to the TUI
}

// initLogger installs L and the slog default and returns a function that
// closes the trace file. A trace file that cannot be opened safely is
// reported on stderr (when there is one) and skipped.
func initLogger(o logOptions) (cleanup func()) {
	var handlers []slog.Handler
	cleanup = func() {}

	stderrLevel := parseLevel(o.Level, slog.LevelWarn)
	if o.Debug {
		stderrLevel = slog.LevelDebug
	}
	fileLevel := LevelTrace
	if !o.Trace {
		fileLevel = slog.LevelDebug
	}
	if o.TracePath != "" && (o.Trace || (o.Debug && o.Stderr == nil)) {
		f, err := openTraceFile(o.TracePath)
		if err != nil {
			if o.Stderr != nil {
				fmt.Fprintf(o.Stderr, "bunker: no trace log: %v\n", err) //nolint:errcheck // stderr is the last resort
			}
		} else {
			handlers = append(handlers, slog.NewTextHandler(f, &slog.HandlerOptions{Level: fileLevel}))
			cleanup = func() {
				if err := f.Close(); err != nil && o.Stderr != nil {
					fmt.Fprintf(o.Stderr, "bunker: close trace log: %v\n", err) //nolint:errcheck // stderr is the last resort
				}
			}
		}
	}
	if o.Stderr != nil {
		handlers = append(handlers, slog.NewTextHandler(o.Stderr, &slog.HandlerOptions{Level: stderrLevel}))
	}

	L = slog.New(slog.NewMultiHandler(handlers...))
	slog.SetDefault(L)
	return cleanup
}

func parseLevel(name string, fallback slog.Level) slog.Level {
	switch name {
	case "trace":
		return LevelTrace
	case "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	}
	return fallback
}

var errTraceNotOurs = errors.New("not a regular file owned by this user")

// openTraceFile opens path for a fresh trace log. The trace holds terminal
// output, and /tmp is shared: a file another user planted there (or a
// symlink to one) would let them read it, so only a regular file this user
// owns is truncated and used, and it is kept at mode 0600. O_NONBLOCK keeps
// a planted FIFO from blocking startup until someone reads it.
func openTraceFile(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	fail := func(err error) (*os.File, error) {
		if cerr := f.Close(); cerr != nil {
			err = errors.Join(err, cerr)
		}
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	info, err := f.Stat()
	if err != nil {
		return fail(err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || !ok || int(st.Uid) != os.Getuid() {
		return fail(errTraceNotOurs)
	}
	if err := f.Chmod(0o600); err != nil {
		return fail(err)
	}
	if err := f.Truncate(0); err != nil {
		return fail(err)
	}
	return f, nil
}
