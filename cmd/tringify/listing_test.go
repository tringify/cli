package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/tringify/cli/internal/devapi"
)

type fixedTokens struct{}

func (fixedTokens) AccessToken(context.Context) (string, error)  { return "token", nil }
func (fixedTokens) ForceRefresh(context.Context) (string, error) { return "token", nil }

func versionsServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" || r.Header.Get("X-Organization-ID") != "org" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/v1/org/themes/L":
			w.Write([]byte(`{"success":true,"data":{"id":"L","name":"Aurora"}}`))
		case "/api/v1/org/themes/other":
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"code":"NOT_FOUND","message":"Listing not found"}`))
		case "/api/v1/org/themes/L/versions":
			w.Write([]byte(`{"success":true,"data":[{"id":"v3","version":"1.3.0","status":"draft"},{"id":"v2","version":"1.2.0","status":"published"},{"id":"v1","version":"1.1.0","status":"published"}]}`))
		case "/api/v1/org/themes/L/versions/v2/package":
			w.Header().Set("Content-Type", "application/zip")
			w.Header().Set("Content-Disposition", `attachment; filename="aurora-1.2.0.zip"`)
			w.Write([]byte("PK-zip"))
		case "/api/v1/org/themes/L/versions/v1/package":
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"code":"NOT_FOUND","message":"Theme version not found"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func TestResolveAndDownloadListingVersions(t *testing.T) {
	srv := versionsServer(t)
	defer srv.Close()
	api := devapi.New(srv.URL, "org", fixedTokens{}, "test")
	ctx := context.Background()
	// Without a name: the newest published version, never a draft.
	v, ok, err := resolveVersion(ctx, api, "L", "")
	if err != nil || !ok || v.ID != "v2" {
		t.Fatalf("newest published = %+v %v %v", v, ok, err)
	}
	if v, ok, err = resolveVersion(ctx, api, "L", "1.1.0"); err != nil || !ok || v.ID != "v1" {
		t.Fatalf("named version = %+v %v %v", v, ok, err)
	}
	if _, _, err = resolveVersion(ctx, api, "L", "9.9.9"); err == nil || !strings.Contains(err.Error(), "no version 9.9.9") {
		t.Fatalf("unknown version: %v", err)
	}
	if _, _, err = resolveVersion(ctx, api, "other", ""); err == nil || !strings.Contains(err.Error(), "is not one of your") {
		t.Fatalf("foreign listing: %v", err)
	}
	raw, name, err := downloadVersion(ctx, api, "L", listingVersion{ID: "v2", Version: "1.2.0"})
	if err != nil || string(raw) != "PK-zip" || name != "aurora-1.2.0.zip" {
		t.Fatalf("download = %q %q %v", raw, name, err)
	}
	if _, _, err = downloadVersion(ctx, api, "L", listingVersion{ID: "v1", Version: "1.1.0"}); err == nil || !strings.Contains(err.Error(), "Theme version not found") {
		t.Fatalf("API error not reported: %v", err)
	}
}

func TestConfirmNeedsAnAnswerOrATerminal(t *testing.T) {
	var out bytes.Buffer
	if ok, err := confirm(strings.NewReader("y\n"), &out, "Publish?"); err != nil || !ok || out.String() != "Publish? [y/N] " {
		t.Fatalf("yes = %v %v %q", ok, err, out.String())
	}
	if ok, err := confirm(strings.NewReader("\n"), &out, "Publish?"); err != nil || ok {
		t.Fatalf("empty answer published: %v %v", ok, err)
	}
	// A pipe is not a terminal: refuse instead of guessing.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	w.Close()
	if _, err := confirm(r, &out, "Publish?"); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("non-terminal: %v", err)
	}
}
