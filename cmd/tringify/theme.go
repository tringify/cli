package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tringify/cli/internal/auth"
	"github.com/tringify/cli/internal/devapi"
	"github.com/tringify/cli/internal/mcp"
	"github.com/tringify/cli/internal/theme"
	"github.com/tringify/cli/internal/themetools"
	"github.com/tringify/cli/internal/upload"
)

func (a *app) tools() (*themetools.Runner, error) {
	return themetools.New(a.stdout, a.stderr, a.println)
}

func rootArg(positional []string) (string, error) {
	root := "."
	if len(positional) > 0 {
		root = positional[0]
	}
	if len(positional) > 1 {
		return "", fmt.Errorf("unexpected argument %q", positional[1])
	}
	return filepath.Abs(root)
}

func (a *app) themeInit(ctx context.Context, args []string) error {
	fs := newFlags("theme init [DIR] [--name NAME]", "Create a new theme from the Tringify starter theme (github.com/tringify/theme-starter).")
	name := fs.String("name", "", "theme display name (default: derived from DIR)")
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	dir := "my-theme"
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
	if _, err := os.Stat(dest); err == nil {
		return fmt.Errorf("%s already exists", dir)
	}
	if *name == "" {
		words := strings.FieldsFunc(filepath.Base(dest), func(r rune) bool { return r == '-' || r == '_' || r == ' ' })
		for i, w := range words {
			words[i] = strings.ToUpper(w[:1]) + w[1:]
		}
		*name = strings.Join(words, " ")
	}
	tools, err := a.tools()
	if err != nil {
		return err
	}
	a.println("Downloading the starter theme…")
	starter, cleanup, err := theme.DownloadStarter(ctx, &http.Client{Timeout: 2 * time.Minute})
	if err != nil {
		return err
	}
	defer cleanup()
	if err := tools.Run(ctx, filepath.Dir(dest), "init", dest, "--from", starter, "--name", *name); err != nil {
		return err
	}
	a.printf("\nCreated %s. Next:\n  cd %s\n  tringify theme preview\n", *name, dir)
	return nil
}

func (a *app) themeTool(ctx context.Context, command string, args []string) error {
	tools, err := a.tools()
	if err != nil {
		return err
	}
	return tools.Run(ctx, ".", append([]string{command}, args...)...)
}

func (a *app) themePackage(ctx context.Context, args []string) error {
	fs := newFlags("theme package [DIR] [--output FILE]", "Build, validate and write an upload-ready ZIP.")
	output := fs.String("output", "", "ZIP path (default: dist/<theme>.zip)")
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	root, err := rootArg(positional)
	if err != nil {
		return err
	}
	name, err := theme.Name(root)
	if err != nil {
		return err
	}
	if *output == "" {
		*output = filepath.Join(root, "dist", theme.Slug(name)+".zip")
	}
	out, err := filepath.Abs(*output)
	if err != nil {
		return err
	}
	return a.packageTo(ctx, root, out)
}

func (a *app) packageTo(ctx context.Context, root, out string) error {
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	tools, err := a.tools()
	if err != nil {
		return err
	}
	return tools.Run(ctx, root, "package", root, out)
}

// packageTemp packages the theme into a temporary ZIP.
func (a *app) packageTemp(ctx context.Context, root string) (string, string, func(), error) {
	name, err := theme.Name(root)
	if err != nil {
		return "", "", nil, err
	}
	work, err := os.MkdirTemp("", "tringify-theme-")
	if err != nil {
		return "", "", nil, err
	}
	cleanup := func() { os.RemoveAll(work) }
	out := filepath.Join(work, theme.Slug(name)+".zip")
	if err := a.packageTo(ctx, root, out); err != nil {
		cleanup()
		return "", "", nil, err
	}
	return out, name, cleanup, nil
}

