package cli

import (
	"bytes"
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

func TestRunHasNoCompletionCommand(t *testing.T) {
	code, _, stderr := run("ghzt", "completion", "bash")
	if code != exitError || !strings.Contains(stderr, `unknown command "completion"`) {
		t.Errorf("got (%d, %q), want an unknown command error", code, stderr)
	}
}
