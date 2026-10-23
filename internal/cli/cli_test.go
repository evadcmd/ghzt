package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

func run(args ...string) (code int, stdout, stderr string) {
	var out, errOut bytes.Buffer
	code = Run(args, IO{In: strings.NewReader(""), Out: &out, ErrOut: &errOut})
	return code, out.String(), errOut.String()
}

func TestRunDispatchesOnArgv0(t *testing.T) {
	tests := []struct {
		args []string
		want string
	}{
		{[]string{"/usr/local/bin/gh", "--version"}, "gh version dev\n"},
		{[]string{"/opt/ghzt/bin/git", "--version"}, "git version dev\n"},
		{[]string{"ghzt", "--version"}, "ghzt version dev\n"},
		{[]string{"ghzt", "gh", "--version"}, "gh version dev\n"},
		{[]string{"ghzt", "git", "--version"}, "git version dev\n"},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			code, stdout, stderr := run(tt.args...)
			if code != exitOK || stdout != tt.want || stderr != "" {
				t.Errorf("got (%d, %q, %q), want (%d, %q, \"\")", code, stdout, stderr, exitOK, tt.want)
			}
		})
	}
}

func TestRunReportsFlagErrorsWithoutUsage(t *testing.T) {
	code, stdout, stderr := run("gh", "--no-such-flag")
	if code != exitError {
		t.Errorf("code = %d, want %d", code, exitError)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	want := "gh: unknown flag: --no-such-flag\nRun 'gh --help' for usage.\n"
	if stderr != want {
		t.Errorf("stderr = %q, want %q", stderr, want)
	}
}

func TestRunRejectsUnknownCommands(t *testing.T) {
	tests := []struct {
		args []string
		want string
	}{
		{[]string{"ghzt", "bogus"}, `ghzt: unknown command "bogus" for "ghzt"`},
		{[]string{"gh", "bogus"}, `gh: unknown command "bogus" for "gh"`},
		{[]string{"git", "bogus"}, `git: unknown command "bogus" for "git"`},
		{[]string{"ghzt", "gh", "bogus"}, `ghzt: unknown command "bogus" for "ghzt gh"`},
		{[]string{"ghzt", "git", "bogus"}, `ghzt: unknown command "bogus" for "ghzt git"`},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			code, stdout, stderr := run(tt.args...)
			if code != exitError || stdout != "" || !strings.HasPrefix(stderr, tt.want) {
				t.Errorf("got (%d, %q, %q), want (%d, \"\", %q...)", code, stdout, stderr, exitError, tt.want)
			}
		})
	}
}

func TestRunPrintsHelpWithoutArgs(t *testing.T) {
	for _, args := range [][]string{{"ghzt"}, {"gh"}, {"ghzt", "gh"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			code, stdout, stderr := run(args...)
			if code != exitOK || !strings.Contains(stdout, "Usage:") || stderr != "" {
				t.Errorf("got (%d, %q, %q), want help on stdout and exit %d", code, stdout, stderr, exitOK)
			}
		})
	}
}

func TestRunLogsOneRecordOffTheOutput(t *testing.T) {
	var out, errOut, logged bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logged, nil))
	code := Run([]string{"gh", "bogus"}, IO{In: strings.NewReader(""), Out: &out, ErrOut: &errOut, Log: log})

	var rec map[string]any
	if err := json.Unmarshal(logged.Bytes(), &rec); err != nil {
		t.Fatalf("log = %q, want one JSON record: %v", logged.String(), err)
	}
	if rec["level"] != "ERROR" || rec["name"] != "gh" || rec["command"] != "gh" ||
		rec["exit_code"] != float64(code) || !strings.Contains(fmt.Sprint(rec["error"]), "bogus") {
		t.Errorf("record = %v", rec)
	}
	if _, ok := rec["duration_ms"].(float64); !ok {
		t.Errorf("duration_ms = %v, want a number", rec["duration_ms"])
	}
	if strings.Contains(out.String()+errOut.String(), `"exit_code"`) {
		t.Errorf("the record leaked into the output: stdout %q, stderr %q", out.String(), errOut.String())
	}
}

// The record names the command and the flags that were set, not the raw args,
// which can hold secrets or whole bodies.
func TestRunLogsFlagNamesNotArgs(t *testing.T) {
	var logged bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logged, nil))
	s := IO{In: strings.NewReader(""), Out: &bytes.Buffer{}, ErrOut: &bytes.Buffer{}, Log: log}
	Run([]string{"ghzt", "gh", "--version=true"}, s)

	var rec map[string]any
	if err := json.Unmarshal(logged.Bytes(), &rec); err != nil {
		t.Fatalf("log = %q, want one JSON record: %v", logged.String(), err)
	}
	if rec["command"] != "ghzt gh" || fmt.Sprint(rec["flags"]) != "[version]" {
		t.Errorf("record = %v, want command %q and flags [version]", rec, "ghzt gh")
	}
	if _, ok := rec["args"]; ok || strings.Contains(logged.String(), "--version=true") {
		t.Errorf("log = %q, want no raw args", logged.String())
	}
}

func TestRunHasNoCompletionCommand(t *testing.T) {
	code, _, stderr := run("ghzt", "completion", "bash")
	if code != exitError || !strings.Contains(stderr, `unknown command "completion"`) {
		t.Errorf("got (%d, %q), want an unknown command error", code, stderr)
	}
}
