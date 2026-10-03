package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tringify/cli/internal/auth"
	"github.com/tringify/cli/internal/mcp"
	"github.com/tringify/cli/internal/theme"
	"github.com/tringify/cli/internal/upload"
)

// storeTarget is a signed-in store and the storefront theme commands act on.
type storeTarget struct {
	client       *mcp.Client
	session      *auth.Session
	storefrontID string
}

// storeTarget loads (or asks for) the store sign-in and picks the storefront.
func (a *app) storeTarget(ctx context.Context, storeID, storefrontID string) (*storeTarget, error) {
	session, err := a.session("store", storeID)
	if err != nil {
		return nil, err
	}
	if session == nil {
		a.printf("Sign in to store %s to continue. Choose that store on the next screen.\n", storeID)
		account, err := a.signIn(ctx, "store")
		if err != nil {
			return nil, err
		}
		if account.Target.ID != storeID {
			return nil, fmt.Errorf("you approved store %s (%s), not %s; run the command again and choose the right store", account.Target.Name, account.Target.ID, storeID)
		}
		if session, err = a.session("store", storeID); err != nil {
			return nil, err
		}
	}
	client := mcp.New(a.mcpEndpoint("store"), session, version)
	if storefrontID == "" {
		data, err := client.Call(ctx, "list_storefronts", nil)
		if err != nil {
			return nil, err
		}
		list := items(data)
		for _, sf := range list {
			if sf["is_primary"] == true {
				storefrontID = str(sf["id"])
			}
		}
		if storefrontID == "" && len(list) == 1 {
			storefrontID = str(list[0]["id"])
		}
		if storefrontID == "" {
			return nil, errors.New("could not choose a storefront; pass --storefront")
		}
	}
	return &storeTarget{client: client, session: session, storefrontID: storefrontID}, nil
}

// uploadBundle uploads a packaged theme for the storefront and waits until it
// is validated. It returns the upload to import or sync.
func (a *app) uploadBundle(ctx context.Context, t *storeTarget, bundle string) (string, error) {
	info, err := os.Stat(bundle)
	if err != nil {
		return "", err
	}
	data, err := t.client.Call(ctx, "create_theme_import_upload", map[string]any{"storefront_id": t.storefrontID, "original_filename": filepath.Base(bundle), "size_bytes": info.Size()})
	if err != nil {
		return "", err
	}
	var grant upload.Grant
	if err := decode(data, &grant); err != nil {
		return "", err
	}
	if err := upload.Put(ctx, &http.Client{Timeout: 10 * time.Minute}, grant, bundle); err != nil {
		return "", err
	}
	if _, err := t.client.Call(ctx, "finalize_theme_import_upload", map[string]any{"storefront_id": t.storefrontID, "intent_id": grant.IntentID}); err != nil {
		return "", err
	}
	return upload.Wait(ctx, func(ctx context.Context) (*upload.Status, error) {
		data, err := t.client.Call(ctx, "get_theme_import_upload_status", map[string]any{"storefront_id": t.storefrontID, "intent_id": grant.IntentID})
		if err != nil {
			return nil, err
		}
		var s upload.Status
		return &s, decode(data, &s)
	})
}

// importTheme packages the theme and adds it as a new unpublished theme.
func (a *app) importTheme(ctx context.Context, t *storeTarget, root string) (string, string, error) {
	bundle, name, cleanup, err := a.packageTemp(ctx, root)
	if err != nil {
		return "", "", err
	}
	defer cleanup()
	a.printf("Uploading %s to %s…\n", name, t.session.Account.Target.Name)
	uploadID, err := a.uploadBundle(ctx, t, bundle)
	if err != nil {
		return "", "", err
	}
	imported, err := t.client.Call(ctx, "import_theme", map[string]any{"storefront_id": t.storefrontID, "upload_id": uploadID})
	if err != nil {
		return "", "", err
	}
	themeID := themeIDFrom(imported)
	if themeID == "" {
		return "", "", errors.New("the theme was added, but the reply had no theme ID")
	}
	return themeID, name, nil
}

func themeIDFrom(data any) string {
	m, ok := data.(map[string]any)
	if !ok {
		return ""
	}
	if id := firstNonEmpty(str(m["theme_id"]), str(m["id"])); id != "" {
		return id
	}
	if t, ok := m["theme"].(map[string]any); ok {
		return str(t["id"])
	}
	return ""
}

// storeTheme finds one of the storefront's themes.
func (a *app) storeTheme(ctx context.Context, t *storeTarget, themeID string) (map[string]any, error) {
	data, err := t.client.Call(ctx, "list_storefront_themes", map[string]any{"storefront_id": t.storefrontID})
	if err != nil {
		return nil, err
	}
	for _, th := range items(data) {
		if str(th["id"]) == themeID {
			return th, nil
		}
	}
	return nil, nil
}

func (a *app) themePull(ctx context.Context, args []string) error {
	fs := newFlags("theme pull --store STORE_ID --theme THEME_ID [--storefront ID] [DIR]", "Download a store theme's published files into a new directory. Use it to start from a theme that was changed in the store admin.")
	storeID := fs.String("store", "", "store ID (required)")
	themeID := fs.String("theme", "", "theme ID (required; see the store admin URL, or omit to list the themes)")
	storefrontID := fs.String("storefront", "", "storefront ID (default: the store's primary storefront)")
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	if *storeID == "" {
		return errors.New("--store is required. Find the ID with `tringify store list`")
	}
	target, err := a.storeTarget(ctx, *storeID, *storefrontID)
	if err != nil {
		return err
	}
	if *themeID == "" {
		data, err := target.client.Call(ctx, "list_storefront_themes", map[string]any{"storefront_id": target.storefrontID})
		if err != nil {
			return err
		}
		a.println("Pass --theme with one of these:")
		for _, th := range items(data) {
			state := "unpublished"
			if th["is_active"] == true {
				state = "published"
			}
			a.printf("  %s  %s  %s\n", str(th["id"]), str(th["name"]), state)
		}
		return nil
	}
	data, err := target.client.Call(ctx, "export_theme", map[string]any{"storefront_id": target.storefrontID, "theme_id": *themeID})
	if err != nil {
		return err
	}
	var pkg struct {
		Filename  string `json:"filename"`
		ZipBase64 string `json:"zip_base64"`
	}
	if err := decode(data, &pkg); err != nil {
		return err
	}
	raw, err := base64.StdEncoding.DecodeString(pkg.ZipBase64)
	if err != nil || len(raw) == 0 {
		return errors.New("the store returned no theme package")
	}
	dir := strings.TrimSuffix(pkg.Filename, filepath.Ext(pkg.Filename))
	if len(positional) > 0 {
		dir = positional[0]
	}
	if len(positional) > 1 {
		return fmt.Errorf("unexpected argument %q", positional[1])
	}
	dest, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	count, err := theme.Unzip(raw, dest)
	if err != nil {
		return err
	}
	a.printf("Downloaded %d files to %s.\n", count, dest)
	a.println("These are compiled files: sections are .vasc, not src/. Edit them directly, or move sections into src/ to use theme build.")
	return nil
}
