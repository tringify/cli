// Package themekit builds, validates, packages and previews Tringify themes.
//
// It is a Go port of the Python theme tools (github.com/tringify/theme-tools)
// and keeps their file layout, rules and messages. Validation and preview
// rendering run the prebuilt themecheck and theme-preview-render binaries.
package themekit

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"context"
	"errors"
	"fmt"
	"hash/crc32"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// BundleDirectories and BundleFiles make up a runtime theme package.
var (
	BundleDirectories = []string{"assets", "blocks", "config", "demo", "locales", "sections", "templates"}
	BundleFiles       = []string{"theme.json", "tokens.json"}
)

const dsStore = ".DS_Store"

// Tools locates the prebuilt binaries. Each function is called only when a
// command needs that binary, so a failed build never triggers a download.
type Tools struct {
	Checker  func(ctx context.Context) (string, error)
	Renderer func(ctx context.Context) (string, error)
}

// isDSStore reports whether path is Finder metadata: a regular file (not a
// link or directory) named .DS_Store.
func isDSStore(path string) bool {
	if filepath.Base(path) != dsStore {
		return false
	}
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular()
}

// rel returns path relative to root for messages, in OS form.
func rel(root, path string) string {
	r, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return r
}

// bundlePaths lists the files that go into the runtime archive, sorted by
// their slash-separated relative path.
func bundlePaths(root string) ([]string, error) {
	var missing []string
	for _, name := range BundleFiles {
		if info, err := os.Stat(filepath.Join(root, name)); err != nil || !info.Mode().IsRegular() {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing runtime bundle file: %s", strings.Join(missing, ", "))
	}
	seen := map[string]bool{}
	var paths []string
	add := func(p string) {
		if !seen[p] {
			seen[p] = true
			paths = append(paths, p)
		}
	}
	for _, dir := range BundleDirectories {
		base := filepath.Join(root, dir)
		info, err := os.Lstat(base)
		if err != nil {
			continue
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			if target, err := os.Stat(base); err == nil && target.IsDir() {
				return nil, fmt.Errorf("symlink outside package contract: %s", base)
			}
			continue
		}
		if !info.IsDir() {
			continue
		}
		err = filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if path == base {
				return nil
			}
			if d.Type()&fs.ModeSymlink != 0 {
				// A link to a file would be packaged as that file; refuse it.
				if target, err := os.Stat(path); err == nil && (target.Mode().IsRegular() || target.IsDir()) {
					return fmt.Errorf("symlink outside package contract: %s", path)
				}
				return nil
			}
			if d.Type().IsRegular() && !isDSStore(path) {
				add(path)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	for _, name := range BundleFiles {
		path := filepath.Join(root, name)
		if info, err := os.Lstat(path); err == nil && info.Mode()&fs.ModeSymlink != 0 {
			return nil, fmt.Errorf("symlink outside package contract: %s", path)
		}
		add(path)
	}
	sort.Slice(paths, func(i, j int) bool {
		return filepath.ToSlash(rel(root, paths[i])) < filepath.ToSlash(rel(root, paths[j]))
	})
	return paths, nil
}

// msDosDate1980 is 1980-01-01 00:00:00, the fixed timestamp of every entry.
const msDosDate1980 = 1<<5 | 1

// WriteArchive writes the deterministic runtime archive for root and
// returns the number of files in it.
func WriteArchive(root, output string) (int, error) {
	root, err := resolvePath(root)
	if err != nil {
		return 0, err
	}
	paths, err := bundlePaths(root)
	if err != nil {
		return 0, err
	}
	f, err := os.Create(output)
	if err != nil {
		return 0, err
	}
	w := zip.NewWriter(f)
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			f.Close()
			return 0, err
		}
		var compressed bytes.Buffer
		fw, _ := flate.NewWriter(&compressed, flate.BestCompression)
		fw.Write(data)
		fw.Close()
		header := &zip.FileHeader{
			Name:               filepath.ToSlash(rel(root, path)),
			Method:             zip.Deflate,
			ModifiedDate:       msDosDate1980,
			CreatorVersion:     3<<8 | 20, // Unix, ZIP 2.0
			ReaderVersion:      20,
			ExternalAttrs:      0o100644 << 16,
			CRC32:              crc32.ChecksumIEEE(data),
			CompressedSize64:   uint64(compressed.Len()),
			UncompressedSize64: uint64(len(data)),
		}
		for i := 0; i < len(header.Name); i++ {
			if header.Name[i] >= 0x80 {
				header.Flags |= 0x800 // UTF-8 name
				break
			}
		}
		entry, err := w.CreateRaw(header)
		if err != nil {
			f.Close()
			return 0, err
		}
		if _, err := entry.Write(compressed.Bytes()); err != nil {
			f.Close()
			return 0, err
		}
	}
	if err := w.Close(); err != nil {
		f.Close()
		return 0, err
	}
	return len(paths), f.Close()
}

// resolvePath is Python's Path.resolve(strict=False): absolute, with every
// existing symlink in the path resolved.
func resolvePath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved, nil
	}
	parent := filepath.Dir(abs)
	if parent == abs {
		return abs, nil
	}
	resolvedParent, err := resolvePath(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolvedParent, filepath.Base(abs)), nil
}

// within reports whether path is base or inside it.
func within(path, base string) bool {
	r, err := filepath.Rel(base, path)
	return err == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator)) && !filepath.IsAbs(r)
}

func isSymlink(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode()&fs.ModeSymlink != 0
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// errorf keeps OS errors short: "open src/_shared.css: no such file or
// directory" rather than the absolute temporary path of a staged copy.
func osError(root string, err error) error {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		return fmt.Errorf("%s %s: %w", pathErr.Op, rel(root, pathErr.Path), pathErr.Err)
	}
	return err
}
