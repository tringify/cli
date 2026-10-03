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

func TestHasScopes(t *testing.T) {
	granted := "connection:read store:online_store.storefronts:write store:products:write"
	if !hasScopes(granted, nil) || !hasScopes(granted, []string{"store:products:write"}) {
		t.Fatal("granted scopes not recognised")
	}
	if hasScopes(granted, []string{"store:products:write", "store:files:write"}) {
		t.Fatal("a missing scope was accepted")
	}
}

func TestDemoImageNamesListsOnlyFiles(t *testing.T) {
	root := t.TempDir()
	if names, err := demoImageNames(root); err != nil || names != nil {
		t.Fatalf("no demo folder = %v, %v", names, err)
	}
	dir := filepath.Join(root, "demo", "images")
	os.MkdirAll(filepath.Join(dir, "nested"), 0o755)
	os.WriteFile(filepath.Join(dir, "b.jpg"), []byte("b"), 0o644)
	os.WriteFile(filepath.Join(dir, "a.png"), []byte("a"), 0o644)
	if names, err := demoImageNames(root); err != nil || len(names) != 2 || names[0] != "a.png" || names[1] != "b.jpg" {
		t.Fatalf("names = %v, %v", names, err)
	}
}
