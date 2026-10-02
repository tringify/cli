package themekit

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The test binary doubles as a fake themecheck and theme-preview-render:
// when THEMEKIT_FAKE is set, TestMain behaves as that tool and exits.
func TestMain(m *testing.M) {
	if mode := os.Getenv("THEMEKIT_FAKE"); mode != "" {
		os.Exit(fake(mode, os.Args[1:]))
	}
	os.Exit(m.Run())
}

func fake(mode string, args []string) int {
	if contains(args, "--bundle") {
		mode = "render"
	}
	switch mode {
	case "accept", "reject":
		if len(args) != 4 || args[0] != "-json" || args[1] != "-mode" || !ValidMode(args[2]) {
			fmt.Fprintln(os.Stderr, "unexpected arguments", args)
			return 3
		}
		z, err := zip.OpenReader(args[3])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 3
		}
		names := map[string]bool{}
		for _, f := range z.File {
			names[f.Name] = true
		}
		z.Close()
		if !names["theme.json"] || names["surfaces.json"] {
			fmt.Fprintln(os.Stderr, "bad archive")
			return 3
		}
		if dir := os.Getenv("THEMEKIT_FAKE_MKDIR"); dir != "" {
			os.Mkdir(dir, 0o755)
			os.WriteFile(filepath.Join(dir, "keep.txt"), []byte("Concurrent work"), 0o644)
		}
		fmt.Println(`{"valid":true}`)
		if mode == "reject" {
			return 1
		}
		return 0
	case "diagnostics":
		fmt.Println(`{"error":"Invalid template.","details":{"file":"templates/home.json","reason":"Section is not allowed on this surface"}}`)
		return 1
	case "contract":
		fmt.Println(`{"schema_version":3,"ctx_needs":["cart","shop"],"setting_types":["text"],"hosted_actions":[{"kind":"cart_add","path":"/cart/add"}],"ctx":{"schema_version":1,"roots":[{"name":"cart"},{"name":"shop"}],"definitions":{}}}`)
		return 0
	case "contract-invalid":
		fmt.Println(`{"schema_version":3,"ctx_needs":"cart"}`)
		return 0
	case "render":
		// theme-preview-render --bundle B --output O --base BASE --preset P
		output, base := argAfter(args, "--output"), argAfter(args, "--base")
		if contains(args, "--context") {
			os.WriteFile(os.Getenv("THEMEKIT_FAKE_ARGS"), []byte(strings.Join(args, "\n")), 0o644)
			os.Stdout.WriteString(os.Getenv("THEMEKIT_FAKE_CONTEXT"))
			if code := os.Getenv("THEMEKIT_FAKE_CONTEXT_EXIT"); code != "" {
				fmt.Fprint(os.Stderr, `demo product "missing" not found`)
				return 1
			}
			return 0
		}
		os.MkdirAll(filepath.Join(output, "__asset"), 0o755)
		os.WriteFile(filepath.Join(output, "home.html"), []byte("<h1>Example</h1>"), 0o644)
		os.WriteFile(filepath.Join(output, "__asset", "theme.css"), []byte("body { color: black; }"), 0o644)
		file := "home.html"
		if f := os.Getenv("THEMEKIT_FAKE_PAGE_FILE"); f != "" {
			file = f
		}
		manifest, _ := json.Marshal(map[string]any{"name": "Example", "pages": []any{map[string]any{"title": "Home", "path": base + "/", "file": file}}})
		os.WriteFile(filepath.Join(output, "manifest.json"), manifest, 0o644)
		return 0
	}
	return 3
}

func argAfter(args []string, name string) string {
	for i, a := range args {
		if a == name {
			if i+1 < len(args) {
				return args[i+1]
			}
			return "set"
		}
	}
	return ""
}

// fakeTools returns tools that run this test binary in the given mode.
func fakeTools(t *testing.T, mode string) Tools {
	t.Helper()
	t.Setenv("THEMEKIT_FAKE", mode)
	self := func(context.Context) (string, error) { return os.Args[0], nil }
	return Tools{Checker: self, Renderer: self}
}

// setFake changes the fake's mode without a new Tools value.
func setFake(t *testing.T, mode string) { t.Helper(); t.Setenv("THEMEKIT_FAKE", mode) }

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// makeTheme writes the same minimal theme the Python tool's tests use.
func makeTheme(t *testing.T, root string) {
	t.Helper()
	for _, d := range []string{"sections", "src/sections/hero", "templates"} {
		os.MkdirAll(filepath.Join(root, d), 0o755)
	}
	write(t, filepath.Join(root, "src/_shared.css"), "html { color: black; }\n")
	write(t, filepath.Join(root, "src/sections/hero/body.html"), "<section>Hero</section>\n")
	write(t, filepath.Join(root, "src/sections/hero/style.css"), "section { display: block; }\n")
	write(t, filepath.Join(root, "src/sections/hero/schema.json"), `{"name": "hero", "kind": "section"}`)
	write(t, filepath.Join(root, "theme.json"), `{"schema_version": 3, "name": "Test", "source": "uploaded", "status": "draft", "default_locale": "en", "sections": [{"name": "hero", "kind": "section", "file": "sections/hero.vasc", "origin": "override"}], "surfaces": [{"surface_key": "home", "required_sections": ["hero"]}]}`)
	write(t, filepath.Join(root, "surfaces.json"), "{}\n")
	write(t, filepath.Join(root, "tokens.json"), "{}\n")
	write(t, filepath.Join(root, "templates/home.json"), `{"page_type": "home", "sections": [{"type": "hero", "position": 0}]}`)
}

func zipNames(t *testing.T, path string) []string {
	t.Helper()
	z, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()
	var names []string
	for _, f := range z.File {
		names = append(names, f.Name)
	}
	return names
}

func zipRead(t *testing.T, path, name string) string {
	t.Helper()
	z, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()
	for _, f := range z.File {
		if f.Name == name {
			rc, _ := f.Open()
			defer rc.Close()
			var b strings.Builder
			buf := make([]byte, 4096)
			for {
				n, err := rc.Read(buf)
				b.Write(buf[:n])
				if err != nil {
					break
				}
			}
			return b.String()
		}
	}
	t.Fatalf("%s not in %s", name, path)
	return ""
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// snapshot hashes every file outside dist/.
func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		r, _ := filepath.Rel(root, path)
		if d.IsDir() && r == "dist" {
			return filepath.SkipDir
		}
		if d.Type().IsRegular() {
			data, _ := os.ReadFile(path)
			out[filepath.ToSlash(r)] = sha256Hex(data)
		}
		return nil
	})
	return out
}

func sameSnapshot(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func expectError(t *testing.T, err error, substring string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), substring) {
		t.Fatalf("error = %v, want one containing %q", err, substring)
	}
}
