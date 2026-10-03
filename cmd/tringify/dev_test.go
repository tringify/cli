package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDevStateRemembersTheThemePerStore(t *testing.T) {
	root := t.TempDir()
	if got := loadDevState(root); len(got.Stores) != 0 {
		t.Fatalf("fresh state = %v", got)
	}
	state := loadDevState(root)
	state.Stores["store-a"] = devTarget{StorefrontID: "front", ThemeID: "theme-1"}
	if err := saveDevState(root, state); err != nil {
		t.Fatal(err)
	}
	if got := loadDevState(root).Stores["store-a"]; got.ThemeID != "theme-1" || got.StorefrontID != "front" {
		t.Fatalf("reloaded = %+v", got)
	}
	if _, err := os.Stat(filepath.Join(root, ".tringify", "dev.json")); err != nil {
		t.Fatal(err)
	}
}

func TestSubdomainAndThemeIDHelpers(t *testing.T) {
	if got := subdomainFor("  Theme Lab #2 "); got != "theme-lab-2" {
		t.Fatalf("subdomain = %q", got)
	}
	for _, reply := range []any{map[string]any{"theme_id": "t1"}, map[string]any{"id": "t1"}, map[string]any{"theme": map[string]any{"id": "t1"}}} {
		if got := themeIDFrom(reply); got != "t1" {
			t.Fatalf("themeIDFrom(%v) = %q", reply, got)
		}
	}
}
