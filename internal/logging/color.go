package logging

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"

	"golang.org/x/term"
)

// ColorMode controls whether log output is colored.
type ColorMode int

const (
	// ColorAuto colors output only when the destination is a terminal that supports it.
	ColorAuto ColorMode = iota
	// ColorAlways colors output even when it is redirected to a file or pipe.
	ColorAlways
	// ColorNever disables color entirely.
	ColorNever
)

// ParseColorMode parses the value of the -color flag ("auto", "always" or "never").
func ParseColorMode(s string) (ColorMode, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "auto":
		return ColorAuto, nil
	case "always":
		return ColorAlways, nil
	case "never":
		return ColorNever, nil
	}
	return ColorAuto, fmt.Errorf("invalid color mode %q (want auto, always or never)", s)
}

func (m ColorMode) String() string {
	switch m {
	case ColorAlways:
		return "always"
	case ColorNever:
		return "never"
	}
	return "auto"
}

// ANSI SGR codes used for the level labels.
const (
	ansiReset  = "\x1b[0m"
	ansiRed    = "\x1b[31m"
	ansiYellow = "\x1b[33m"
	ansiCyan   = "\x1b[36m"
	ansiGray   = "\x1b[90m"

	ansiBoldRed = "\x1b[1;31m"
)

// Paint wraps s in the given SGR code and resets afterwards. Each line is
// wrapped separately so a colored segment never spans a newline, which keeps
// escape codes from leaking into later output if a line is cut off or
// interleaved with other writers.
func Paint(code, s string) string {
	if code == "" || s == "" {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if line != "" {
			lines[i] = code + line + ansiReset
		}
	}
	return strings.Join(lines, "\n")
}

var rainbowColors = [...]string{
	"\x1b[31m", // red
	"\x1b[33m", // yellow
	"\x1b[32m", // green
	"\x1b[36m", // cyan
	"\x1b[34m", // blue
	"\x1b[35m", // magenta
}

// paintRainbow colors each visible character of s with the next color of the
// rainbow, leaving whitespace uncolored. Each line ends with a reset, like Paint.
func paintRainbow(s string) string {
	var b strings.Builder
	i, open := 0, false
	for _, r := range s {
		switch {
		case r == '\n':
			if open {
				b.WriteString(ansiReset)
				open = false
			}
		case r != ' ' && r != '\t':
			b.WriteString(rainbowColors[i%len(rainbowColors)])
			i++
			open = true
		}
		b.WriteRune(r)
	}
	if open {
		b.WriteString(ansiReset)
	}
	return b.String()
}

// ShouldColor reports whether output written to w should be colored under mode.
// It is the single place where color support is decided.
func ShouldColor(mode ColorMode, w io.Writer) bool {
	return systemEnv.shouldColor(mode, w)
}

// environment holds everything color detection reads from the outside world,
// so tests can substitute a fake terminal and environment.
type environment struct {
	getenv     func(string) string
	isTerminal func(fd uintptr) bool
	enableVT   func(fd uintptr) bool // turns on ANSI processing (Windows); false if unsupported
	goos       string
}

var systemEnv = environment{
	getenv:     os.Getenv,
	isTerminal: func(fd uintptr) bool { return term.IsTerminal(int(fd)) },
	enableVT:   enableVirtualTerminal,
	goos:       runtime.GOOS,
}

// fileDescriptor is satisfied by *os.File.
type fileDescriptor interface {
	Fd() uintptr
}

func (e environment) shouldColor(mode ColorMode, w io.Writer) bool {
	f, isFile := w.(fileDescriptor)
	tty := isFile && e.isTerminal(f.Fd())

	switch mode {
	case ColorNever:
		return false
	case ColorAlways:
		if tty {
			e.enableVT(f.Fd()) // best effort so codes render instead of printing raw
		}
		return true
	}

	// auto: honour the common environment conventions first.
	if e.getenv("NO_COLOR") != "" { // https://no-color.org
		return false
	}
	if isTruthy(e.getenv("FORCE_COLOR")) || isTruthy(e.getenv("CLICOLOR_FORCE")) {
		return true
	}
	if e.getenv("CLICOLOR") == "0" {
		return false
	}

	if !tty {
		return false // redirected to a file or pipe
	}
	termName := e.getenv("TERM")
	if termName == "dumb" {
		return false
	}
	// Unix terminals always set TERM; an empty one means something unusual.
	// Windows consoles don't set it, so rely on the VT check there.
	if termName == "" && e.goos != "windows" {
		return false
	}
	return e.enableVT(f.Fd())
}

func isTruthy(v string) bool {
	v = strings.ToLower(strings.TrimSpace(v))
	return v != "" && v != "0" && v != "false"
}
