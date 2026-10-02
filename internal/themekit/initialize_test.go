package themekit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type initFixture struct {
	root, source, destination string
	tools                     Tools
}

func newInit(t *testing.T) *initFixture {
	f := &initFixture{root: t.TempDir()}
	f.source = filepath.Join(f.root, "starter")
	f.destination = filepath.Join(f.root, "new-theme")
	makeTheme(t, f.source)
	f.tools = fakeTools(t, "accept")
	return f
}

func (f *initFixture) run(name string) error {
	_, err := Initialize(bg, f.source, f.destination, name, f.tools)
	return err
}

func listDir(t *testing.T, dir string) []string {
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestInitCreatesEditableSourceAndKeepsTheOriginal(t *testing.T) {
	f := newInit(t)
	for _, name := range []string{".env", "setup.py", "README.md", "LICENSE", "NOTICE.txt"} {
		write(t, filepath.Join(f.source, name), "Template text: "+name)
	}
	os.Chmod(filepath.Join(f.source, "src/sections/hero/body.html"), 0o755)
	for _, name := range []string{".git", "node_modules", "dist"} {
		write(t, filepath.Join(f.source, name, "unwanted"), "Do not copy")
	}
	write(t, filepath.Join(f.source, "sections/hero.vasc"), "Stale output")
	before := snapshot(t, f.source)
	if err := f.run("Studio Élan"); err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Name     string
		Status   string
		Surfaces []struct {
			RequiredSections []string `json:"required_sections"`
		}
	}
	json.Unmarshal([]byte(read(t, filepath.Join(f.destination, "theme.json"))), &manifest)
	if manifest.Name != "Studio Élan" || manifest.Status != "draft" || manifest.Surfaces[0].RequiredSections[0] != "hero" {
		t.Fatalf("manifest = %+v", manifest)
	}
	if !strings.Contains(read(t, filepath.Join(f.destination, "sections/hero.vasc")), "<section>Hero</section>") {
		t.Fatal("sections were not rebuilt")
	}
	if !exists(filepath.Join(f.destination, "src/.generated-sections.json")) {
		t.Fatal("no ownership file")
	}
	for _, name := range []string{"README.md", "LICENSE", "NOTICE.txt"} {
		if read(t, filepath.Join(f.destination, name)) != read(t, filepath.Join(f.source, name)) {
			t.Fatalf("%s not copied", name)
		}
	}
	for _, name := range []string{".git", "node_modules", "dist", ".env", "setup.py", "surfaces.json"} {
		if exists(filepath.Join(f.destination, name)) {
			t.Fatalf("%s was copied", name)
		}
	}
	if read(t, filepath.Join(f.destination, ".gitignore")) != Gitignore {
		t.Fatal("missing .gitignore")
	}
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(filepath.Join(f.destination, "src/sections/hero/body.html"))
		if info.Mode().Perm() != 0o644 {
			t.Fatalf("mode = %v", info.Mode())
		}
	}
	if !sameSnapshot(before, snapshot(t, f.source)) {
		t.Fatal("source was modified")
	}
}

func TestInitNeverOverwritesAnExistingDirectoryOrFile(t *testing.T) {
	f := newInit(t)
	write(t, filepath.Join(f.destination, "keep.txt"), "Existing work")
	expectError(t, f.run("X"), "already exists")
	if read(t, filepath.Join(f.destination, "keep.txt")) != "Existing work" {
		t.Fatal("existing file changed")
	}
	os.Remove(filepath.Join(f.destination, "keep.txt"))
	if f.run("X") == nil {
		t.Fatal("empty directory was reused")
	}
	os.Remove(f.destination)
	write(t, f.destination, "Existing file")
	if f.run("X") == nil || read(t, f.destination) != "Existing file" {
		t.Fatal("existing file was replaced")
	}
}

func TestInitCheckerFailureLeavesNothingBehind(t *testing.T) {
	f := newInit(t)
	before := listDir(t, f.root)
	setFake(t, "reject")
	if f.run("X") == nil {
		t.Fatal("rejected theme was created")
	}
	if exists(f.destination) || strings.Join(before, ",") != strings.Join(listDir(t, f.root), ",") {
		t.Fatalf("left behind: %v", listDir(t, f.root))
	}
}

func TestInitPreservesADestinationCreatedDuringValidation(t *testing.T) {
	f := newInit(t)
	t.Setenv("THEMEKIT_FAKE_MKDIR", f.destination)
	if f.run("X") == nil {
		t.Fatal("init replaced a concurrently created directory")
	}
	if got := listDir(t, f.destination); len(got) != 1 || got[0] != "keep.txt" {
		t.Fatalf("destination now holds %v", got)
	}
}

func TestInitRejectsSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	f := newInit(t)
	link := filepath.Join(f.root, "linked-source")
	os.Symlink(f.source, link)
	if _, err := Initialize(bg, link, f.destination, "X", f.tools); err == nil {
		t.Fatal("symlinked source accepted")
	}
	os.Symlink(filepath.Join(f.root, "does-not-exist"), f.destination)
	if f.run("X") == nil || !isSymlink(f.destination) {
		t.Fatal("symlinked destination replaced")
	}
	os.Remove(f.destination)
	os.MkdirAll(filepath.Join(f.source, "assets"), 0o755)
	os.Symlink(filepath.Join(f.root, "does-not-exist"), filepath.Join(f.source, "assets/outside.png"))
	expectError(t, f.run("X"), "symlink")
	if exists(f.destination) {
		t.Fatal("destination created")
	}
}

func TestInitRejectsNestedDestinationAndUnrecognisedSource(t *testing.T) {
	f := newInit(t)
	if _, err := Initialize(bg, f.source, filepath.Join(f.source, "nested"), "X", f.tools); err == nil {
		t.Fatal("nested destination accepted")
	}
	write(t, filepath.Join(f.source, "src/.env"), "must not be copied")
	expectError(t, f.run("X"), "unsupported editable source file")
	if exists(f.destination) {
		t.Fatal("destination created")
	}
}

func TestInitValidatesTheNameBeforeWriting(t *testing.T) {
	f := newInit(t)
	for _, name := range []string{"", "   ", strings.Repeat("A", 101), "Injected\nname", "A\x7fB"} {
		expectError(t, f.run(name), "theme name must be 1–100 characters")
		if exists(f.destination) {
			t.Fatalf("destination created for %q", name)
		}
	}
	if err := f.run(strings.Repeat("é", 100)); err != nil {
		t.Fatalf("100-character name rejected: %v", err)
	}
}

func TestInitAcceptsADirectVasculaTheme(t *testing.T) {
	f := newInit(t)
	os.RemoveAll(filepath.Join(f.source, "src"))
	original := `<h1>{{ shop.name }}</h1>{% schema %}{"name":"hero","kind":"section","ctx_needs":["shop"]}{% endschema %}`
	write(t, filepath.Join(f.source, "sections/hero.vasc"), original)
	if err := f.run("X"); err != nil {
		t.Fatal(err)
	}
	if read(t, filepath.Join(f.destination, "sections/hero.vasc")) != original {
		t.Fatal("compiled section changed")
	}
	if exists(filepath.Join(f.destination, "src")) {
		t.Fatal("src created")
	}
}
