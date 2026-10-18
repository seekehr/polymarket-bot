package logging

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// fakeTTY is a writer that claims a file descriptor, standing in for a terminal.
type fakeTTY struct{ bytes.Buffer }

func (*fakeTTY) Fd() uintptr { return 42 }

// fakeEnv builds a detection environment with the given variables, where only
// fd 42 is a terminal and vtOK says whether ANSI processing can be enabled.
func fakeEnv(goos string, vtOK bool, vars map[string]string) environment {
	return environment{
		getenv:     func(k string) string { return vars[k] },
		isTerminal: func(fd uintptr) bool { return fd == 42 },
		enableVT:   func(uintptr) bool { return vtOK },
		goos:       goos,
	}
}

func TestShouldColor(t *testing.T) {
	xterm := map[string]string{"TERM": "xterm-256color"}
	tests := []struct {
		name string
		env  environment
		mode ColorMode
		tty  bool
		want bool
	}{
		{"supported interactive terminal", fakeEnv("linux", true, xterm), ColorAuto, true, true},
		{"modern windows console", fakeEnv("windows", true, nil), ColorAuto, true, true},
		{"legacy windows console without VT", fakeEnv("windows", false, nil), ColorAuto, true, false},
		{"dumb terminal", fakeEnv("linux", true, map[string]string{"TERM": "dumb"}), ColorAuto, true, false},
		{"unix terminal with no TERM", fakeEnv("darwin", true, nil), ColorAuto, true, false},
		{"redirected output", fakeEnv("linux", true, xterm), ColorAuto, false, false},
		{"NO_COLOR", fakeEnv("linux", true, map[string]string{"TERM": "xterm", "NO_COLOR": "1"}), ColorAuto, true, false},
		{"NO_COLOR empty is ignored", fakeEnv("linux", true, map[string]string{"TERM": "xterm", "NO_COLOR": ""}), ColorAuto, true, true},
		{"CLICOLOR=0", fakeEnv("linux", true, map[string]string{"TERM": "xterm", "CLICOLOR": "0"}), ColorAuto, true, false},
		{"FORCE_COLOR when redirected", fakeEnv("linux", true, map[string]string{"FORCE_COLOR": "1"}), ColorAuto, false, true},
		{"FORCE_COLOR=0", fakeEnv("linux", true, map[string]string{"FORCE_COLOR": "0"}), ColorAuto, false, false},
		{"always when redirected", fakeEnv("linux", true, nil), ColorAlways, false, true},
		{"always overrides NO_COLOR", fakeEnv("linux", true, map[string]string{"NO_COLOR": "1"}), ColorAlways, true, true},
		{"never on a terminal", fakeEnv("linux", true, xterm), ColorNever, true, false},
		{"never overrides FORCE_COLOR", fakeEnv("linux", true, map[string]string{"FORCE_COLOR": "1"}), ColorNever, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var w interface{ Write([]byte) (int, error) } = &bytes.Buffer{}
			if tt.tty {
				w = &fakeTTY{}
			}
			if got := tt.env.shouldColor(tt.mode, w); got != tt.want {
				t.Errorf("shouldColor(%v) = %v, want %v", tt.mode, got, tt.want)
			}
		})
	}
}

