package themekit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// copyTree copies a theme directory into the staging area. Links are
// refused rather than followed, Finder metadata is skipped, and anything
// other than regular files and directories is an error.
func copyTree(source, dest string) error {
	return filepath.WalkDir(source, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(dest, rel(source, path))
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			return errors.New("theme source symlinks are not supported")
		case d.IsDir():
			return os.MkdirAll(target, 0o777)
		case d.Type().IsRegular():
			if isDSStore(path) {
				return nil
			}
			return copyFile(path, target)
		}
		return fmt.Errorf("%s is not a regular file", path)
	})
}

func copyFile(source, target string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	return err
}

// Package builds the section sources in an isolated copy, validates the
// result and atomically writes the runtime archive to output. A failed
// build or validation never replaces an existing package. It returns the
// number of files in the archive.
func Package(ctx context.Context, root, output, mode string, tools Tools) (int, error) {
	root, err := resolvePath(root)
	if err != nil {
		return 0, err
	}
	output, err = resolvePath(output)
	if err != nil {
		return 0, err
	}
	if output == root || (within(output, root) && strings.Split(filepath.ToSlash(rel(root, output)), "/")[0] != "dist") {
		return 0, errors.New("outputs inside the source tree must be under dist/")
	}
	dir, err := os.MkdirTemp("", "tringify-theme-package-")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(dir)
	staged := filepath.Join(dir, "source")
	if err := os.Mkdir(staged, 0o777); err != nil {
		return 0, err
	}
	names := append(append(append([]string{}, BundleDirectories...), "src"), BundleFiles...)
	for _, name := range names {
		source := filepath.Join(root, name)
		if isSymlink(source) {
			return 0, fmt.Errorf("theme source symlinks are not supported: %s", name)
		}
		info, err := os.Stat(source)
		if err != nil {
			continue
		}
		if info.IsDir() {
			if err := copyTree(source, filepath.Join(staged, name)); err != nil {
				return 0, err
			}
		} else if info.Mode().IsRegular() {
			if err := copyFile(source, filepath.Join(staged, name)); err != nil {
				return 0, err
			}
		}
	}
	if isDir(filepath.Join(staged, "src", "sections")) {
		if _, err := Build(staged); err != nil {
			return 0, err
		}
	}
	archive := filepath.Join(dir, "theme.zip")
	count, err := WriteArchive(staged, archive)
	if err != nil {
		return 0, err
	}
	if _, err := CheckArchive(ctx, archive, mode, tools); err != nil {
		return 0, err
	}
	if err := os.MkdirAll(filepath.Dir(output), 0o777); err != nil {
		return 0, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(output), ".theme-*.zip")
	if err != nil {
		return 0, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	in, err := os.Open(archive)
	if err != nil {
		tmp.Close()
		return 0, err
	}
	_, err = io.Copy(tmp, in)
	in.Close()
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return 0, err
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return 0, err
	}
	if err := os.Rename(tmpName, output); err != nil {
		return 0, err
	}
	return count, nil
}
