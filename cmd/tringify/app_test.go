package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tringify/cli/internal/devapi"
)

func TestAppTargetPrefersTheFlagThenTheFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "tringify.app.json")
	if _, err := appTarget("", file); err == nil || !strings.Contains(err.Error(), "--app") {
		t.Fatalf("missing file: %v", err)
	}
	os.WriteFile(file, []byte(`{"schema_version":1}`), 0o644)
	if _, err := appTarget("", file); err == nil || !strings.Contains(err.Error(), "no app_id") {
		t.Fatalf("file without app_id: %v", err)
	}
	os.WriteFile(file, []byte(`{"schema_version":1,"app_id":"from-file"}`), 0o644)
	if id, _ := appTarget("", file); id != "from-file" {
		t.Fatalf("file app = %q", id)
	}
	if id, _ := appTarget("from-flag", file); id != "from-flag" {
		t.Fatalf("flag app = %q", id)
	}
}

func TestConfigErrorsCarryTheAPIExplanation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/apps/A/config":
			w.Write([]byte(`{"success":true,"data":{"schema_version":1,"app_id":"A"}}`))
		case r.Method == http.MethodPut:
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"code":"INVALID_INPUT","message":"Invalid input","details":"invalid embed_type"}`))
		case r.URL.Path == "/api/v1/apps/B/config":
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"code":"FORBIDDEN","message":"Forbidden"}`))
		}
	}))
	defer srv.Close()
	api := devapi.New(srv.URL, "org", fixedTokens{}, "test")
	a := newApp(&strings.Builder{}, &strings.Builder{})
	got, err := a.fetchConfig(context.Background(), api, "A")
	if err != nil || string(got) != "{\n  \"schema_version\": 1,\n  \"app_id\": \"A\"\n}\n" {
		t.Fatalf("fetch = %q %v", got, err)
	}
	err = api.Do(context.Background(), http.MethodPut, "/apps/A/config", map[string]any{}, nil)
	if err == nil || err.Error() != "Invalid input invalid embed_type (INVALID_INPUT)" {
		t.Fatalf("detail not shown: %v", err)
	}
	if _, err = a.fetchConfig(context.Background(), api, "B"); err == nil || !strings.Contains(err.Error(), "tringify login") {
		t.Fatalf("forbidden without the sign-in hint: %v", err)
	}
}

func TestWriteStarterSettingsPointsTheStarterAtTheApp(t *testing.T) {
	dest := t.TempDir()
	os.WriteFile(filepath.Join(dest, "wrangler.jsonc"), []byte(`{
  "name": "my-tringify-app",
  "d1_databases": [{ "database_name": "my-tringify-app" }],
  "vars": { "TRINGIFY_APP_ID": "" }
}`), 0o644)
	if err := writeStarterSettings(dest, "app-1", "rocket", []byte("{}\n")); err != nil {
		t.Fatal(err)
	}
	wrangler, _ := os.ReadFile(filepath.Join(dest, "wrangler.jsonc"))
	for _, want := range []string{`"name": "rocket"`, `"database_name": "rocket"`, `"TRINGIFY_APP_ID": "app-1"`} {
		if !strings.Contains(string(wrangler), want) {
			t.Fatalf("wrangler.jsonc lacks %s:\n%s", want, wrangler)
		}
	}
	vars, _ := os.ReadFile(filepath.Join(dest, ".dev.vars"))
	if !strings.Contains(string(vars), "TRINGIFY_WEBHOOK_SECRET=\n") || !strings.Contains(string(vars), "TOKEN_ENCRYPTION_KEY=") {
		t.Fatalf(".dev.vars = %s", vars)
	}
	if info, _ := os.Stat(filepath.Join(dest, ".dev.vars")); info.Mode().Perm() != 0o600 {
		t.Fatalf(".dev.vars mode = %v", info.Mode().Perm())
	}
	os.WriteFile(filepath.Join(dest, "wrangler.jsonc"), []byte(`{}`), 0o644)
	if err := writeStarterSettings(dest, "app-1", "rocket", []byte("{}\n")); err == nil {
		t.Fatal("an unexpected wrangler.jsonc was accepted")
	}
}