// A real file is never a terminal, so auto must stay plain even with TERM set.
func TestShouldColorRedirectedToFile(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	t.Setenv("FORCE_COLOR", "")
	t.Setenv("CLICOLOR_FORCE", "")
	f, err := os.Create(filepath.Join(t.TempDir(), "out.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	if ShouldColor(ColorAuto, f) {
		t.Error("auto mode colored output redirected to a file")
	}
	if !ShouldColor(ColorAlways, f) {
		t.Error("always mode did not color output redirected to a file")
	}
}

func TestParseColorMode(t *testing.T) {
	for in, want := range map[string]ColorMode{"": ColorAuto, "auto": ColorAuto, "ALWAYS": ColorAlways, "never": ColorNever} {
		got, err := ParseColorMode(in)
		if err != nil || got != want {
			t.Errorf("ParseColorMode(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	if _, err := ParseColorMode("sometimes"); err == nil {
		t.Error("ParseColorMode accepted an invalid value")
	}
}

func TestPaintMultiline(t *testing.T) {
	got := Paint(ansiRed, "one\n\ntwo\n")
	want := ansiRed + "one" + ansiReset + "\n\n" + ansiRed + "two" + ansiReset + "\n"
	if got != want {
		t.Errorf("Paint = %q, want %q", got, want)
	}
}

var escapes = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func TestPaintRainbow(t *testing.T) {
	in := " PAPER $1\nab"
	got := paintRainbow(in)
	if plain := escapes.ReplaceAllString(got, ""); plain != in {
		t.Errorf("rainbow changed the text: %q", plain)
	}
	if !strings.HasPrefix(got, " "+rainbowColors[0]+"P"+rainbowColors[1]+"A") {
		t.Errorf("colors don't cycle per character: %q", got)
	}
	for _, line := range strings.Split(got, "\n") {
		if !strings.HasSuffix(line, ansiReset) {
			t.Errorf("line not reset: %q", line)
		}
	}
}

func TestRainbowRespectsColorMode(t *testing.T) {
	prev := Default()
	defer std.Store(prev)

	Configure(&bytes.Buffer{}, ColorNever)
	if got := Rainbow("hi"); got != "hi" {
		t.Errorf("Rainbow with color off = %q, want plain", got)
	}
	Configure(&bytes.Buffer{}, ColorAlways)
	if got := Rainbow("hi"); got == "hi" {
		t.Error("Rainbow with color on returned plain text")
	}
}

var timestamp = regexp.MustCompile(`^\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2} `)

var wsTopic = NewTopic("ws", Cyan)

func TestTopicLabel(t *testing.T) {
	tests := []struct {
		lvl   Level
		color bool
		want  string
	}{
		{LevelInfo, false, "WS     "},
		{LevelError, false, "WS      ERROR"},
		{LevelWarn, false, "WS      WARN"},
		{LevelDebug, false, "WS      DEBUG"},
		{LevelInfo, true, string(Cyan) + "WS" + ansiReset + "     "},
		{LevelError, true, string(Cyan) + "WS" + ansiReset + "      " + ansiBoldRed + "ERROR" + ansiReset},
		{LevelDebug, true, ansiGray + "WS" + ansiReset + "      " + ansiGray + "DEBUG" + ansiReset},
	}
	for _, tt := range tests {
		if got := wsTopic.Label(tt.lvl, tt.color); got != tt.want {
			t.Errorf("Label(%v, color=%v) = %q, want %q", tt.lvl, tt.color, got, tt.want)
		}
	}
	// Names longer than the column are not truncated.
	if got := NewTopic("STRATEGY", Blue).Label(LevelInfo, false); got != "STRATEGY" {
		t.Errorf("long topic label = %q", got)
	}
}

func TestLoggerColorModes(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("FORCE_COLOR", "")
	t.Setenv("CLICOLOR_FORCE", "")

	t.Run("never", func(t *testing.T) {
		var buf bytes.Buffer
		l := New(&buf, ColorNever)
		l.Logf(wsTopic, LevelError, "boom %d", 1)
		line := buf.String()
		if strings.Contains(line, "\x1b") {
			t.Errorf("never mode emitted escape codes: %q", line)
		}
		if !timestamp.MatchString(line) || !strings.HasSuffix(line, "WS      ERROR boom 1\n") {
			t.Errorf("unexpected line %q", line)
		}
	})

	t.Run("auto on a pipe", func(t *testing.T) {
		var buf bytes.Buffer
		New(&buf, ColorAuto).Logf(wsTopic, LevelInfo, "hello")
		if strings.Contains(buf.String(), "\x1b") {
			t.Errorf("auto mode colored a non-terminal writer: %q", buf.String())
		}
	})

	t.Run("always", func(t *testing.T) {
		var buf bytes.Buffer
		l := New(&buf, ColorAlways)
		l.Logf(wsTopic, LevelWarn, "first\nsecond")
		l.Logf(wsTopic, LevelInfo, "plain")
		out := buf.String()
		if !strings.Contains(out, ansiYellow+"WARN"+ansiReset+" first\nsecond\n") {
			t.Errorf("warn line not colored as expected: %q", out)
		}
		// Every colored segment is closed before the line ends.
		for _, line := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
			if strings.Count(line, "\x1b[") != 2*strings.Count(line, ansiReset) {
				t.Errorf("unbalanced escape codes: %q", line)
			}
		}
		if !strings.Contains(out, string(Cyan)+"WS"+ansiReset+"      plain\n") {
			t.Errorf("info line not colored as expected: %q", out)
		}
	})
}

func TestLoggerLevelFiltering(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, ColorNever)
	l.Logf(wsTopic, LevelDebug, "hidden")
	l.SetLevel(LevelDebug)
	l.Logf(wsTopic, LevelDebug, "shown")
	l.SetLevel(LevelError)
	l.Logf(wsTopic, LevelWarn, "hidden too")

	out := buf.String()
	if strings.Contains(out, "hidden") || !strings.Contains(out, "DEBUG shown") {
		t.Errorf("unexpected output %q", out)
	}
}

func TestTopicUsesDefaultLogger(t *testing.T) {
	prev := Default()
	defer std.Store(prev)

	var buf bytes.Buffer
	Configure(&buf, ColorNever)
	wsTopic.Info("connected", 3)
	wsTopic.Errorf("lost %s", "feed")
	out := buf.String()
	if !strings.Contains(out, "WS      connected 3\n") || !strings.Contains(out, "WS      ERROR lost feed\n") {
		t.Errorf("unexpected output %q", out)
	}
}

func TestParseLevel(t *testing.T) {
	for in, want := range map[string]Level{"debug": LevelDebug, "": LevelInfo, "info": LevelInfo, "WARN": LevelWarn, "error": LevelError, "bogus": LevelInfo} {
		if got := ParseLevel(in); got != want {
			t.Errorf("ParseLevel(%q) = %v, want %v", in, got, want)
		}
	}
}
