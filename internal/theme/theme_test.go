package theme

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidVersionAndChangelog(t *testing.T) {
	for _, v := range []string{"1.0.0", "0.3.12", "2.0.0-beta.1"} {
		if !ValidVersion(v) {
			t.Errorf("%s rejected", v)
		}
	}
	for _, v := range []string{"1.0", "v1.0.0", "01.0.0", "1.0.0 "} {
		if ValidVersion(v) {
			t.Errorf("%q accepted", v)
		}
	}
	if got := Changelog(" Fixes. ", "abc"); got != "Fixes.\n\nSource commit: abc" {
		t.Fatalf("changelog = %q", got)
	}
	if Slug("My Theme!") != "my-theme" || Slug("***") != "theme" {
		t.Fatal("slug")
	}
}

func TestGitSourceRefusesUncommittedWork(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	git := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.test"}, args...)...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	git("init", "-q")
	if _, err := GitSource(context.Background(), dir); err == nil {
		t.Fatal("repository without commits accepted")
	}
	os.WriteFile(filepath.Join(dir, "theme.json"), []byte(`{"name":"T"}`), 0o644)
	git("add", ".")
	git("commit", "-q", "-m", "init")
	s, err := GitSource(context.Background(), dir)
	if err != nil || s.Dirty || len(s.Commit) != 40 {
		t.Fatalf("clean tree: %+v %v", s, err)
	}
	os.WriteFile(filepath.Join(dir, "new.txt"), []byte("x"), 0o644)
	if s, _ := GitSource(context.Background(), dir); !s.Dirty {
		t.Fatal("untracked file not detected")
	}
}

func TestUntarRejectsEscapes(t *testing.T) {
	if _, err := untar(strings.NewReader("not gzip"), t.TempDir()); err == nil {
		t.Fatal("garbage accepted")
	}
}

func TestUntarAcceptsGitHubArchives(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Typeflag: tar.TypeXGlobalHeader, Name: "pax_global_header", PAXRecords: map[string]string{"comment": "abc"}, Format: tar.FormatPAX})
	tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: "theme-starter-main/", Mode: 0o755})
	body := []byte(`{"name":"Starter"}`)
	tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: "theme-starter-main/theme.json", Mode: 0o644, Size: int64(len(body))})
	tw.Write(body)
	tw.Close()
	gz.Close()
	root, err := untar(&buf, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if name, err := Name(root); err != nil || name != "Starter" {
		t.Fatalf("%s %v", name, err)
	}
}

func zipOf(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(body))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestUnzipWritesANewDirectoryOnly(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "dawn")
	count, err := Unzip(zipOf(t, map[string]string{"theme.json": "{}", "sections/hero.vasc": "hero"}), dest)
	if err != nil || count != 2 {
		t.Fatalf("unzip = %d, %v", count, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dest, "sections", "hero.vasc")); string(b) != "hero" {
		t.Fatalf("hero = %q", b)
	}
	if _, err := Unzip(zipOf(t, map[string]string{"theme.json": "{}"}), dest); err == nil {
		t.Fatal("an existing directory must be refused")
	}
}

func TestUnzipRefusesPathsOutsideTheDirectory(t *testing.T) {
	root := t.TempDir()
	dest := filepath.Join(root, "theme")
	if _, err := Unzip(zipOf(t, map[string]string{"../escape.txt": "x"}), dest); err == nil {
		t.Fatal("a parent path was accepted")
	}
	if _, err := os.Stat(filepath.Join(root, "escape.txt")); err == nil {
		t.Fatal("a file was written outside the directory")
	}
}
