package themekit

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func newPreview(t *testing.T) (*Preview, string, *strings.Builder) {
	t.Helper()
	root := t.TempDir()
	makeTheme(t, root)
	var out strings.Builder
	p, err := NewPreview(root, t.TempDir(), "/s/fixture", os.Args[0], "", fakeTools(t, "accept"), &out)
	if err != nil {
		t.Fatal(err)
	}
	return p, root, &out
}

// touch rewrites a file with a different size, so the change is seen even
// on filesystems with coarse timestamps.
func touch(t *testing.T, path, content string) {
	t.Helper()
	write(t, path, content)
	later := time.Now().Add(time.Duration(len(content)) * time.Second)
	os.Chtimes(path, later, later)
}

func TestPreviewKeepsTheLastSnapshotAndRetriesChangedSource(t *testing.T) {
	p, root, out := newPreview(t)
	if !p.Rebuild(bg) {
		t.Fatalf("first build failed: %s", p.Error())
	}
	first := p.Snapshot()
	if !strings.Contains(string(first["/s/fixture/"]), "<h1>Example</h1>") || !strings.Contains(string(first["/s/fixture/"]), "tringify-preview-page") {
		t.Fatalf("snapshot = %v", first)
	}
	if p.Rebuild(bg) {
		t.Fatal("rebuilt unchanged source")
	}
	touch(t, filepath.Join(root, "src/sections/hero/schema.json"), "{broken")
	if p.Rebuild(bg) || p.Revision() != 1 || !strings.Contains(p.Error(), "schema.json:1:2") {
		t.Fatalf("revision %d error %q", p.Revision(), p.Error())
	}
	if len(p.Snapshot()) != len(first) {
		t.Fatal("failed build replaced the snapshot")
	}
	p.Rebuild(bg)
	if strings.Count(out.String(), "Preview needs attention") != 1 {
		t.Fatalf("error reported more than once:\n%s", out.String())
	}
	touch(t, filepath.Join(root, "src/sections/hero/schema.json"), `{"name": "hero", "kind": "section"}`)
	if !p.Rebuild(bg) || p.Revision() != 2 || p.Error() != "" {
		t.Fatalf("revision %d error %q", p.Revision(), p.Error())
	}
	if !strings.Contains(out.String(), "Preview updated: Example · 1 pages") {
		t.Fatalf("output:\n%s", out.String())
	}
	if p.State() != `{"revision": 2, "error": "", "name": "Example", "pages": [{"file": "home.html", "path": "/s/fixture/", "title": "Home"}]}` {
		t.Fatalf("state = %s", p.State())
	}
}

func TestSourceWatchIgnoresDistAndCredentialsButRejectsSymlinks(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "theme.json"), `{"name":"Example"}`)
	first, err := SourceSignature(root)
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, ".env"), "must not be read or served")
	write(t, filepath.Join(root, "dist/theme.zip"), "new output")
	if second, _ := SourceSignature(root); second != first {
		t.Fatal("dist or .env changed the signature")
	}
	write(t, filepath.Join(root, "templates/home.json"), "{}")
	if third, _ := SourceSignature(root); third == first {
		t.Fatal("template change not seen")
	}
	if runtime.GOOS != "windows" {
		os.Symlink(t.TempDir(), filepath.Join(root, "assets"))
		if _, err := SourceSignature(root); err == nil || !strings.Contains(err.Error(), "symlinks") {
			t.Fatalf("err = %v", err)
		}
	}
}

func TestPreviewServesOnlyTheSnapshotAndBlocksRebindingAndMutations(t *testing.T) {
	p, _, _ := newPreview(t)
	p.Rebuild(bg)
	server := httptest.NewServer(nil)
	defer server.Close()
	host := strings.TrimPrefix(server.URL, "http://")
	server.Config.Handler = p.Handler(func(h string) bool { return h == host })
	request := func(method, path, hostHeader string) (int, string, http.Header) {
		req, _ := http.NewRequest(method, server.URL+path, nil)
		if hostHeader != "" {
			req.Host = hostHeader
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body), resp.Header
	}
	status, body, headers := request("GET", "/s/fixture/", "")
	csp := headers.Get("Content-Security-Policy")
	if status != 200 || !strings.Contains(body, "<h1>Example</h1>") {
		t.Fatalf("%d %s", status, body)
	}
	for _, want := range []string{"form-action 'none'", "connect-src 'none'", "sandbox allow-scripts"} {
		if !strings.Contains(csp, want) {
			t.Fatalf("CSP %q lacks %q", csp, want)
		}
	}
	if strings.Contains(csp, "allow-same-origin") || headers.Get("X-Content-Type-Options") != "nosniff" || headers.Get("Cache-Control") != "no-store" {
		t.Fatalf("headers %v", headers)
	}
	if status, _, h := request("GET", "/s/fixture/__asset/theme.css", ""); status != 200 || h.Get("Content-Type") != "text/css" {
		t.Fatalf("asset %d %s", status, h.Get("Content-Type"))
	}
	if status, body, _ := request("GET", "/s/fixture/studio", ""); status != 200 || !strings.Contains(body, "const base='/s/fixture'") {
		t.Fatalf("studio %d", status)
	}
	if status, body, _ := request("GET", "/s/fixture/state", ""); status != 200 || !strings.HasPrefix(body, `{"revision": 1, "error": ""`) {
		t.Fatalf("state %d %s", status, body)
	}
	for _, route := range []string{"/", "/theme.json", "/s/wrong/", "/s/fixture/.env", "/s/fixture/%2e%2e/theme.json"} {
		if status, _, _ := request("GET", route, ""); status != 404 {
			t.Fatalf("%s: %d", route, status)
		}
	}
	if status, _, _ := request("GET", "/s/fixture/", "attacker.example"); status != 403 {
		t.Fatalf("foreign host: %d", status)
	}
	if status, _, _ := request("POST", "/s/fixture/", ""); status != 501 {
		t.Fatalf("POST: %d", status)
	}
}

func TestPreviewRejectsARendererPageOutsideItsOutput(t *testing.T) {
	p, root, _ := newPreview(t)
	t.Setenv("THEMEKIT_FAKE_PAGE_FILE", filepath.Join(root, "theme.json"))
	if p.Rebuild(bg) {
		t.Fatal("accepted a page outside the output")
	}
	if len(p.Snapshot()) != 0 || !strings.Contains(p.Error(), "invalid page file") {
		t.Fatalf("error %q", p.Error())
	}
}

func TestPreviewAddressRules(t *testing.T) {
	for host, want := range map[string]string{
		"0.0.0.0":   "use the specific LAN IP",
		"::1":       "requires an IPv4 address",
		"localhost": "'localhost' does not appear to be an IPv4 or IPv6 address",
	} {
		expectError(t, CheckHost(host, 9292), want)
	}
	expectError(t, CheckHost("127.0.0.1", 70000), "port must be between 0 and 65535")
	if err := CheckHost("192.168.1.20", 0); err != nil {
		t.Fatal(err)
	}
}
