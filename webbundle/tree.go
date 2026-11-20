package webbundle

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/cplieger/go/httpserver/v2"
)

const gitignore = ".gitignore"

// tree is the temporary directory a build writes beside Out.
type tree struct {
	root *os.Root
	dir  string
	out  string
}

// tempPrefix names Out's temporary trees, so a build sweeps the ones an
// interrupted build left beside Out.
func tempPrefix(out string) string { return "." + filepath.Base(out) + ".webbundle-" }

func newTree(out string) (*tree, error) {
	parent := filepath.Dir(out)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return nil, fmt.Errorf("webbundle: %w", err)
	}
	if err := sweepTemps(parent, tempPrefix(out)); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(parent, tempPrefix(out)+"*")
	if err != nil {
		return nil, fmt.Errorf("webbundle: %w", err)
	}
	// MkdirTemp makes the directory 0700 and it becomes Out.
	if cerr := os.Chmod(dir, 0o755); cerr != nil {
		return nil, errors.Join(fmt.Errorf("webbundle: %w", cerr), os.RemoveAll(dir))
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("webbundle: %w", err), os.RemoveAll(dir))
	}
	return &tree{root: root, dir: dir, out: out}, nil
}

func sweepTemps(parent, prefix string) error {
	entries, err := os.ReadDir(parent)
	if err != nil {
		return fmt.Errorf("webbundle: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), prefix) {
			if err := os.RemoveAll(filepath.Join(parent, e.Name())); err != nil {
				return fmt.Errorf("webbundle: remove an earlier build's temporary tree: %w", err)
			}
		}
	}
	return nil
}

// discard removes the temporary tree; Out is untouched.
func (t *tree) discard() error {
	return errors.Join(t.root.Close(), os.RemoveAll(t.dir))
}

// write writes one file at name, a slash path, under the tree.
func (t *tree) write(name string, body []byte, perm fs.FileMode) error {
	if dir := path.Dir(name); dir != "." {
		if err := t.root.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("webbundle: %w", err)
		}
	}
	if err := t.root.WriteFile(name, body, perm); err != nil {
		return fmt.Errorf("webbundle: %w", err)
	}
	return nil
}

// writeHashed writes body as assets/<stem>-<hash><ext>, the hash being the
// first 8 bytes of its SHA-256 in hex, and returns that path. The same
// bytes written twice under one stem and extension are one file; other
// bytes whose name and 8-byte hash prefix match are refused, never written
// over.
func (t *tree) writeHashed(stem, ext string, body []byte) (string, error) {
	sum := sha256.Sum256(body)
	name := "assets/" + stem + "-" + hex.EncodeToString(sum[:8]) + ext
	if have, err := t.root.ReadFile(name); err == nil {
		if bytes.Equal(have, body) {
			return name, nil
		}
		return "", fmt.Errorf("webbundle: two files are %s", name)
	}
	return name, t.write(name, body, 0o644)
}

// keepGitignore copies Out/.gitignore byte for byte into the tree, so the
// swap neither deletes nor rewrites the file an app commits to keep Out in a
// clean clone; it writes none when Out has none.
func (t *tree) keepGitignore() error {
	p := filepath.Join(t.out, gitignore)
	info, err := os.Lstat(p)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return fmt.Errorf("webbundle: %w", err)
	case !info.Mode().IsRegular():
		return fmt.Errorf("webbundle: %s is not a regular file", p)
	}
	body, err := os.ReadFile(p)
	if err != nil {
		return fmt.Errorf("webbundle: %w", err)
	}
	return t.write(gitignore, body, info.Mode().Perm())
}

// manifest writes bundle.json, listing every file in the tree but itself and
// .gitignore with its SHA-256, and returns every file's path, sorted. It
// refuses a tree over maxOutput, a file outside licenses/ whose extension
// httpserver does not serve (SPA serves every notice as text), and a tree
// httpserver.ReadBundle refuses, so Build never swaps in one SPA cannot
// serve.
func (t *tree) manifest(entries []httpserver.BundleEntry) ([]string, error) {
	files := map[string]string{}
	var total int64
	types := httpserver.ContentTypes()
	err := fs.WalkDir(t.root.FS(), ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || name == gitignore {
			return err
		}
		if !strings.HasPrefix(name, licensesDir) && types[path.Ext(name)] == "" {
			return fmt.Errorf("webbundle: the build wrote %s, whose extension httpserver does not serve", name)
		}
		body, err := fs.ReadFile(t.root.FS(), name)
		if err != nil {
			return err
		}
		if total += int64(len(body)); total > maxOutput {
			return fmt.Errorf("webbundle: the build writes more than %d MiB", maxOutput>>20)
		}
		sum := sha256.Sum256(body)
		files[name] = hex.EncodeToString(sum[:])
		return nil
	})
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(httpserver.Bundle{Files: files, Entries: entries}); err != nil {
		return nil, fmt.Errorf("webbundle: %s: %w", httpserver.BundlePath, err)
	}
	if err := t.write(httpserver.BundlePath, b.Bytes(), 0o644); err != nil {
		return nil, err
	}
	if _, _, err := httpserver.ReadBundle(t.root.FS()); err != nil {
		return nil, fmt.Errorf("webbundle: httpserver.SPA would refuse the built tree: %w", err)
	}
	names := append(slices.Collect(maps.Keys(files)), httpserver.BundlePath)
	slices.Sort(names)
	return names, nil
}

// swap replaces Out with the tree by two renames beside it, the old tree
// moved aside first, so Out holds the last good build until the new one is
// whole; the old tree is then removed.
func (t *tree) swap() error {
	if err := t.root.Close(); err != nil {
		return errors.Join(fmt.Errorf("webbundle: %w", err), os.RemoveAll(t.dir))
	}
	old := t.dir + ".old"
	err := os.Rename(t.out, old)
	if errors.Is(err, fs.ErrNotExist) {
		if rerr := os.Rename(t.dir, t.out); rerr != nil {
			return errors.Join(fmt.Errorf("webbundle: %w", rerr), os.RemoveAll(t.dir))
		}
		return nil
	}
	if err != nil {
		return errors.Join(fmt.Errorf("webbundle: move the last tree aside: %w", err), os.RemoveAll(t.dir))
	}
	if err := os.Rename(t.dir, t.out); err != nil {
		return errors.Join(fmt.Errorf("webbundle: %w", err), os.Rename(old, t.out), os.RemoveAll(t.dir))
	}
	if err := os.RemoveAll(old); err != nil {
		return fmt.Errorf("webbundle: remove the last tree: %w", err)
	}
	return nil
}
