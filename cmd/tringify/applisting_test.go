package main

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/tringify/cli/internal/devapi"
)

func writeIcon(t *testing.T, dir string, w, h int) string {
	t.Helper()
	p := filepath.Join(dir, "icon.png")
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAppIconMustBe512Square(t *testing.T) {
	dir := t.TempDir()
	if err := checkIcon(writeIcon(t, dir, 512, 512)); err != nil {
		t.Fatalf("512×512 rejected: %v", err)
	}
	if err := checkIcon(writeIcon(t, dir, 600, 512)); err == nil || !strings.Contains(err.Error(), "512×512") {
		t.Fatalf("600×512 accepted: %v", err)
	}
}

func TestAppListingUpdateKeepsFieldsAndUploadsIcon(t *testing.T) {
	dir := t.TempDir()
	icon := writeIcon(t, dir, 512, 512)
	var mu sync.Mutex
	var put map[string]any
	var intentCategory string
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/apps/app-1":
			w.Write([]byte(`{"success":true,"data":{"name":"Custom Products","tagline":"Old tagline","support_email":"s@example.com","icon_file_id":"old-icon","screenshot_file_ids":["s1","s2"]}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/org/files/upload-intents":
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			intentCategory, _ = body["category"].(string)
			w.Write([]byte(`{"success":true,"data":{"intent_id":"new-icon","url":"` + srv.URL + `/bucket","headers":{"Content-Type":"image/png"}}}`))
		case r.Method == http.MethodPut && r.URL.Path == "/bucket":
			io.Copy(io.Discard, r.Body)
		case r.URL.Path == "/api/v1/org/files/upload-intents/finalize":
			w.Write([]byte(`{"success":true,"data":{}}`))
		case r.URL.Path == "/api/v1/org/files/upload-intents/status":
			w.Write([]byte(`{"success":true,"data":{"intents":[{"status":"ready","file":{"id":"new-icon"}}]}}`))
		case r.Method == http.MethodPut && r.URL.Path == "/api/v1/apps/app-1/listing":
			json.NewDecoder(r.Body).Decode(&put)
			w.Write([]byte(`{"success":true,"data":{}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	var out bytes.Buffer
	a := &app{stdout: &out, stderr: &out}
	api := devapi.New(srv.URL, "org", fixedTokens{}, "test")
	api.HTTP = srv.Client()
	a.uploadHTTP = srv.Client()
	if err := a.updateAppListing(context.Background(), api, "app-1", appListingChange{icon: icon}); err != nil {
		t.Fatal(err)
	}
	if intentCategory != "app_icon" {
		t.Fatalf("upload category = %q", intentCategory)
	}
	if put["icon_file_id"] != "new-icon" || put["tagline"] != "Old tagline" || put["support_email"] != "s@example.com" {
		t.Fatalf("listing body = %v", put)
	}
	if shots, _ := put["screenshot_file_ids"].([]any); len(shots) != 2 {
		t.Fatalf("screenshots dropped: %v", put["screenshot_file_ids"])
	}
}
