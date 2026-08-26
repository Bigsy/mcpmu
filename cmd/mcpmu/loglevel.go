package main

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"

	"github.com/Bigsy/mcpmu/internal/mcp"
	"github.com/Bigsy/mcpmu/internal/server"
)

// logLevel is the severity ladder behind --log-level.
type logLevel int

const (
	levelDebug logLevel = iota
	levelInfo
	levelWarn
	levelError
)

// parseLogLevel maps the --log-level flag value to a level. Case-insensitive.
func parseLogLevel(s string) (logLevel, error) {
	switch strings.ToLower(s) {
	case "debug":
		return levelDebug, nil
	case "info":
		return levelInfo, nil
	case "warn":
		return levelWarn, nil
	case "error":
		return levelError, nil
	}
	return 0, fmt.Errorf("invalid log level %q: expected debug, info, warn, or error", s)
}

// configureLogging points the standard logger at output, filtered to level.
// The codebase logs through the untagged standard logger, so severity is
// inferred per line by classifyLogLine; debug-only payload dumps are gated
// separately by the DebugLogging switches, which only "debug" turns on.
func configureLogging(level string, output io.Writer) error {
	lvl, err := parseLogLevel(level)
	if err != nil {
		return err
	}
	server.DebugLogging = lvl == levelDebug
	mcp.DebugLogging = lvl == levelDebug
	flags := log.LstdFlags
	if lvl == levelDebug {
		flags |= log.Lshortfile
	}
	log.SetFlags(flags)
	log.SetOutput(newLeveledWriter(lvl, output, flags))
	return nil
}

// leveledWriter drops standard-logger lines below its level. The standard
// logger writes one complete line per call, so each Write is one message.
type leveledWriter struct {
	level logLevel
	out   io.Writer
	flags int

	mu sync.Mutex
}

func newLeveledWriter(level logLevel, out io.Writer, flags int) *leveledWriter {
	return &leveledWriter{level: level, out: out, flags: flags}
}

func (w *leveledWriter) Write(p []byte) (int, error) {
	if classifyLogLine(stripLogPrefix(p, w.flags)) < w.level {
		return len(p), nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.out.Write(p)
}

// stripLogPrefix skips the date, time and file:line fields the standard
// logger prepends per flags, leaving the message the caller wrote.
func stripLogPrefix(line []byte, flags int) []byte {
	skip := 0
	if flags&log.Ldate != 0 {
		skip++
	}
	if flags&log.Ltime != 0 {
		skip++
	}
	if flags&(log.Lshortfile|log.Llongfile) != 0 {
		skip++
	}
	for ; skip > 0; skip-- {
		i := bytes.IndexByte(line, ' ')
		if i < 0 {
			return line
		}
		line = line[i+1:]
	}
	return line
}

// classifyLogLine infers a message's severity from how it opens. The
// conventions are the ones the codebase already uses ("Failed to …",
// "Warning: …", "PANIC …"); anything else is informational.
func classifyLogLine(msg []byte) logLevel {
	head := msg
	if len(head) > 24 {
		head = head[:24]
	}
	lower := strings.ToLower(string(head))
	switch {
	case strings.HasPrefix(lower, "panic"),
		strings.HasPrefix(lower, "error"),
		strings.HasPrefix(lower, "fatal"),
		strings.HasPrefix(lower, "failed"),
		strings.HasPrefix(lower, "cannot"),
		strings.HasPrefix(lower, "refusing"):
		return levelError
	case strings.HasPrefix(lower, "warn"),
		strings.HasPrefix(lower, "rejecting"),
		strings.HasPrefix(lower, "no "):
		return levelWarn
	}
	// "<subsystem>: error …" / "<subsystem> error: …" — a tagged failure.
	if i := strings.IndexByte(lower, ':'); i > 0 {
		tag := strings.TrimSpace(lower[:i])
		rest := strings.TrimSpace(lower[i+1:])
		switch {
		case strings.HasSuffix(tag, "error"), strings.HasSuffix(tag, "failed"),
			strings.HasPrefix(rest, "error"), strings.HasPrefix(rest, "failed"):
			return levelError
		case strings.HasSuffix(tag, "warning"), strings.HasPrefix(rest, "warn"):
			return levelWarn
		}
	}
	return levelInfo
}
