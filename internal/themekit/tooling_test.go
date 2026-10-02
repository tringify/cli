package themekit

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/tringify/cli/internal/pyjson"
)

var bg = context.Background()

func TestBuildCheckAndPackage(t *testing.T) {
	root := t.TempDir()
	makeTheme(t, root)
	tools := fakeTools(t, "accept")

	names, err := Build(root)
	if err != nil || strings.Join(names, ",") != "hero" {
		t.Fatalf("Build = %v, %v", names, err)
	}
	sections, err := Validate(bg, root, "sealed", tools)
	if err != nil || strings.Join(sections, ",") != "hero" {
		t.Fatalf("Validate = %v, %v", sections, err)
	}
	output := filepath.Join(root, "dist", "theme.zip")
	count, err := Package(bg, root, output, "sealed", tools)
	if err != nil {
		t.Fatal(err)
	}
	got := zipNames(t, output)
	if count != len(got) || !contains(got, "theme.json") || !contains(got, "sections/hero.vasc") {
		t.Fatalf("package has %d/%v", count, got)
	}
	for _, name := range []string{"surfaces.json", "src/_shared.css", "src/.generated-sections.json"} {
		if contains(got, name) {
			t.Fatalf("package includes %s", name)
		}
	}
}

func TestBuildOutputMatchesThePythonFormat(t *testing.T) {
	root := t.TempDir()
	makeTheme(t, root)
	write(t, filepath.Join(root, "src/sections/hero/schema.json"), `{"name": "hero", "kind": "section", "settings": [{"id": "title", "type": "text", "default": "Café ☕", "max": 1.0, "big": 12345678901234567890}]}`)
	if _, err := Build(root); err != nil {
		t.Fatal(err)
	}
	want := "<style>\nhtml { color: black; }\n\nsection { display: block; }\n</style>\n<section>Hero</section>\n{% schema %}\n" +
		"{\n  \"name\": \"hero\",\n  \"kind\": \"section\",\n  \"settings\": [\n    {\n      \"id\": \"title\",\n      \"type\": \"text\",\n" +
		"      \"default\": \"Caf\\u00e9 \\u2615\",\n      \"max\": 1.0,\n      \"big\": 12345678901234567890\n    }\n  ]\n}\n{% endschema %}\n"
	if got := read(t, filepath.Join(root, "sections/hero.vasc")); got != want {
		t.Fatalf("hero.vasc:\n%s\nwant:\n%s", got, want)
	}
	owned := read(t, filepath.Join(root, "src/.generated-sections.json"))
	if owned != "{\n  \"version\": 1,\n  \"sections\": {\n    \"hero\": \""+sha256Hex([]byte(want))+"\"\n  }\n}\n" {
		t.Fatalf("ownership file:\n%s", owned)
	}
	manifest := read(t, filepath.Join(root, "theme.json"))
	if !strings.HasPrefix(manifest, "{\n  \"schema_version\": 3,\n  \"name\": \"Test\",") || !strings.HasSuffix(manifest, "}\n") {
		t.Fatalf("theme.json:\n%s", manifest)
	}
}

func TestBuildNormalisesNewlinesAndStripsLikePython(t *testing.T) {
	root := t.TempDir()
	makeTheme(t, root)
	write(t, filepath.Join(root, "src/_shared.css"), "a {}\r\nb {}\r\n")
	write(t, filepath.Join(root, "src/sections/hero/body.html"), "\x1c\r\n<section>Hero</section>\u00a0\r\n")
	if _, err := Build(root); err != nil {
		t.Fatal(err)
	}
	got := read(t, filepath.Join(root, "sections/hero.vasc"))
	if !strings.HasPrefix(got, "<style>\na {}\nb {}\n\nsection") || !strings.Contains(got, "</style>\n<section>Hero</section>\n{% schema %}") {
		t.Fatalf("hero.vasc:\n%q", got)
	}
}

