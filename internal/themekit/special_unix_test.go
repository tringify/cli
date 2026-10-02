//go:build !windows

package themekit

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestInitRejectsNonRegularFilesWithoutReadingThem(t *testing.T) {
	f := newInit(t)
	os.MkdirAll(filepath.Join(f.source, "assets"), 0o755)
	if err := syscall.Mkfifo(filepath.Join(f.source, "assets/wait.png"), 0o644); err != nil {
		t.Skip(err)
	}
	expectError(t, f.run("X"), "regular files")
	if exists(f.destination) {
		t.Fatal("destination created")
	}
}

func TestFinderMetadataNamedFIFOIsRejectedWithoutReading(t *testing.T) {
	f := newInit(t)
	os.MkdirAll(filepath.Join(f.source, "assets"), 0o755)
	if err := syscall.Mkfifo(filepath.Join(f.source, "assets/.DS_Store"), 0o644); err != nil {
		t.Skip(err)
	}
	expectError(t, f.run("X"), "regular files")
	if _, err := Package(bg, f.source, filepath.Join(f.root, "theme.zip"), "sealed", f.tools); err == nil {
		t.Fatal("package read a FIFO")
	}
}
