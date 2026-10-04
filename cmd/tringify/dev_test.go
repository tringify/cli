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
	// import_theme returns {theme_id, name}; nothing else is read as an ID.
	if got := themeIDFrom(map[string]any{"theme_id": "t1", "name": "Starter"}); got != "t1" {
		t.Fatalf("themeIDFrom = %q", got)
	}
	if got := themeIDFrom(map[string]any{"id": "t1"}); got != "" {
		t.Fatalf("themeIDFrom read an undocumented field: %q", got)
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

func TestSizeLabel(t *testing.T) {
	for n, want := range map[int64]string{512: "512 bytes", 2048: "2 KB", 1468006: "1.4 MB"} {
		if got := sizeLabel(n); got != want {
			t.Errorf("sizeLabel(%d) = %q, want %q", n, got, want)
		}
	}
}