func decode(in any, out any) error {
	raw, err := json.Marshal(in)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

// items finds the list in a response that is either a list or an object
// holding one.
func items(data any) []map[string]any {
	var list []any
	switch v := data.(type) {
	case []any:
		list = v
	case map[string]any:
		for _, key := range []string{"items", "storefronts", "listings", "themes", "data"} {
			if l, ok := v[key].([]any); ok {
				list = l
				break
			}
		}
	}
	out := []map[string]any{}
	for _, item := range list {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func (a *app) themePush(ctx context.Context, args []string) error {
	fs := newFlags("theme push --store STORE_ID [--storefront ID] [DIR]", "Package the theme and add it to a store as a new, unpublished theme. Use a development store. The store's live theme does not change.")
	storeID := fs.String("store", "", "store ID (required)")
	storefrontID := fs.String("storefront", "", "storefront ID (default: the store's primary storefront)")
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	if *storeID == "" {
		return errors.New("--store is required. Find the ID in the store's admin URL or the Developer Portal")
	}
	root, err := rootArg(positional)
	if err != nil {
		return err
	}
	session, err := a.session("store", *storeID)
	if err != nil {
		return err
	}
	if session == nil {
		a.printf("Sign in to store %s to continue. Choose that store on the next screen.\n", *storeID)
		account, err := a.signIn(ctx, "store")
		if err != nil {
			return err
		}
		if account.Target.ID != *storeID {
			return fmt.Errorf("you approved store %s (%s), not %s; run the command again and choose the right store", account.Target.Name, account.Target.ID, *storeID)
		}
		if session, err = a.session("store", *storeID); err != nil {
			return err
		}
	}
	client := mcp.New(a.mcpEndpoint("store"), session, version)
	if *storefrontID == "" {
		data, err := client.Call(ctx, "list_storefronts", nil)
		if err != nil {
			return err
		}
		list := items(data)
		for _, sf := range list {
			if sf["is_primary"] == true {
				*storefrontID = str(sf["id"])
			}
		}
		if *storefrontID == "" && len(list) == 1 {
			*storefrontID = str(list[0]["id"])
		}
		if *storefrontID == "" {
			return errors.New("could not choose a storefront; pass --storefront")
		}
	}
	bundle, name, cleanup, err := a.packageTemp(ctx, root)
	if err != nil {
		return err
	}
	defer cleanup()
	info, err := os.Stat(bundle)
	if err != nil {
		return err
	}
	a.printf("Uploading %s (%d KB) to %s…\n", filepath.Base(bundle), (info.Size()+1023)/1024, session.Account.Target.Name)
	data, err := client.Call(ctx, "create_theme_import_upload", map[string]any{"storefront_id": *storefrontID, "original_filename": filepath.Base(bundle), "size_bytes": info.Size()})
	if err != nil {
		return err
	}
	var grant upload.Grant
	if err := decode(data, &grant); err != nil {
		return err
	}
	if err := upload.Put(ctx, &http.Client{Timeout: 10 * time.Minute}, grant, bundle); err != nil {
		return err
	}
	if _, err := client.Call(ctx, "finalize_theme_import_upload", map[string]any{"storefront_id": *storefrontID, "intent_id": grant.IntentID}); err != nil {
		return err
	}
	uploadID, err := upload.Wait(ctx, func(ctx context.Context) (*upload.Status, error) {
		data, err := client.Call(ctx, "get_theme_import_upload_status", map[string]any{"storefront_id": *storefrontID, "intent_id": grant.IntentID})
		if err != nil {
			return nil, err
		}
		var s upload.Status
		return &s, decode(data, &s)
	})
	if err != nil {
		return err
	}
	imported, err := client.Call(ctx, "import_theme", map[string]any{"storefront_id": *storefrontID, "upload_id": uploadID})
	if err != nil {
		return err
	}
	themeID := ""
	if m, ok := imported.(map[string]any); ok {
		themeID = str(m["id"])
		if themeID == "" {
			if t, ok := m["theme"].(map[string]any); ok {
				themeID = str(t["id"])
			}
		}
	}
	a.printf("Added %s to %s as an unpublished theme", name, session.Account.Target.Name)
	if themeID != "" {
		a.printf(" (theme %s)", themeID)
	}
	a.println(". Preview and publish it from the store admin under Online Store → Themes.")
	return nil
}

func (a *app) devAPI() (*devapi.Client, *auth.Session, error) {
	session, err := a.session("organization", "")
	if err != nil {
		return nil, nil, err
	}
	return devapi.New(a.devAPIBase, session.Account.Target.ID, session, version), session, nil
}

func (a *app) themeListings(ctx context.Context, args []string) error {
	fs := newFlags("theme listings", "List the theme listings in your developer organization.")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	api, session, err := a.devAPI()
	if err != nil {
		return err
	}
	var data any
	if err := api.Do(ctx, http.MethodGet, "/org/themes", nil, &data); err != nil {
		return err
	}
	list := items(data)
	if len(list) == 0 {
		a.printf("No theme listings in %s. Create one in the Developer Portal under Themes.\n", session.Account.Target.Name)
		return nil
	}
	for _, l := range list {
		a.printf("%s  %s  %s\n", str(l["id"]), firstNonEmpty(str(l["name"]), str(l["slug"])), str(l["status"]))
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func (a *app) themePublish(ctx context.Context, args []string) error {
	fs := newFlags("theme publish --listing ID --version X.Y.Z [DIR]", "Package the theme from a clean git checkout and publish it as a new version of an existing listing. The version records the source commit.")
	listing := fs.String("listing", "", "theme listing ID (see `tringify theme listings`)")
	versionFlag := fs.String("version", "", "version to publish, for example 1.2.0")
	notes := fs.String("notes", "", "release notes")
	breaking := fs.Bool("breaking", false, "mark the version as containing breaking changes")
	installStore := fs.String("install-store", "", "also install the published version into this organization development store")
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	if *listing == "" || *versionFlag == "" {
		return errors.New("--listing and --version are required")
	}
	if !theme.ValidVersion(*versionFlag) {
		return fmt.Errorf("%q is not a version like 1.2.0", *versionFlag)
	}
	root, err := rootArg(positional)
	if err != nil {
		return err
	}
	source, err := theme.GitSource(ctx, root)
	if err != nil {
		return err
	}
	if source.Dirty {
		return errors.New("the git work tree has uncommitted or untracked changes. Commit or stash them so the published version matches a commit")
	}
	api, session, err := a.devAPI()
	if err != nil {
		return err
	}
	bundle, name, cleanup, err := a.packageTemp(ctx, root)
	if err != nil {
		return err
	}
	defer cleanup()
	info, err := os.Stat(bundle)
	if err != nil {
		return err
	}
	a.printf("Uploading %s %s from commit %s to %s…\n", name, *versionFlag, source.Commit[:12], session.Account.Target.Name)
	var grant upload.Grant
	if err := api.Do(ctx, http.MethodPost, "/org/themes/upload", map[string]any{"original_filename": filepath.Base(bundle), "mime_type": "application/zip", "size_bytes": info.Size()}, &grant); err != nil {
		return err
	}
	if err := upload.Put(ctx, &http.Client{Timeout: 10 * time.Minute}, grant, bundle); err != nil {
		return err
	}
	if err := api.Do(ctx, http.MethodPost, "/org/themes/upload/finalize", map[string]any{"intent_id": grant.IntentID}, nil); err != nil {
		return err
	}
	uploadID, err := upload.Wait(ctx, func(ctx context.Context) (*upload.Status, error) {
		var s upload.Status
		return &s, api.Do(ctx, http.MethodPost, "/org/themes/upload/status", map[string]any{"intent_id": grant.IntentID}, &s)
	})
	if err != nil {
		return err
	}
	var published map[string]any
	body := map[string]any{"version": *versionFlag, "changelog": theme.Changelog(*notes, source.Commit), "is_breaking": *breaking, "upload_id": uploadID}
	if err := api.Do(ctx, http.MethodPost, "/org/themes/"+*listing+"/versions", body, &published); err != nil {
		return err
	}
	versionID := firstNonEmpty(str(published["id"]), str(published["version_id"]))
	a.printf("Published %s %s (source commit %s).\n", name, *versionFlag, source.Commit)
	if *installStore != "" {
		if versionID == "" {
			return errors.New("published, but the response had no version ID to install")
		}
		if err := api.Do(ctx, http.MethodPost, "/org/themes/"+*listing+"/versions/"+versionID+"/install", map[string]any{"store_id": *installStore}, nil); err != nil {
			return fmt.Errorf("published, but installing into %s failed: %w", *installStore, err)
		}
		a.printf("Installed it into store %s as an unpublished theme.\n", *installStore)
	}
	return nil
}
