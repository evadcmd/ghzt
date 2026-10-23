package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setup makes base/user as a root with a file in it, and a secret file next
// to base, outside every root.
func setup(t *testing.T) (r *Root, base, dir, secret string) {
	t.Helper()
	tmp := t.TempDir()
	base = filepath.Join(tmp, "workspace")
	dir = filepath.Join(base, "user")
	secret = filepath.Join(tmp, "secret.pem")
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
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
	r, err := Open(base, dir)
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
	_, base, dir, secret := setup(t)
	outside := filepath.Dir(secret)
	symlink(t, outside, filepath.Join(base, "escape"))

	for _, d := range []string{base, dir} {
		r, err := Open(base, d)
		if err != nil {
			t.Errorf("Open(%q) = %v, want ok", d, err)
			continue
		}
		_ = r.Close()
	}
	_, err := Open(base, "workspace/user")
	wantErr(t, err, "not an absolute path")
	_, err = Open(base, outside)
	wantErr(t, err, "is not inside")
	_, err = Open(base, base+"/user/../..")
	wantErr(t, err, "is not inside")
	_, err = Open(base, filepath.Join(base, "escape"))
	wantErr(t, err, "is not inside")
}

func TestFromEnv(t *testing.T) {
	_, err := FromEnv(func(string) string { return "" })
	wantErr(t, err, EnvRoot+" is not set")
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
	if got, err := os.ReadFile(filepath.Join(dir, "new", "deep", "out.txt")); err != nil || string(got) != "two" {
		t.Errorf("file = %q, %v; want two", got, err)
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
	if got, _ := os.ReadFile(secret); string(got) != "key" {
		t.Errorf("secret = %q, want it untouched", got)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(secret), "out.txt")); err == nil {
		t.Error("a file was written outside the root")
	}
}
