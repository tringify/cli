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

func TestExtractKeepsOnlyTheNamedFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tools.zip")
	f, _ := os.Create(path)
	w := zip.NewWriter(f)
	for _, name := range []string{"../escape.txt", "top/../../escape.txt", "top/themecheck", "top/theme.py", "other/theme-preview-render"} {
		entry, _ := w.Create(name)
		entry.Write([]byte(name))
	}
	w.Close()
	f.Close()
	out := filepath.Join(dir, "out")
	if err := extract(path, out, "top", []string{"themecheck", "theme-preview-render"}); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(out)
	if len(entries) != 1 || entries[0].Name() != "themecheck" {
		t.Fatalf("extracted %v", entries)
	}
	if _, err := os.Stat(filepath.Join(dir, "escape.txt")); err == nil {
		t.Fatal("file written outside the destination")
	}
}