func TestFailedGateDoesNotReplaceAnExistingPackage(t *testing.T) {
	root := t.TempDir()
	makeTheme(t, root)
	Build(root)
	output := filepath.Join(root, "dist", "theme.zip")
	tools := fakeTools(t, "accept")
	if _, err := Package(bg, root, output, "sealed", tools); err != nil {
		t.Fatal(err)
	}
	accepted := read(t, output)
	setFake(t, "reject")
	if _, err := Package(bg, root, output, "sealed", tools); err == nil {
		t.Fatal("rejected package was accepted")
	}
	if read(t, output) != accepted {
		t.Fatal("rejected package replaced the accepted one")
	}
}

func TestPackageIsDeterministic(t *testing.T) {
	root := t.TempDir()
	makeTheme(t, root)
	tools := fakeTools(t, "accept")
	first, second := filepath.Join(root, "dist", "a.zip"), filepath.Join(root, "dist", "b.zip")
	Package(bg, root, first, "sealed", tools)
	if _, err := Package(bg, root, second, "sealed", tools); err != nil {
		t.Fatal(err)
	}
	if read(t, first) != read(t, second) {
		t.Fatal("two packages of the same source differ")
	}
}

func TestPackageBuildsLatestSourceWithoutMutatingAuthorFiles(t *testing.T) {
	root := t.TempDir()
	makeTheme(t, root)
	Build(root)
	write(t, filepath.Join(root, "src/sections/hero/body.html"), "<section>Fresh content</section>")
	before := snapshot(t, root)
	output := filepath.Join(root, "dist", "theme.zip")
	if _, err := Package(bg, root, output, "sealed", fakeTools(t, "accept")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(zipRead(t, output, "sections/hero.vasc"), "Fresh content") {
		t.Fatal("package did not build the latest source")
	}
	if contains(zipNames(t, output), "src/.generated-sections.json") {
		t.Fatal("package includes the ownership file")
	}
	if !sameSnapshot(before, snapshot(t, root)) {
		t.Fatal("package changed the author's files")
	}
}

func TestBuildRefreshesManifestAndOnlyPrunesOwnedSections(t *testing.T) {
	root := t.TempDir()
	makeTheme(t, root)
	value, _ := pyjson.Loads(read(t, filepath.Join(root, "theme.json")))
	manifest := value.(*pyjson.Object)
	sections, _ := manifest.Get("sections")
	manual := pyjson.NewObject()
	manual.Set("name", pyjson.String("manual"))
	manual.Set("file", pyjson.String("sections/manual.vasc"))
	manual.Set("kind", pyjson.String("section"))
	manifest.Set("sections", append(sections.([]pyjson.Value), manual))
	write(t, filepath.Join(root, "theme.json"), pyjson.Compact(manifest))
	write(t, filepath.Join(root, "sections/manual.vasc"), "Authored directly")
	if _, err := Build(root); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(root, "src/sections/hero"), filepath.Join(root, "src/sections/banner")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "src/sections/banner/schema.json"), `{"name": "banner", "kind": "section"}`)
	if _, err := Build(root); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(root, "sections/hero.vasc")) {
		t.Fatal("stale generated section was kept")
	}
	if read(t, filepath.Join(root, "sections/manual.vasc")) != "Authored directly" {
		t.Fatal("hand-written section was changed")
	}
	var parsed struct{ Sections []struct{ Name string } }
	json.Unmarshal([]byte(read(t, filepath.Join(root, "theme.json"))), &parsed)
	if len(parsed.Sections) != 2 || parsed.Sections[0].Name != "manual" || parsed.Sections[1].Name != "banner" {
		t.Fatalf("sections = %+v", parsed.Sections)
	}
}

func TestModifiedStaleOutputIsNotDeleted(t *testing.T) {
	root := t.TempDir()
	makeTheme(t, root)
	Build(root)
	write(t, filepath.Join(root, "sections/hero.vasc"), "Preserve my edit")
	os.RemoveAll(filepath.Join(root, "src/sections/hero"))
	_, err := Build(root)
	expectError(t, err, "previously generated section was edited")
	if read(t, filepath.Join(root, "sections/hero.vasc")) != "Preserve my edit" {
		t.Fatal("edited output was deleted")
	}
}

