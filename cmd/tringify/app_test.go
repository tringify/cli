package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
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
  "vars": { "TRINGIFY_APP_ID": "", "TRINGIFY_CLIENT_ID": "" }
}`), 0o644)
	if err := writeStarterSettings(dest, "app-1", "app_client", "rocket", []byte("{}\n")); err != nil {
		t.Fatal(err)
	}
	wrangler, _ := os.ReadFile(filepath.Join(dest, "wrangler.jsonc"))
	for _, want := range []string{`"name": "rocket"`, `"database_name": "rocket"`, `"TRINGIFY_APP_ID": "app-1"`, `"TRINGIFY_CLIENT_ID": "app_client"`} {
		if !strings.Contains(string(wrangler), want) {
			t.Fatalf("wrangler.jsonc lacks %s:\n%s", want, wrangler)
		}
	}
	vars, _ := os.ReadFile(filepath.Join(dest, ".dev.vars"))
	if !strings.Contains(string(vars), "TRINGIFY_WEBHOOK_SECRET=\n") || !strings.Contains(string(vars), "TRINGIFY_CLIENT_SECRET=\n") || !strings.Contains(string(vars), "TOKEN_ENCRYPTION_KEY=") {
		t.Fatalf(".dev.vars = %s", vars)
	}
	// Windows has no Unix permission bits.
	if info, _ := os.Stat(filepath.Join(dest, ".dev.vars")); runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf(".dev.vars mode = %v", info.Mode().Perm())
	}
	os.WriteFile(filepath.Join(dest, "wrangler.jsonc"), []byte(`{}`), 0o644)
	if err := writeStarterSettings(dest, "app-1", "app_client", "rocket", []byte("{}\n")); err == nil {
		t.Fatal("an unexpected wrangler.jsonc was accepted")
	}
}

func TestCreateAppSendsTheChoicesAndShowsTheSecretOnce(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/apps" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"success":true,"data":{"app":{"id":"A1","name":"Shop Channel","slug":"shop-channel"},"secrets":{"client_id":"cid","client_secret":"tcs_once"}}}`))
	}))
	defer srv.Close()
	api := devapi.New(srv.URL, "org", fixedTokens{}, "test")
	body := map[string]any{"name": "Shop Channel", "app_type": "sales_channel", "distribution": "private", "category_id": "sales_channels", "subcategory_id": "marketplaces"}

	var out strings.Builder
	if err := newApp(&out, &strings.Builder{}).createApp(context.Background(), api, body, false); err != nil {
		t.Fatal(err)
	}
	if got["app_type"] != "sales_channel" || got["subcategory_id"] != "marketplaces" {
		t.Fatalf("request = %v", got)
	}
	if !strings.Contains(out.String(), "App ID:         A1") || !strings.Contains(out.String(), "Client secret:  tcs_once") || !strings.Contains(out.String(), "shown only once") {
		t.Fatalf("output = %q", out.String())
	}

	out.Reset()
	if err := newApp(&out, &strings.Builder{}).createApp(context.Background(), api, body, true); err != nil {
		t.Fatal(err)
	}
	var printed map[string]string
	if err := json.Unmarshal([]byte(out.String()), &printed); err != nil || printed["app_id"] != "A1" || printed["client_secret"] != "tcs_once" || printed["client_id"] != "cid" {
		t.Fatalf("json = %q %v", out.String(), err)
	}
}

func TestRotateWebhookKeyPrintsTheNewSecret(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/apps/A1/webhooks/rotate-signing-key" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		w.Write([]byte(`{"success":true,"data":{"signing_key":"whsec_new"}}`))
	}))
	defer srv.Close()
	api := devapi.New(srv.URL, "org", fixedTokens{}, "test")
	var out strings.Builder
	if err := newApp(&out, &strings.Builder{}).rotateWebhookKey(context.Background(), api, "A1", true, ""); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != `{"app_id":"A1","webhook_signing_secret":"whsec_new"}` {
		t.Fatalf("json = %q", out.String())
	}
}

func TestRotateClientSecretPrintsTheNewSecret(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/apps/A1/regenerate-secret" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		w.Write([]byte(`{"success":true,"data":{"client_secret":"tcs_new"}}`))
	}))
	defer srv.Close()
	api := devapi.New(srv.URL, "org", fixedTokens{}, "test")
	var out strings.Builder
	if err := newApp(&out, &strings.Builder{}).rotateClientSecret(context.Background(), api, "A1", true, ""); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != `{"app_id":"A1","client_secret":"tcs_new"}` {
		t.Fatalf("json = %q", out.String())
	}
	out.Reset()
	if err := newApp(&out, &strings.Builder{}).rotateClientSecret(context.Background(), api, "A1", false, ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Client secret:  tcs_new") || !strings.Contains(out.String(), "shown only once") {
		t.Fatalf("output = %q", out.String())
	}
}

// --pipe-to hands the new secret to the given command on its standard input
// and never prints it; a failing command is reported with what to do next.
func TestRotateClientSecretPipesItIntoTheSecretStore(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell command")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"success":true,"data":{"client_secret":"tcs_piped"}}`))
	}))
	defer srv.Close()
	api := devapi.New(srv.URL, "org", fixedTokens{}, "test")
	stored := filepath.Join(t.TempDir(), "secret")
	var out, errOut strings.Builder
	if err := newApp(&out, &errOut).rotateClientSecret(context.Background(), api, "A1", false, "cat > "+stored); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(stored); string(got) != "tcs_piped" {
		t.Fatalf("stored = %q", got)
	}
	if strings.Contains(out.String()+errOut.String(), "tcs_piped") {
		t.Fatalf("the secret was printed: %q %q", out.String(), errOut.String())
	}
	err := newApp(&out, &errOut).rotateClientSecret(context.Background(), api, "A1", false, "exit 3")
	if err == nil || !strings.Contains(err.Error(), "run this command again") || strings.Contains(err.Error(), "tcs_piped") {
		t.Fatalf("failing store: %v", err)
	}
}
