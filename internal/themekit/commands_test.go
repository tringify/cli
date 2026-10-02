package themekit

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/tringify/cli/internal/pyjson"
)

func TestFinderMetadataIsOmittedWithoutModifyingSource(t *testing.T) {
	f := newInit(t)
	os.MkdirAll(filepath.Join(f.source, "assets/images"), 0o755)
	Build(f.source)
	for _, folder := range []string{"", "src", "src/sections", "src/sections/hero", "assets", "assets/images", "templates"} {
		write(t, filepath.Join(f.source, folder, ".DS_Store"), "Finder metadata")
	}
	before := snapshot(t, f.source)
	if err := f.run("New theme"); err != nil {
		t.Fatal(err)
	}
	filepath.WalkDir(f.destination, func(path string, d os.DirEntry, err error) error {
		if d.Name() == ".DS_Store" {
			t.Fatalf("copied %s", path)
		}
		return nil
	})
	if _, err := Validate(bg, f.source, "sealed", f.tools); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(f.root, "theme.zip")
	Package(bg, f.source, output, "sealed", f.tools)
	first := read(t, output)
	Package(bg, f.source, output, "sealed", f.tools)
	if read(t, output) != first {
		t.Fatal("package is not deterministic")
	}
	for _, name := range zipNames(t, output) {
		if filepath.Base(name) == ".DS_Store" {
			t.Fatalf("package includes %s", name)
		}
	}
	if !sameSnapshot(before, snapshot(t, f.source)) {
		t.Fatal("source modified")
	}
}

func TestFinderMetadataSymlinksAreRejectedByEveryCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	f := newInit(t)
	Build(f.source)
	sentinel := filepath.Join(f.root, "sentinel")
	write(t, sentinel, "Do not follow or ignore this link")
	os.MkdirAll(filepath.Join(f.source, "assets"), 0o755)
	os.Symlink(sentinel, filepath.Join(f.source, "assets/.DS_Store"))
	expectError(t, f.run("New theme"), "symlink")
	_, err := Package(bg, f.source, filepath.Join(f.root, "theme.zip"), "sealed", f.tools)
	expectError(t, err, "symlink")
	_, err = Validate(bg, f.source, "sealed", f.tools)
	expectError(t, err, "symlink")
	if exists(f.destination) || exists(filepath.Join(f.root, "theme.zip")) || read(t, sentinel) != "Do not follow or ignore this link" {
		t.Fatal("a command followed the link")
	}
}

func TestADirectoryNamedLikeFinderMetadataIsKept(t *testing.T) {
	root := t.TempDir()
	makeTheme(t, root)
	write(t, filepath.Join(root, "assets/.DS_Store/image.svg"), "<svg></svg>")
	archive := filepath.Join(t.TempDir(), "archive.zip")
	if _, err := WriteArchive(root, archive); err != nil {
		t.Fatal(err)
	}
	if !contains(zipNames(t, archive), "assets/.DS_Store/image.svg") {
		t.Fatal("directory dropped")
	}
}

func TestOtherHiddenSourceFilesRemainInvalid(t *testing.T) {
	f := newInit(t)
	write(t, filepath.Join(f.source, "src/.not-a-section"), "Unexpected source")
	expectError(t, f.run("New theme"), "unsupported editable source file")
}

func TestArchiveEntriesAreSortedWithFixedMetadata(t *testing.T) {
	root := t.TempDir()
	makeTheme(t, root)
	write(t, filepath.Join(root, "assets/b.css"), "b")
	write(t, filepath.Join(root, "assets/a-é.css"), "a")
	archive := filepath.Join(t.TempDir(), "theme.zip")
	if _, err := WriteArchive(root, archive); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(zipNames(t, archive), " ")
	if got != "assets/a-é.css assets/b.css templates/home.json theme.json tokens.json" {
		t.Fatalf("entries = %s", got)
	}
}

