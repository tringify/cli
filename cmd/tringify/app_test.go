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
