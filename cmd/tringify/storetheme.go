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
// extra names scopes beyond the usual store access that this command needs;
// a saved login without them is replaced by one that has them.
func (a *app) storeTarget(ctx context.Context, storeID, storefrontID string, extra ...string) (*storeTarget, error) {
	session, err := a.session("store", storeID)
	if err != nil {
		return nil, err
	}
	if session != nil && !hasScopes(session.Account.Scope, extra) {
		a.printf("Sign in to store %s again to allow adding sample products and their images.\n", storeID)
		session = nil
	}
	if session == nil {
		if len(extra) == 0 {
			a.printf("Sign in to store %s to continue. Choose that store on the next screen.\n", storeID)
		}
		account, err := a.signInWith(ctx, "store", append(append([]string{}, auth.StoreScopes...), extra...))
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
		list := listAt(data, "storefronts")
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
// is validated. It returns the upload to import or sync. step, when set,
// reports each stage.
func (a *app) uploadBundle(ctx context.Context, t *storeTarget, bundle string, step func(string)) (string, error) {
	if step == nil {
		step = func(string) {}
	}
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
	step(fmt.Sprintf("Uploading %s…", sizeLabel(info.Size())))
	if err := upload.Put(ctx, &http.Client{Timeout: 10 * time.Minute}, grant, bundle); err != nil {
		return "", err
	}
	step("Checking the theme…")
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
	a.println("Packaging the theme…")
	bundle, name, cleanup, err := a.packageTemp(ctx, root)
	if err != nil {
		return "", "", err
	}
	defer cleanup()
	uploadID, err := a.uploadBundle(ctx, t, bundle, a.println)
	if err != nil {
		return "", "", err
	}
	a.printf("Adding %s to %s…\n", name, t.session.Account.Target.Name)
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

// themeIDFrom reads the theme_id an import returns.
func themeIDFrom(data any) string {
	m, _ := data.(map[string]any)
	return str(m["theme_id"])
}

// storeTheme finds one of the storefront's themes.
func (a *app) storeTheme(ctx context.Context, t *storeTarget, themeID string) (map[string]any, error) {
	data, err := t.client.Call(ctx, "list_storefront_themes", map[string]any{"storefront_id": t.storefrontID})
	if err != nil {
		return nil, err
	}
	for _, th := range listAt(data, "themes") {
		if str(th["id"]) == themeID {
			return th, nil
		}
	}
	return nil, nil
}

func (a *app) themePull(ctx context.Context, args []string) error {
	fs := newFlags("theme pull --store STORE_ID --theme THEME_ID [--storefront ID] [DIR]\n       tringify theme pull --listing ID [--version X.Y.Z] [DIR]", "Download a store theme's published files, or the exact package a version of your listing was published with, into a new directory.")
	storeID := fs.String("store", "", "store ID")
	themeID := fs.String("theme", "", "theme ID (see the store admin URL, or omit to list the store's themes)")
	storefrontID := fs.String("storefront", "", "storefront ID (default: the store's primary storefront)")
	listing := fs.String("listing", "", "theme listing ID, to download a published version instead of a store theme")
	versionFlag := fs.String("version", "", "with --listing: the version to download (default: the newest published)")
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	if *listing != "" {
		if *storeID != "" || *themeID != "" || *storefrontID != "" {
			return errors.New("use either --listing or --store, not both")
		}
		return a.pullListingVersion(ctx, *listing, *versionFlag, positional)
	}
	if *versionFlag != "" {
		return errors.New("--version needs --listing")
	}
	if *storeID == "" {
		return errors.New("--store or --listing is required. Find a store ID with `tringify store list` and listing IDs with `tringify theme listings`")
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
		for _, th := range listAt(data, "themes") {
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

// hasScopes reports whether a space-separated granted scope list includes
// every wanted scope.
func hasScopes(granted string, wanted []string) bool {
	have := map[string]bool{}
	for _, s := range strings.Fields(granted) {
		have[s] = true
	}
	for _, s := range wanted {
		if !have[s] {
			return false
		}
	}
	return true
}

// sizeLabel is a file size for progress lines, for example "1.4 MB".
func sizeLabel(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%d KB", n>>10)
	}
	return fmt.Sprintf("%d bytes", n)
}