func TestRuntimeOnlyThemesCanStillBePackaged(t *testing.T) {
	root := t.TempDir()
	makeTheme(t, root)
	Build(root)
	os.RemoveAll(filepath.Join(root, "src"))
	if _, err := Package(bg, root, filepath.Join(root, "dist", "theme.zip"), "sealed", fakeTools(t, "accept")); err != nil {
		t.Fatal(err)
	}
}

func TestSymlinksCannotReadOrOverwriteFilesOutsideTheTheme(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	base := t.TempDir()
	root := filepath.Join(base, "theme")
	makeTheme(t, root)
	outside := filepath.Join(base, "private.txt")
	write(t, outside, "outside")
	if err := os.Symlink(outside, filepath.Join(root, "sections/hero.vasc")); err != nil {
		t.Fatal(err)
	}
	_, err := Build(root)
	expectError(t, err, "symlinks")
	_, err = Package(bg, root, filepath.Join(root, "dist", "theme.zip"), "sealed", fakeTools(t, "accept"))
	expectError(t, err, "symlinks")
	if read(t, outside) != "outside" {
		t.Fatal("file outside the theme was changed")
	}
}

func TestOutputCannotBeTheThemeDirectoryOrOutsideDist(t *testing.T) {
	root := t.TempDir()
	makeTheme(t, root)
	tools := fakeTools(t, "accept")
	_, err := Package(bg, root, root, "sealed", tools)
	expectError(t, err, "outputs inside")
	_, err = Package(bg, root, filepath.Join(root, "src", "theme.zip"), "sealed", tools)
	expectError(t, err, "must be under dist/")
}

func TestInvalidSchemaNamesTheFileAndKeepsCompiledOutput(t *testing.T) {
	root := t.TempDir()
	makeTheme(t, root)
	Build(root)
	previous := read(t, filepath.Join(root, "sections/hero.vasc"))
	write(t, filepath.Join(root, "src/sections/hero/schema.json"), "{invalid")
	_, err := Build(root)
	expectError(t, err, filepath.FromSlash("src/sections/hero/schema.json")+":1:2: Expecting property name enclosed in double quotes")
	if read(t, filepath.Join(root, "sections/hero.vasc")) != previous {
		t.Fatal("failed build changed the compiled section")
	}
}

func TestBuildRejectsBrokenManifests(t *testing.T) {
	cases := map[string]string{
		`{"sections": 3}`: "theme.json must contain a sections list",
		`[]`:              "theme.json must contain a sections list",
		`{"sections": [{"name": "a"}, {"name": "a"}]}`: "duplicate section names",
		`{"sections": [{"name": 1}]}`:                  "sections must be named objects",
	}
	for manifest, want := range cases {
		root := t.TempDir()
		makeTheme(t, root)
		write(t, filepath.Join(root, "theme.json"), manifest)
		_, err := Build(root)
		expectError(t, err, want)
	}
}

func TestBuildFailureOnAnEmptyDirectoryIsConcise(t *testing.T) {
	_, err := Build(t.TempDir())
	expectError(t, err, filepath.FromSlash("src/_shared.css"))
}

func TestMissingRuntimeFilesAreNamed(t *testing.T) {
	root := t.TempDir()
	makeTheme(t, root)
	os.Remove(filepath.Join(root, "tokens.json"))
	_, err := Validate(bg, root, "sealed", fakeTools(t, "accept"))
	expectError(t, err, "missing runtime bundle file: tokens.json")
}

func TestMissingCheckerFailsClosed(t *testing.T) {
	root := t.TempDir()
	makeTheme(t, root)
	Build(root)
	missing := errors.New("themecheck is required")
	tools := Tools{Checker: func(context.Context) (string, error) { return "", missing }}
	if _, err := Validate(bg, root, "sealed", tools); !errors.Is(err, missing) {
		t.Fatalf("err = %v", err)
	}
}
