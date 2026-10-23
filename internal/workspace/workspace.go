// Package workspace confines the files ghzt reads and writes (--body-file,
// -o, commit paths) to one directory, GHZT_ROOT, inside /workspace.
//
// GHZT_ROOT is where the caller's files live: /workspace today, and
// /workspace/<user_id> once each user gets their own directory. Commands take
// paths relative to it, or absolute paths inside it.
package workspace

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

const (
	// Base is the directory every root has to be in.
	Base = "/workspace"
	// EnvRoot names the root directory.
	EnvRoot = "GHZT_ROOT"
)

// Root is a directory files are read from and written to. Nothing outside
// it can be reached, through ".." or symlinks alike (os.Root), and a symlink
// as the file itself is refused, so what is read or written is always a
// regular file in the root.
type Root struct {
	dir  string
	root *os.Root
}

// FromEnv opens the root named by GHZT_ROOT, read with getenv.
func FromEnv(getenv func(string) string) (*Root, error) {
	dir := getenv(EnvRoot)
	if dir == "" {
		return nil, fmt.Errorf("%s is not set", EnvRoot)
	}
	return Open(Base, dir)
}

// Open opens dir as a root. dir must be absolute and, with symlinks
// resolved, be base or inside it.
func Open(base, dir string) (*Root, error) {
	if !filepath.IsAbs(dir) {
		return nil, fmt.Errorf("root %q is not an absolute path", dir)
	}
	realBase, err := filepath.EvalSymlinks(base)
	if err != nil {
		return nil, err
	}
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, err
	}
	if rel, err := filepath.Rel(realBase, realDir); err != nil || !filepath.IsLocal(rel) {
		return nil, fmt.Errorf("root %q is not inside %s", dir, base)
	}
	root, err := os.OpenRoot(realDir)
	if err != nil {
		return nil, err
	}
	return &Root{dir: filepath.Clean(dir), root: root}, nil
}

// Close releases the root's directory handle.
func (r *Root) Close() error { return r.root.Close() }

// rel returns name relative to the root. name is relative to the root
// already, or absolute and inside it.
func (r *Root) rel(name string) (string, error) {
	rel := name
	if filepath.IsAbs(name) {
		var err error
		if rel, err = filepath.Rel(r.dir, name); err != nil {
			rel = ".."
		}
	}
	if !filepath.IsLocal(rel) {
		return "", fmt.Errorf("%s: outside %s", name, r.dir)
	}
	return filepath.Clean(rel), nil
}

// ReadFile reads the regular file name, failing if it is larger than max
// bytes.
func (r *Root) ReadFile(name string, maxBytes int64) ([]byte, error) {
	rel, err := r.rel(name)
	if err != nil {
		return nil, err
	}
	info, err := r.root.Lstat(rel)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s: not a regular file", name)
	}
	if info.Size() > maxBytes {
		return nil, fmt.Errorf("%s: larger than %d bytes", name, maxBytes)
	}
	f, err := r.root.Open(rel)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	// Open follows a symlink, so make sure it is still the file Lstat saw.
	if opened, err := f.Stat(); err != nil || !os.SameFile(info, opened) {
		return nil, fmt.Errorf("%s: changed while being opened", name)
	}
	return readLimited(name, f, maxBytes)
}

// ReadInput reads name like ReadFile, or standard input from stdin if name
// is "-", the way gh's --body-file does.
func (r *Root) ReadInput(name string, stdin io.Reader, maxBytes int64) ([]byte, error) {
	if name == "-" {
		return readLimited("standard input", stdin, maxBytes)
	}
	return r.ReadFile(name, maxBytes)
}

func readLimited(name string, src io.Reader, maxBytes int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(src, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("%s: larger than %d bytes", name, maxBytes)
	}
	return data, nil
}

// WriteFile writes data to name, creating missing parent directories and
// replacing the file if it exists. A symlink at name is refused rather than
// followed.
func (r *Root) WriteFile(name string, data []byte) error {
	rel, err := r.rel(name)
	if err != nil {
		return err
	}
	if err := r.root.MkdirAll(filepath.Dir(rel), 0o755); err != nil {
		return err
	}
	info, err := r.root.Lstat(rel)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return err
	case !info.Mode().IsRegular():
		return fmt.Errorf("%s: not a regular file", name)
	}
	return r.root.WriteFile(rel, data, 0o644)
}
