// Package rootfs is the one way ternly reads files it loads by itself, as
// opposed to files a tool call asks for (tools.Registry): instruction files,
// rules, commands, skills, agent definitions, plugin files, project MCP
// config, and source files for the code graph. Every read goes through an
// os.Root opened on the directory that owns the file — the workspace, a
// plugin's directory, the user's home for personal files — so neither ".."
// nor a symlink (in the file or in any directory above it) can make ternly
// read outside it. A repository can't put ~/.ssh/id_rsa into a prompt by
// naming it, linking it, or linking a directory that leads to it.
package rootfs

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// MaxFile is the largest file loaded this way.
const MaxFile = 4 << 20

// ErrOutside is a path that names something outside the directory.
var ErrOutside = errors.New("outside the directory it belongs to")

// Dir is a directory files are read from.
type Dir struct {
	Path string // absolute, cleaned
	root *os.Root
}

// Open opens dir. The directory itself may be reached through symlinks (a
// workspace under a linked home is fine); nothing below it may leave it.
func Open(dir string) (*Dir, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	r, err := os.OpenRoot(abs)
	if err != nil {
		return nil, err
	}
	return &Dir{Path: filepath.Clean(abs), root: r}, nil
}

// Close releases the directory.
func (d *Dir) Close() error { return d.root.Close() }

// rel turns a path (relative to the directory, or absolute inside it) into
// one os.Root accepts.
func (d *Dir) rel(p string) (string, error) {
	if filepath.IsAbs(p) {
		r, err := filepath.Rel(d.Path, filepath.Clean(p))
		if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
			return "", ErrOutside
		}
		p = r
	}
	if p == "" {
		p = "."
	}
	return filepath.Clean(p), nil
}

// ReadFile reads a regular file inside the directory.
func (d *Dir) ReadFile(p string) ([]byte, error) {
	r, err := d.rel(p)
	if err != nil {
		return nil, err
	}
	fi, err := d.root.Stat(r) // follows symlinks, but only within the root
	if err != nil {
		return nil, wrap(err)
	}
	if !fi.Mode().IsRegular() {
		return nil, errors.New(p + ": not a regular file")
	}
	if fi.Size() > MaxFile {
		return nil, errors.New(p + ": too large to load")
	}
	b, err := d.root.ReadFile(r)
	return b, wrap(err)
}

// Exists reports whether p is a file or directory inside the directory.
func (d *Dir) Exists(p string) bool {
	r, err := d.rel(p)
	if err != nil {
		return false
	}
	_, err = d.root.Stat(r)
	return err == nil
}

// Stat describes p (symlinks followed within the directory only).
func (d *Dir) Stat(p string) (fs.FileInfo, error) {
	r, err := d.rel(p)
	if err != nil {
		return nil, err
	}
	fi, err := d.root.Stat(r)
	return fi, wrap(err)
}

// WalkDir walks the tree under p (relative or absolute inside the
// directory). Paths given to fn are absolute. Symlinks are not followed, as
// with filepath.WalkDir, and the walk can't start outside the directory.
func (d *Dir) WalkDir(p string, fn fs.WalkDirFunc) error {
	r, err := d.rel(p)
	if err != nil {
		return err
	}
	if fi, err := d.root.Lstat(r); err == nil && fi.Mode()&fs.ModeSymlink != 0 {
		return nil // a linked directory isn't walked into
	}
	return fs.WalkDir(d.root.FS(), filepath.ToSlash(r), func(q string, e fs.DirEntry, err error) error {
		return fn(filepath.Join(d.Path, filepath.FromSlash(q)), e, err)
	})
}

// ReadFile reads path, confined to dir: the one-shot form.
func ReadFile(dir, path string) ([]byte, error) {
	d, err := Open(dir)
	if err != nil {
		return nil, err
	}
	defer d.Close()
	return d.ReadFile(path)
}

// wrap names an escape (os.Root's "path escapes from parent") plainly.
func wrap(err error) error {
	if err != nil && strings.Contains(err.Error(), "escapes from parent") {
		return ErrOutside
	}
	return err
}

// ReadDir lists a directory inside the directory (entries aren't followed).
func (d *Dir) ReadDir(p string) ([]fs.DirEntry, error) {
	r, err := d.rel(p)
	if err != nil {
		return nil, err
	}
	if fi, err := d.root.Lstat(r); err == nil && fi.Mode()&fs.ModeSymlink != 0 {
		return nil, ErrOutside // not through a linked directory
	}
	es, err := fs.ReadDir(d.root.FS(), filepath.ToSlash(r))
	return es, wrap(err)
}
