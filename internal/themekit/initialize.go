package themekit

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/tringify/cli/internal/pyjson"
)

const (
	maxSourceFiles = 2000
	maxSourceBytes = 100 * 1024 * 1024
)

// Gitignore is written into every new theme.
const Gitignore = "dist/\n.DS_Store\n__pycache__/\n.env\n.env.*\n"

var sourceParts = map[string]bool{"body.html": true, "style.css": true, "schema.json": true}

// sourceFiles lists what a new theme copies from its source: the runtime
// bundle, editable section sources, and README, LICENSE and NOTICE files.
func sourceFiles(root string) ([]string, error) {
	var files []string
	var collect func(path string) error
	collect = func(path string) error {
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("theme source symlinks are not supported: %s", rel(root, path))
		}
		info, err = os.Stat(path)
		if err != nil {
			return err
		}
		switch {
		case info.IsDir():
			entries, err := os.ReadDir(path)
			if err != nil {
				return err
			}
			names := make([]string, len(entries))
			for i, e := range entries {
				names[i] = e.Name()
			}
			sortNames(names)
			for _, name := range names {
				if err := collect(filepath.Join(path, name)); err != nil {
					return err
				}
			}
		case info.Mode().IsRegular():
			if isDSStore(path) {
				return nil
			}
			files = append(files, path)
			if len(files) > maxSourceFiles {
				return errors.New("theme source exceeds the 2000-file initialization limit")
			}
		default:
			return fmt.Errorf("theme source must contain regular files: %s", rel(root, path))
		}
		return nil
	}
	names := append(append(append([]string{}, BundleDirectories...), BundleFiles...), "src")
	for _, name := range names {
		path := filepath.Join(root, name)
		if exists(path) || isSymlink(path) {
			if err := collect(path); err != nil {
				return nil, err
			}
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var top []string
	for _, e := range entries {
		n := e.Name()
		if n == "README.md" || n == "NOTICE" || n == "LICENSE" || strings.HasPrefix(n, "LICENSE.") || strings.HasPrefix(n, "NOTICE.") {
			top = append(top, n)
		}
	}
	sortNames(top)
	for _, n := range top {
		if err := collect(filepath.Join(root, n)); err != nil {
			return nil, err
		}
	}
	var total int64
	for _, path := range files {
		parts := strings.Split(filepath.ToSlash(rel(root, path)), "/")
		if parts[0] == "src" {
			ok := (len(parts) == 2 && (parts[1] == "_shared.css" || parts[1] == ".generated-sections.json")) ||
				(len(parts) == 4 && parts[1] == "sections" && sourceParts[parts[3]])
			if !ok {
				return nil, fmt.Errorf("unsupported editable source file: %s", rel(root, path))
			}
		}
	}
	for _, path := range files {
		info, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		total += info.Size()
	}
	if total > maxSourceBytes {
		return nil, errors.New("theme source exceeds the 100 MiB initialization limit")
	}
	return files, nil
}

// ValidName checks a theme display name and returns it trimmed.
func ValidName(name string) (string, error) {
	name = pyStrip(name)
	n := utf8.RuneCountInString(name)
	bad := n < 1 || n > 100
	for _, r := range name {
		if r < 32 || r == 127 {
			bad = true
		}
	}
	if bad {
		return "", errors.New("theme name must be 1–100 characters without control characters")
	}
	return name, nil
}

// Initialize creates destination as an editable copy of the local theme
// source, named name, built and validated. Nothing is created if any step
// fails, and the source is never modified. It returns the section count.
func Initialize(ctx context.Context, source, destination, name string, tools Tools) (int, error) {
	name, err := ValidName(name)
	if err != nil {
		return 0, err
	}
	if info, err := os.Stat(source); isSymlink(source) || err != nil || !info.IsDir() {
		return 0, errors.New("source must be a local theme directory, not a symlink")
	}
	if source, err = resolvePath(source); err != nil {
		return 0, err
	}
	if destination, err = filepath.Abs(destination); err != nil {
		return 0, err
	}
	if exists(destination) || isSymlink(destination) {
		return 0, errors.New("destination already exists; choose a new directory")
	}
	if !isDir(filepath.Dir(destination)) {
		return 0, errors.New("destination parent directory must already exist")
	}
	parent, err := resolvePath(filepath.Dir(destination))
	if err != nil {
		return 0, err
	}
	destination = filepath.Join(parent, filepath.Base(destination))
	if within(destination, source) {
		return 0, errors.New("destination must be outside the source theme")
	}

	files, err := sourceFiles(source)
	if err != nil {
		return 0, err
	}
	temporary, err := os.MkdirTemp(parent, ".tringify-theme-init-")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(temporary)
	staged := filepath.Join(temporary, "theme")
	if err := os.Mkdir(staged, 0o777); err != nil {
		return 0, err
	}
	for _, path := range files {
		out := filepath.Join(staged, rel(source, path))
		if err := os.MkdirAll(filepath.Dir(out), 0o777); err != nil {
			return 0, err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return 0, err
		}
		// Copy bytes only. A source executable never becomes an executable hook.
		if err := os.WriteFile(out, data, 0o644); err != nil {
			return 0, err
		}
		if err := os.Chmod(out, 0o644); err != nil {
			return 0, err
		}
	}
	manifestPath := filepath.Join(staged, "theme.json")
	text, err := readText(staged, manifestPath)
	if err != nil {
		return 0, err
	}
	value, err := pyjson.Loads(text)
	if err != nil {
		return 0, err
	}
	manifest, ok := value.(*pyjson.Object)
	if !ok {
		return 0, errors.New("theme.json must contain an object")
	}
	manifest.Set("name", pyjson.String(name))
	manifest.Set("status", pyjson.String("draft"))
	if err := os.WriteFile(manifestPath, []byte(pyjson.Dumps(manifest)+"\n"), 0o644); err != nil {
		return 0, err
	}
	if isDir(filepath.Join(staged, "src", "sections")) {
		if _, err := Build(staged); err != nil {
			return 0, err
		}
	}
	archive := filepath.Join(temporary, "theme.zip")
	if _, err := WriteArchive(staged, archive); err != nil {
		return 0, err
	}
	if _, err := CheckArchive(ctx, archive, "sealed", tools); err != nil {
		return 0, err
	}
	count := 0
	if built, err := readJSON(manifestPath, staged); err == nil {
		if obj, ok := built.(*pyjson.Object); ok {
			if sections, ok := obj.Get("sections"); ok {
				if list, ok := sections.([]pyjson.Value); ok {
					count = len(list)
				}
			}
		}
	}
	if err := os.WriteFile(filepath.Join(staged, ".gitignore"), []byte(Gitignore), 0o644); err != nil {
		return 0, err
	}

	// Reserve the destination exclusively after validation. Unlike rename,
	// mkdir cannot replace an empty directory created by someone else.
	if err := os.Mkdir(destination, 0o777); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return 0, errors.New("destination already exists; choose a new directory")
		}
		return 0, err
	}
	children, err := os.ReadDir(staged)
	if err != nil {
		os.RemoveAll(destination)
		return 0, err
	}
	for _, child := range children {
		if err := os.Rename(filepath.Join(staged, child.Name()), filepath.Join(destination, child.Name())); err != nil {
			os.RemoveAll(destination)
			return 0, err
		}
	}
	return count, nil
}
