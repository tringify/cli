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

func TestCategoryIDAcceptsSlugNameOrID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/org/theme-categories" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write([]byte(`{"success":true,"data":{"categories":[{"id":"c-1","slug":"home-living","name":"Home & Living"}]}}`))
	}))
	defer srv.Close()
	api := devapi.New(srv.URL, "org", fixedTokens{}, "test")
	for _, want := range []string{"home-living", "Home & Living", "c-1"} {
		if id, err := categoryID(context.Background(), api, want); err != nil || id != "c-1" {
			t.Fatalf("%q = %q %v", want, id, err)
		}
	}
	if _, err := categoryID(context.Background(), api, "garden"); err == nil || !strings.Contains(err.Error(), "tringify theme categories") {
		t.Fatalf("unknown category: %v", err)
	}
}

func TestListingMediaMustBeAnImage(t *testing.T) {
	dir := t.TempDir()
	for name, ok := range map[string]bool{"cover.png": true, "shot.JPG": true, "a.webp": true, "theme.zip": false, "notes.txt": false} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := mediaType(p); (err == nil) != ok {
			t.Fatalf("%s accepted=%v err=%v", name, err == nil, err)
		}
	}
	if _, err := mediaType(dir); err == nil {
		t.Fatal("a directory was accepted")
	}
}