func TestCheckerDiagnosticsNameTheFileAndReason(t *testing.T) {
	got := CheckerFailure(`{"valid": false, "error": "Theme section is invalid.", "code": "THEME_IMPORT_INVALID_SECTION", "details": {"file": "sections/hero.vasc", "reason": "Unknown setting type: colour", "empty": "", "skip": null, "line_number": 4, "allowed_types": ["text", "é"]}}`, "")
	want := "Theme section is invalid. (THEME_IMPORT_INVALID_SECTION)\n  File: sections/hero.vasc\n  Reason: Unknown setting type: colour\n  Line number: 4\n  Allowed types: [\n  \"text\",\n  \"é\"\n]"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	if got := CheckerFailure(`{"error": "Bad.", "details": ["a", 1, true, null]}`, ""); got != "Bad.\n  ['a', 1, True, None]" {
		t.Fatalf("list details: %q", got)
	}
	if got := CheckerFailure("", "Invalid theme archive\n"); got != "Invalid theme archive" {
		t.Fatalf("text error: %q", got)
	}
	if got := CheckerFailure("", ""); got != "themecheck failed" {
		t.Fatalf("empty error: %q", got)
	}
}

func TestCheckReportsDiagnosticsFromTheChecker(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "theme.json"), "{}")
	write(t, filepath.Join(root, "tokens.json"), "{}")
	_, err := Validate(bg, root, "sealed", fakeTools(t, "diagnostics"))
	expectError(t, err, "File: templates/home.json")
	expectError(t, err, "Reason: Section is not allowed")
	if strings.Contains(err.Error(), `"error":`) {
		t.Fatal("raw JSON shown")
	}
}

func TestContractIsValidatedAndKeptInOrder(t *testing.T) {
	contract, err := AuthorContract(bg, fakeTools(t, "contract"))
	if err != nil {
		t.Fatal(err)
	}
	got := pyjson.DumpsUnicode(contract)
	if !strings.HasPrefix(got, "{\n  \"schema_version\": 3,\n  \"ctx_needs\": [\n    \"cart\",\n    \"shop\"\n  ],") {
		t.Fatalf("contract:\n%s", got)
	}
	_, err = AuthorContract(bg, fakeTools(t, "contract-invalid"))
	expectError(t, err, "invalid author contract")
}

func TestContextPackagesTheThemeAndPassesThePage(t *testing.T) {
	root := t.TempDir()
	makeTheme(t, root)
	argsFile := filepath.Join(t.TempDir(), "args")
	t.Setenv("THEMEKIT_FAKE_ARGS", argsFile)
	t.Setenv("THEMEKIT_FAKE_CONTEXT", `{"schema_version": 1, "page": "product", "entity": "mug", "context": {"product": {"title": "Mug"}}}`)
	tools := fakeTools(t, "accept")
	payload, err := InspectContext(bg, root, "product", "mug", "Editorial", tools)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(pyjson.DumpsUnicode(payload), `"title": "Mug"`) {
		t.Fatalf("payload = %s", pyjson.DumpsUnicode(payload))
	}
	args := strings.Split(read(t, argsFile), "\n")
	for _, pair := range [][2]string{{"--page", "product"}, {"--entity", "mug"}, {"--preset", "Editorial"}, {"--base", "/preview"}} {
		if argAfter(args, pair[0]) != pair[1] {
			t.Fatalf("renderer args %v", args)
		}
	}
}

func TestContextRejectsAnInvalidPayloadAndSurfacesFailures(t *testing.T) {
	root := t.TempDir()
	makeTheme(t, root)
	t.Setenv("THEMEKIT_FAKE_ARGS", filepath.Join(t.TempDir(), "args"))
	tools := fakeTools(t, "accept")
	t.Setenv("THEMEKIT_FAKE_CONTEXT", `{"page":"home"}`)
	_, err := InspectContext(bg, root, "home", "", "", tools)
	expectError(t, err, "invalid context result")
	t.Setenv("THEMEKIT_FAKE_CONTEXT", "")
	t.Setenv("THEMEKIT_FAKE_CONTEXT_EXIT", "1")
	_, err = InspectContext(bg, root, "product", "missing", "", tools)
	expectError(t, err, `demo product "missing" not found`)
}
