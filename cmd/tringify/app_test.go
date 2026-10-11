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
		w.Write([]byte(`{"success":true,"data":{"signing_key":"whsec_new","previous_key_expires_at":"2026-10-14T09:30:00Z"}}`))
	}))
	defer srv.Close()
	api := devapi.New(srv.URL, "org", fixedTokens{}, "test")
	var out strings.Builder
	if err := newApp(&out, &strings.Builder{}).rotateWebhookKey(context.Background(), api, "A1", true, ""); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != `{"app_id":"A1","previous_key_expires_at":"2026-10-14T09:30:00Z","webhook_signing_secret":"whsec_new"}` {
		t.Fatalf("json = %q", out.String())
	}
	out.Reset()
	if err := newApp(&out, &strings.Builder{}).rotateWebhookKey(context.Background(), api, "A1", false, ""); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Webhook signing secret:  whsec_new", "shown only once", "Until 2026-10-14 09:30 UTC", "finish-rotation"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output lacks %q: %q", want, out.String())
		}
	}
}

// The first secret an app gets has nothing to overlap with.
func TestRotateWebhookKeyWithoutAPreviousSecret(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"success":true,"data":{"signing_key":"whsec_first","previous_key_expires_at":null}}`))
	}))
	defer srv.Close()
	api := devapi.New(srv.URL, "org", fixedTokens{}, "test")
	var out strings.Builder
	if err := newApp(&out, &strings.Builder{}).rotateWebhookKey(context.Background(), api, "A1", true, ""); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != `{"app_id":"A1","previous_key_expires_at":null,"webhook_signing_secret":"whsec_first"}` {
		t.Fatalf("json = %q", out.String())
	}
	out.Reset()
	if err := newApp(&out, &strings.Builder{}).rotateWebhookKey(context.Background(), api, "A1", false, ""); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "previous secret") {
		t.Fatalf("output = %q", out.String())
	}
}

// --pipe-to never prints the secret but still says when signatures made with
// the previous one stop; a failing store command names the secret that was
// current before the rotation and warns that rotating again drops it.
func TestRotateWebhookKeyPipesItAndStatesTheOverlap(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell command")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"success":true,"data":{"signing_key":"whsec_piped","previous_key_expires_at":"2026-10-14T09:30:00Z"}}`))
	}))
	defer srv.Close()
	api := devapi.New(srv.URL, "org", fixedTokens{}, "test")
	stored := filepath.Join(t.TempDir(), "secret")
	var out, errOut strings.Builder
	if err := newApp(&out, &errOut).rotateWebhookKey(context.Background(), api, "A1", false, "cat > "+stored); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(stored); string(got) != "whsec_piped" {
		t.Fatalf("stored = %q", got)
	}
	if strings.Contains(out.String()+errOut.String(), "whsec_piped") || !strings.Contains(out.String(), "Until 2026-10-14 09:30 UTC") {
		t.Fatalf("output = %q %q", out.String(), errOut.String())
	}
	err := newApp(&out, &errOut).rotateWebhookKey(context.Background(), api, "A1", false, "exit 3")
	if err == nil || !strings.Contains(err.Error(), "Until 2026-10-14 09:30 UTC") || !strings.Contains(err.Error(), "current before this command ran") || !strings.Contains(err.Error(), "stops those signatures at once") || strings.Contains(err.Error(), "secret your app uses now") || strings.Contains(err.Error(), "whsec_piped") {
		t.Fatalf("failing store: %v", err)
	}
}

// A store command that echoes its input must not get the secret printed: its
// standard output is dropped, and its standard error is redacted when it fails.
func TestPipeToNeverForwardsTheSecretFromTheCommandsOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell command")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"success":true,"data":{"signing_key":"whsec_echoed","previous_key_expires_at":"2026-10-14T09:30:00Z"}}`))
	}))
	defer srv.Close()
	api := devapi.New(srv.URL, "org", fixedTokens{}, "test")

	var out, errOut strings.Builder
	if err := newApp(&out, &errOut).rotateWebhookKey(context.Background(), api, "A1", false, "tee /dev/stderr"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String()+errOut.String(), "whsec_echoed") || !strings.Contains(out.String(), "stored it with: tee /dev/stderr") {
		t.Fatalf("succeeding echo: %q %q", out.String(), errOut.String())
	}

	out.Reset()
	errOut.Reset()
	err := newApp(&out, &errOut).rotateWebhookKey(context.Background(), api, "A1", false, `read s; echo "bad secret: $s" >&2; echo "$s"; exit 3`)
	if err == nil || strings.Contains(err.Error(), "whsec_echoed") {
		t.Fatalf("failing echo: %v", err)
	}
	if strings.Contains(out.String()+errOut.String(), "whsec_echoed") || !strings.Contains(errOut.String(), "bad secret: [redacted]") {
		t.Fatalf("failing echo: %q %q", out.String(), errOut.String())
	}
}

func TestFinishWebhookKeyRotation(t *testing.T) {
	inProgress := true
	finished := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/apps/A1":
			if inProgress {
				w.Write([]byte(`{"success":true,"data":{"id":"A1","signing_key_rotation":{"in_progress":true,"previous_key_expires_at":"2026-10-14T09:30:00Z"}}}`))
			} else {
				w.Write([]byte(`{"success":true,"data":{"id":"A1","signing_key_rotation":{"in_progress":false,"previous_key_expires_at":null}}}`))
			}
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/apps/A1/webhooks/finish-signing-key-rotation":
			finished++
			w.Write([]byte(`{"success":true,"data":{}}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()
	api := devapi.New(srv.URL, "org", fixedTokens{}, "test")

	var out strings.Builder
	a := newApp(&out, &strings.Builder{})
	a.stdin = strings.NewReader("n\n")
	if err := a.finishWebhookKeyRotation(context.Background(), api, "A1", false); err != nil {
		t.Fatal(err)
	}
	if finished != 0 || !strings.Contains(out.String(), "until 2026-10-14 09:30 UTC") || !strings.Contains(out.String(), "Rotation not finished.") {
		t.Fatalf("declined: finished=%d output=%q", finished, out.String())
	}

	out.Reset()
	a.stdin = strings.NewReader("y\n")
	if err := a.finishWebhookKeyRotation(context.Background(), api, "A1", false); err != nil {
		t.Fatal(err)
	}
	if finished != 1 || !strings.Contains(out.String(), "Finished the rotation.") {
		t.Fatalf("confirmed: finished=%d output=%q", finished, out.String())
	}

	inProgress = false
	out.Reset()
	if err := a.finishWebhookKeyRotation(context.Background(), api, "A1", true); err != nil {
		t.Fatal(err)
	}
	if finished != 1 || !strings.Contains(out.String(), "No signing secret rotation is in progress") {
		t.Fatalf("nothing to finish: finished=%d output=%q", finished, out.String())
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
