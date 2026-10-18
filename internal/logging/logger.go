// Package logging is a thin wrapper around the standard log package that
// labels each line with a topic (what the bot is doing) and, for anything
// other than plain info, its severity. Labels are colored when supported.
package logging

import (
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync/atomic"
)

// Level is a log severity.
type Level int32

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
)

// ParseLevel parses a config log level ("debug", "info", "warn", "error").
// Unknown values fall back to info.
func ParseLevel(s string) Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return LevelDebug
	case "warn", "warning":
		return LevelWarn
	case "error":
		return LevelError
	}
	return LevelInfo
}

// Severity words shown after the topic. Info has none: the topic says enough.
var levelWords = [...]struct{ text, color string }{
	LevelDebug: {"DEBUG", ansiGray},
	LevelInfo:  {"", ""},
	LevelWarn:  {"WARN", ansiYellow},
	LevelError: {"ERROR", ansiBoldRed},
}

// Color is an ANSI color used for a topic label.
type Color string

const (
	Blue    Color = "\x1b[1;34m"
	Cyan    Color = "\x1b[1;36m"
	Green   Color = "\x1b[1;32m"
	Magenta Color = "\x1b[1;35m"
	Yellow  Color = "\x1b[1;33m"
	Red     Color = "\x1b[1;31m"
)

// topicWidth is the column width topic names are padded to so messages line up.
const topicWidth = 7

// Topic is a label for one area of the bot, such as "WS" or "FILL".
// Create one per area with NewTopic and log through its methods.
type Topic struct {
	name  string
	color Color
}

// NewTopic returns a topic with the given label and color.
func NewTopic(name string, c Color) Topic {
	return Topic{name: strings.ToUpper(name), color: c}
}

// Label returns the label for a line of topic t at lvl, colored if color is
// true, e.g. "WS     " for info or "WS      ERROR" for an error.
func (t Topic) Label(lvl Level, color bool) string {
	pad := ""
	if n := topicWidth - len(t.name); n > 0 {
		pad = strings.Repeat(" ", n)
	}
	w := levelWords[lvl]
	if !color {
		if w.text == "" {
			return t.name + pad
		}
		return t.name + pad + " " + w.text
	}

	code := string(t.color)
	if lvl == LevelDebug {
		code = ansiGray // keep debug noise visually quiet
	}
	out := Paint(code, t.name) + pad
	if w.text != "" {
		out += " " + Paint(w.color, w.text)
	}
	return out
}

func (t Topic) Debugf(format string, args ...any) { Default().output(3, t, LevelDebug, format, args) }
func (t Topic) Infof(format string, args ...any)  { Default().output(3, t, LevelInfo, format, args) }
func (t Topic) Warnf(format string, args ...any)  { Default().output(3, t, LevelWarn, format, args) }
func (t Topic) Errorf(format string, args ...any) { Default().output(3, t, LevelError, format, args) }

// Info logs its operands like log.Println.
func (t Topic) Info(args ...any) { Default().output(3, t, LevelInfo, "", args) }

// Fatalf logs at error level and exits with status 1, like log.Fatalf.
func (t Topic) Fatalf(format string, args ...any) {
	l := Default()
	l.out.Output(2, t.Label(LevelError, l.color)+" "+fmt.Sprintf(format, args...))
	os.Exit(1)
}

// Logger writes labeled lines in the standard log format
// ("2006/01/02 15:04:05 TOPIC   message").
type Logger struct {
	out   *log.Logger
	color bool
	level atomic.Int32
}

// New returns a Logger writing to w, with color decided by ShouldColor.
func New(w io.Writer, mode ColorMode) *Logger {
	l := &Logger{
		out:   log.New(w, "", log.LstdFlags),
		color: ShouldColor(mode, w),
	}
	l.level.Store(int32(LevelInfo))
	return l
}

// ColorEnabled reports whether this logger emits ANSI codes.
func (l *Logger) ColorEnabled() bool { return l.color }

// SetLevel sets the minimum level that is written.
func (l *Logger) SetLevel(lvl Level) { l.level.Store(int32(lvl)) }

// Logf writes one line for topic t at lvl.
func (l *Logger) Logf(t Topic, lvl Level, format string, args ...any) {
	l.output(3, t, lvl, format, args)
}

// output formats with Sprintf, or like Println when format is empty.
func (l *Logger) output(depth int, t Topic, lvl Level, format string, args []any) {
	if lvl < Level(l.level.Load()) {
		return
	}
	var msg string
	if format == "" {
		msg = strings.TrimSuffix(fmt.Sprintln(args...), "\n")
	} else {
		msg = fmt.Sprintf(format, args...)
	}
	l.out.Output(depth, t.Label(lvl, l.color)+" "+msg)
}

// Package-level logger used by the rest of the application.

var std atomic.Pointer[Logger]

func init() { std.Store(New(os.Stderr, ColorAuto)) }

// Configure replaces the default logger. Call it once at startup, before
// other goroutines log.
func Configure(w io.Writer, mode ColorMode) { std.Store(New(w, mode)) }

// Default returns the package-level logger.
func Default() *Logger { return std.Load() }

func SetLevel(lvl Level) { Default().SetLevel(lvl) }

// Rainbow returns s rainbow-colored if the default logger uses color, and
// unchanged otherwise, so it can be passed straight to a log call.
func Rainbow(s string) string {
	if !Default().color {
		return s
	}
	return paintRainbow(s)
}
