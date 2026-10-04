package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/tringify/cli/internal/auth"
	"github.com/tringify/cli/internal/mcp"
	"github.com/tringify/cli/internal/themekit"
)

// devState remembers, per store, the unpublished theme `theme dev` syncs into.
// It lives in .tringify/dev.json in the theme directory and is never packaged.
type devState struct {
	Stores map[string]devTarget `json:"stores"`
}

type devTarget struct {
	StorefrontID string `json:"storefront_id"`
	ThemeID      string `json:"theme_id"`
}

func devStatePath(root string) string { return filepath.Join(root, ".tringify", "dev.json") }

func loadDevState(root string) devState {
	state := devState{Stores: map[string]devTarget{}}
	if raw, err := os.ReadFile(devStatePath(root)); err == nil {
		_ = json.Unmarshal(raw, &state)
		if state.Stores == nil {
			state.Stores = map[string]devTarget{}
		}
	}
	return state
}

func saveDevState(root string, state devState) error {
	path := devStatePath(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o644)
}

func (a *app) themeDev(ctx context.Context, args []string) error {
	fs := newFlags("theme dev --store STORE_ID [--storefront ID] [--theme THEME_ID] [DIR]", "Sync the theme into an unpublished theme on a store while you edit. The first run adds the theme; after that every saved change is built, validated and synced. The store's published theme is never changed. Press Ctrl+C to stop.")
	storeID := fs.String("store", "", "store ID (required; use a development store)")
	storefrontID := fs.String("storefront", "", "storefront ID (default: the store's primary storefront)")
	themeFlag := fs.String("theme", "", "sync into this unpublished theme instead of the one theme dev added")
	interval := fs.Duration("interval", time.Second, "how often to look for changes")
	withDemo := fs.Bool("with-demo", false, "also add the theme's demo products and collections to the store (development stores only)")
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	if *storeID == "" {
		return errors.New("--store is required. Find the ID with `tringify store list`")
	}
	root, err := rootArg(positional)
	if err != nil {
		return err
	}
	state := loadDevState(root)
	saved := state.Stores[*storeID]
	if *storefrontID == "" {
		*storefrontID = saved.StorefrontID
	}
	var extra []string
	if *withDemo {
		extra = auth.SampleContentScopes
	}
	target, err := a.storeTarget(ctx, *storeID, *storefrontID, extra...)
	if err != nil {
		return err
	}
	themeID := *themeFlag
	if themeID == "" && saved.StorefrontID == target.storefrontID {
		themeID = saved.ThemeID
	}
	if themeID != "" {
		th, err := a.storeTheme(ctx, target, themeID)
		if err != nil {
			return err
		}
		switch {
		case th == nil && *themeFlag != "":
			return fmt.Errorf("theme %s is not on this storefront", themeID)
		case th == nil:
			a.println("The theme theme dev added earlier is gone; adding a new one.")
			themeID = ""
		case th["is_active"] == true:
			return fmt.Errorf("theme %s is the published theme; theme dev only syncs unpublished themes", themeID)
		case str(th["source"]) != "uploaded":
			return fmt.Errorf("theme %s was not added from a theme package; pick a theme added by theme push or theme dev", themeID)
		}
	}

	var name string
	signature, _ := themekit.SourceSignature(root)
	if themeID == "" {
		if themeID, name, err = a.importTheme(ctx, target, root); err != nil {
			return err
		}
		a.printf("Added %s as an unpublished theme (theme %s).\n", name, themeID)
	} else if err := a.syncTheme(ctx, target, root, themeID); err != nil {
		if errors.Is(err, mcp.ErrUnauthorized) || errors.Is(err, mcp.ErrInsufficientScope) {
			return err
		}
		a.printf("Sync failed: %v\n", err)
	}
	state.Stores[*storeID] = devTarget{StorefrontID: target.storefrontID, ThemeID: themeID}
	if err := saveDevState(root, state); err != nil {
		return err
	}
	if *withDemo {
		if err := a.addSampleContent(ctx, target, root); err != nil {
			if errors.Is(err, mcp.ErrUnauthorized) {
				return err
			}
			a.printf("Sample products were not added: %v\n", err)
		}
	}
	a.printf("Syncing into theme %s on %s. Preview it in the store admin under Online Store → Themes.\n", themeID, target.session.Account.Target.Name)
	a.println("Watching for changes. Press Ctrl+C to stop.")

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(*interval):
		}
		next, err := themekit.SourceSignature(root)
		if err != nil || next == signature {
			continue
		}
		// Let a burst of saves settle before building.
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(400 * time.Millisecond):
			}
			again, err := themekit.SourceSignature(root)
			if err != nil || again == next {
				break
			}
			next = again
		}
		signature = next
		if err := a.syncTheme(ctx, target, root, themeID); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if errors.Is(err, mcp.ErrUnauthorized) || errors.Is(err, mcp.ErrInsufficientScope) {
				return err
			}
			a.printf("%s  sync failed: %v\n", time.Now().Format("15:04:05"), err)
		}
	}
}

// syncTheme packages the theme and replaces the unpublished theme's code.
func (a *app) syncTheme(ctx context.Context, t *storeTarget, root, themeID string) error {
	started := time.Now()
	bundle, _, cleanup, err := a.packageTemp(ctx, root)
	if err != nil {
		return err
	}
	defer cleanup()
	uploadID, err := a.uploadBundle(ctx, t, bundle, nil)
	if err != nil {
		return err
	}
	if _, err := t.client.Call(ctx, "sync_theme", map[string]any{"storefront_id": t.storefrontID, "theme_id": themeID, "upload_id": uploadID}); err != nil {
		return err
	}
	a.printf("%s  synced in %.1fs\n", time.Now().Format("15:04:05"), time.Since(started).Seconds())
	return nil
}
