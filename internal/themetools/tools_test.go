package themetools

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExpectedSum(t *testing.T) {
	sum := strings.Repeat("a", 64)
	sums := sum + "  tringify-theme-tools-linux-amd64.zip\n" + strings.Repeat("b", 64) + " *tringify-theme-tools-darwin-arm64.zip\n"
	if got, err := ExpectedSum(sums, "tringify-theme-tools-linux-amd64.zip"); err != nil || got != sum {
		t.Fatalf("%s %v", got, err)
	}
	if got, err := ExpectedSum(sums, "tringify-theme-tools-darwin-arm64.zip"); err != nil || got != strings.Repeat("b", 64) {
		t.Fatalf("%s %v", got, err)
	}
	if _, err := ExpectedSum(sums, "tringify-theme-tools-windows-amd64.zip"); err == nil {
		t.Fatal("missing checksum accepted")
	}
}

func TestUnzipRefusesPathsOutsideTheDestination(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.zip")
	f, _ := os.Create(path)
	w := zip.NewWriter(f)
	entry, _ := w.Create("../escape.txt")
	entry.Write([]byte("x"))
	w.Close()
	f.Close()
	if err := unzip(path, filepath.Join(dir, "out")); err == nil {
		t.Fatal("path traversal accepted")
	}
	if _, err := os.Stat(filepath.Join(dir, "escape.txt")); err == nil {
		t.Fatal("file written outside the destination")
	}
}
