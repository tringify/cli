package themekit

import (
	"archive/zip"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tringify/cli/internal/pyjson"
	"github.com/tringify/cli/internal/theme"
)

// The public starter theme at a fixed commit. Its committed sections/*.vasc,
// theme.json and src/.generated-sections.json are what the Python theme
// tools build from its sources, and starterPackageDigest is the digest of
// the package the Python tools write for it (entry names and contents, in
// order). Set TRINGIFY_THEME_STARTER to a checkout of that commit to run
// offline; otherwise the test downloads it, and skips if it cannot.
const (
	starterCommit        = "dc439656a7c7c999dba5be749b0828bdfa727fc2"
	starterSections      = 38
	starterPackageFiles  = 95
	starterPackageDigest = "ea193835eb85300f5de2674e5286858f5656107d9728e09ba14bc8abccf08daf"
)

func starter(t *testing.T) string {
	t.Helper()
	if dir := os.Getenv("TRINGIFY_THEME_STARTER"); dir != "" {
		copyDir := filepath.Join(t.TempDir(), "starter")
		if err := copyTree(dir, copyDir); err != nil {
			t.Fatal(err)
		}
		os.RemoveAll(filepath.Join(copyDir, ".git"))
		return copyDir
	}
	if testing.Short() {
		t.Skip("downloads the starter theme")
	}
	theme.StarterArchive = "https://codeload.github.com/tringify/theme-starter/tar.gz/" + starterCommit
	root, cleanup, err := theme.DownloadStarter(bg, &http.Client{Timeout: time.Minute})
	if err != nil {
		t.Skipf("starter theme unavailable: %v", err)
	}
	t.Cleanup(cleanup)
	return root
}

func TestStarterBuildMatchesThePythonTools(t *testing.T) {
	root := starter(t)
	before := snapshot(t, root)
	// Damage every generated file; the build must restore each byte.
	matches, _ := filepath.Glob(filepath.Join(root, "sections", "*.vasc"))
	for _, m := range matches {
		write(t, m, "stale")
	}
	manifest, err := pyjson.Loads(read(t, filepath.Join(root, "theme.json")))
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "theme.json"), pyjson.Compact(manifest))
	names, err := Build(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != starterSections {
		t.Fatalf("built %d sections", len(names))
	}
	after := snapshot(t, root)
	for name, sum := range before {
		if after[name] != sum {
			t.Errorf("%s differs from the Python build", name)
		}
	}
	if len(after) != len(before) {
		t.Errorf("build changed the file set: %d -> %d", len(before), len(after))
	}
}

func TestStarterPackageMatchesThePythonTools(t *testing.T) {
	root := starter(t)
	output := filepath.Join(t.TempDir(), "starter.zip")
	count, err := Package(bg, root, output, "sealed", fakeTools(t, "accept"))
	if err != nil {
		t.Fatal(err)
	}
	z, err := zip.OpenReader(output)
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()
	digest := sha256.New()
	for _, f := range z.File {
		if f.Modified.Year() != 1980 || f.ExternalAttrs != 0o100644<<16 || f.Method != zip.Deflate || strings.Contains(f.Name, "\\") {
			t.Fatalf("%s: unexpected metadata", f.Name)
		}
		rc, _ := f.Open()
		h := sha256.New()
		io.Copy(h, rc)
		rc.Close()
		fmt.Fprintf(digest, "%s\x00%x\n", f.Name, h.Sum(nil))
	}
	if count != starterPackageFiles || len(z.File) != starterPackageFiles {
		t.Fatalf("packaged %d files", count)
	}
	if got := fmt.Sprintf("%x", digest.Sum(nil)); got != starterPackageDigest {
		t.Fatalf("package digest %s, want %s", got, starterPackageDigest)
	}
}
