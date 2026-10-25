package workspace

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setup makes base/user as a root with a file in it, and a secret file next
// to base, outside the root.
func setup(t *testing.T) (r *Root, base, dir, secret string) {
	t.Helper()
	tmp := t.TempDir()
	base = filepath.Join(tmp, "workspace")
	dir = filepath.Join(base, "user")
	secret = filepath.Join(tmp, "secret.pem")
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o750); err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{
		filepath.Join(dir, "sub", "body.md"): "hello",
		secret:                               "key",
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	r, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r, base, dir, secret
}

func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

func wantErr(t *testing.T, err error, substr string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), substr) {
		t.Errorf("err = %v, want it to contain %q", err, substr)
	}
}

func TestOpen(t *testing.T) {
	_, base, dir, _ := setup(t)

	for _, d := range []string{base, dir} {
		r, err := Open(d)
		if err != nil {
			t.Errorf("Open(%q) = %v, want ok", d, err)
			continue
		}
		_ = r.Close()
	}
	_, err := Open("workspace/user")
	wantErr(t, err, "not an absolute path")
	_, err = Open(filepath.Join(base, "missing"))
	wantErr(t, err, "no such file")
}

func TestFromEnv(t *testing.T) {
	_, _, dir, _ := setup(t)
	readBody := func(r *Root, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = r.Close() }()
		if got, err := r.ReadFile("sub/body.md", 5); err != nil || string(got) != "hello" {
			t.Errorf("ReadFile = %q, %v; want hello", got, err)
		}
	}

	readBody(FromEnv(func(string) string { return dir }))
	// Unset, it falls back to the working directory.
	t.Chdir(dir)
	readBody(FromEnv(func(string) string { return "" }))

	_, err := FromEnv(func(string) string { return "user" })
	wantErr(t, err, "not an absolute path")
}

func TestReadFile(t *testing.T) {
	r, _, dir, secret := setup(t)
	symlink(t, secret, filepath.Join(dir, "link.pem"))
	symlink(t, filepath.Dir(secret), filepath.Join(dir, "updir"))
	symlink(t, "sub", filepath.Join(dir, "inner"))

	for _, name := range []string{"sub/body.md", "./sub/../sub/body.md", filepath.Join(dir, "sub", "body.md")} {
		got, err := r.ReadFile(name, 5)
		if err != nil || string(got) != "hello" {
			t.Errorf("ReadFile(%q) = %q, %v; want hello", name, got, err)
		}
	}
	// A symlinked directory inside the root is fine; only escaping isn't.
	if got, err := r.ReadFile("inner/body.md", 5); err != nil || string(got) != "hello" {
		t.Errorf("ReadFile through an inner symlinked dir = %q, %v; want hello", got, err)
	}

	tests := []struct{ name, want string }{
		{"../secret.pem", "outside"},
		{secret, "outside"},
		{"", "outside"},
		{"link.pem", "not a regular file"},
		{"updir/secret.pem", "escapes"},
		{"sub", "not a regular file"},
		{"missing.md", "no such file"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := r.ReadFile(tt.name, 100)
			wantErr(t, err, tt.want)
		})
	}

	_, err := r.ReadFile("sub/body.md", 4)
	wantErr(t, err, "larger than 4 bytes")

	// A missing file is named as the caller gave it, not by os.Root's
	// syscall and relative path, and is still fs.ErrNotExist.
	name := filepath.Join(dir, "sub", "missing.md")
	_, err = r.ReadFile(name, 100)
	if want := name + ": no such file or directory"; err == nil || err.Error() != want || !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("err = %v, want %q and fs.ErrNotExist", err, want)
	}
}

func TestReadFileMode(t *testing.T) {
	r, _, dir, secret := setup(t)
	symlink(t, secret, filepath.Join(dir, "link.pem"))
	run := filepath.Join(dir, "run.sh")
	if err := os.WriteFile(run, []byte("#!/bin/sh"), 0o700); err != nil { //nolint:gosec // G306: the test needs an executable file.
		t.Fatal(err)
	}

	for name, exec := range map[string]bool{"sub/body.md": false, "run.sh": true} {
		data, mode, err := r.ReadFileMode(name, 100)
		if err != nil || len(data) == 0 || !mode.IsRegular() || (mode&0o100 != 0) != exec {
			t.Errorf("ReadFileMode(%q) = %q, %v, %v; want executable %v", name, data, mode, err, exec)
		}
	}
	_, _, err := r.ReadFileMode("link.pem", 100)
	wantErr(t, err, "not a regular file")
	_, _, err = r.ReadFileMode("run.sh", 4)
	wantErr(t, err, "larger than 4 bytes")
}

func TestSymlinkedRootTakesBothSpellings(t *testing.T) {
	_, base, dir, _ := setup(t)
	link := filepath.Join(base, "link")
	symlink(t, dir, link)
	r, err := Open(link)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()

	for _, name := range []string{filepath.Join(link, "sub", "body.md"), filepath.Join(dir, "sub", "body.md")} {
		if got, err := r.ReadFile(name, 5); err != nil || string(got) != "hello" {
			t.Errorf("ReadFile(%q) = %q, %v; want hello", name, got, err)
		}
	}
	_, err = r.ReadFile(filepath.Join(base, "other.md"), 5)
	wantErr(t, err, "outside "+link)
}

func TestReadInput(t *testing.T) {
	r, _, _, _ := setup(t)
	got, err := r.ReadInput("-", strings.NewReader("from stdin"), 10)
	if err != nil || string(got) != "from stdin" {
		t.Errorf("ReadInput(-) = %q, %v; want from stdin", got, err)
	}
	_, err = r.ReadInput("-", strings.NewReader("from stdin"), 9)
	wantErr(t, err, "standard input: larger than 9 bytes")
	if got, err := r.ReadInput("sub/body.md", nil, 5); err != nil || string(got) != "hello" {
		t.Errorf("ReadInput(file) = %q, %v; want hello", got, err)
	}
}

func TestWriteFile(t *testing.T) {
	r, _, dir, secret := setup(t)
	symlink(t, secret, filepath.Join(dir, "link.pem"))
	symlink(t, filepath.Dir(secret), filepath.Join(dir, "updir"))

	if err := r.WriteFile("new/deep/out.txt", []byte("one")); err != nil {
		t.Fatal(err)
	}
	if err := r.WriteFile(filepath.Join(dir, "new", "deep", "out.txt"), []byte("two")); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "new", "deep", "out.txt")); err != nil || string(got) != "two" { //nolint:gosec // G304: a path under the test temp dir.
		t.Errorf("file = %q, %v; want two", got, err)
	}
	// A shorter write replaces the whole file, not just its start.
	if err := r.WriteFile("sub/body.md", []byte("hi")); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "sub", "body.md")); err != nil || string(got) != "hi" { //nolint:gosec // G304: a path under the test temp dir.
		t.Errorf("file = %q, %v; want hi", got, err)
	}

	tests := []struct{ name, want string }{
		{"../out.txt", "outside"},
		{"link.pem", "not a regular file"},
		{"updir/out.txt", "escapes"},
		{"sub", "not a regular file"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wantErr(t, r.WriteFile(tt.name, []byte("x")), tt.want)
		})
	}
	if got, _ := os.ReadFile(secret); string(got) != "key" { //nolint:gosec // G304: a path under the test temp dir.
		t.Errorf("secret = %q, want it untouched", got)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(secret), "out.txt")); err == nil {
		t.Error("a file was written outside the root")
	}
}
