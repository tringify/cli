package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tringify/cli/internal/auth"
	"github.com/tringify/cli/internal/devapi"
	"github.com/tringify/cli/internal/pyjson"
	"github.com/tringify/cli/internal/theme"
	"github.com/tringify/cli/internal/themekit"
	"github.com/tringify/cli/internal/themetools"
	"github.com/tringify/cli/internal/upload"
)

func (a *app) tools(checker, renderer string) themekit.Tools {
	if a.resolver == nil {
		a.resolver = themetools.New(a.stderr)
	}
	return a.resolver.Tools(checker, renderer)
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

// modeFlag adds the themecheck --mode option.
func modeFlag(fs *flag.FlagSet) *string {
	return fs.String("mode", "sealed", "themecheck mode: sealed (upload rules) or development")
}

func checkMode(mode string) error {
	if !themekit.ValidMode(mode) {
		return fmt.Errorf("--mode must be sealed or development, not %q", mode)
	}
	return nil
}

const (
	checkerHelp  = "path to themecheck (default: TRINGIFY_THEME_CHECK, PATH, or the downloaded theme tools)"
	rendererHelp = "path to theme-preview-render (default: TRINGIFY_THEME_PREVIEW, PATH, or the downloaded theme tools)"
)

func (a *app) themeInit(ctx context.Context, args []string) error {
	fs := newFlags("theme init [DIR] [--name NAME] [--from SOURCE]", "Create a new theme from the Tringify starter theme (github.com/tringify/theme-starter), or from a local theme with --from. The new theme is built and validated before DIR is created.")
	name := fs.String("name", "", "theme display name (default: derived from DIR)")
	from := fs.String("from", "", "local theme directory to start from (default: download the starter theme)")
	checker := fs.String("checker", "", checkerHelp)
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
	if _, err := os.Lstat(dest); err == nil {
		return errors.New("theme initialization failed: destination already exists; choose a new directory")
	}
	if *name == "" {
		words := strings.FieldsFunc(filepath.Base(dest), func(r rune) bool { return r == '-' || r == '_' || r == ' ' })
		for i, w := range words {
			words[i] = strings.ToUpper(w[:1]) + w[1:]
		}
		*name = strings.Join(words, " ")
	}
	if _, err := themekit.ValidName(*name); err != nil {
		return fmt.Errorf("theme initialization failed: %w", err)
	}
	source := *from
	if source == "" {
		a.println("Downloading the starter theme…")
		starter, cleanup, err := theme.DownloadStarter(ctx, &http.Client{Timeout: 2 * time.Minute})
		if err != nil {
			return err
		}
		defer cleanup()
		source = starter
	}
	if _, err := themekit.Initialize(ctx, source, dest, *name, a.tools(*checker, "")); err != nil {
		return fmt.Errorf("theme initialization failed: %w", err)
	}
	resolved := dest
	if r, err := filepath.EvalSymlinks(dest); err == nil {
		resolved = r
	}
	trimmed, _ := themekit.ValidName(*name)
	a.printf("Created %s: %s\n", trimmed, resolved)
	a.println("Built and validated for import. Edit the source, then run build, check, or package.")
	a.printf("\nNext:\n  cd %s\n  tringify theme preview\n", dir)
	return nil
}

func (a *app) themeBuild(_ context.Context, args []string) error {
	fs := newFlags("theme build [DIR]", "Compile src/sections/<name>/ (body.html, style.css, schema.json) into sections/<name>.vasc and update the section list in theme.json.")
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	root, err := rootArg(positional)
	if err != nil {
		return err
	}
	names, err := themekit.Build(root)
	if err != nil {
		return fmt.Errorf("theme build failed: %w", err)
	}
	a.printf("built %d sections: %s\n", len(names), strings.Join(names, ", "))
	return nil
}

func (a *app) themeCheck(ctx context.Context, args []string) error {
	fs := newFlags("theme check [DIR] [--mode sealed|development]", "Validate the compiled theme, as it is, with the same rules used when a theme is uploaded. Run build first after editing src/.")
	mode := modeFlag(fs)
	checker := fs.String("checker", "", checkerHelp)
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := checkMode(*mode); err != nil {
		return err
	}
	root, err := rootArg(positional)
	if err != nil {
		return err
	}
	sections, err := themekit.Validate(ctx, root, *mode, a.tools(*checker, ""))
	if err != nil {
		return fmt.Errorf("theme validation failed: %w", err)
	}
	a.printf("theme validation passed: %d source sections\n", len(sections))
	return nil
}

func (a *app) themeContract(ctx context.Context, args []string) error {
	fs := newFlags("theme contract", "Print the theme author contract as JSON: CTX roots and fields, editor setting types and hosted form actions.")
	checker := fs.String("checker", "", checkerHelp)
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(positional) > 0 {
		return fmt.Errorf("unexpected argument %q", positional[0])
	}
	contract, err := themekit.AuthorContract(ctx, a.tools(*checker, ""))
	if err != nil {
		return fmt.Errorf("theme contract failed: %w", err)
	}
	a.println(pyjson.DumpsUnicode(contract))
	return nil
}

func (a *app) themeContext(ctx context.Context, args []string) error {
	fs := newFlags("theme context [DIR] [--page PAGE] [--entity HANDLE] [--preset NAME]", "Print the exact sample data (CTX) the preview gives one page.")
	page := fs.String("page", "home", "template page type")
	entity := fs.String("entity", "", "product, collection, article, or page handle (default: the first in the demo pack)")
	preset := fs.String("preset", "", "theme style preset name")
	renderer := fs.String("renderer", "", rendererHelp)
	checker := fs.String("checker", "", checkerHelp)
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	root, err := rootArg(positional)
	if err != nil {
		return err
	}
	payload, err := themekit.InspectContext(ctx, root, *page, *entity, *preset, a.tools(*checker, *renderer))
	if err != nil {
		return fmt.Errorf("theme context failed: %w", err)
	}
	a.println(pyjson.DumpsUnicode(payload))
	return nil
}

func (a *app) themePreview(ctx context.Context, args []string) error {
	fs := newFlags("theme preview [DIR] [--port 9292] [--preset NAME] [--host ADDRESS]", "Serve a local preview with sample content. It rebuilds in a temporary copy whenever the theme's sources change. Press Ctrl+C to stop.")
	host := fs.String("host", "127.0.0.1", "specific IPv4 interface; pass this computer's LAN address to preview from another device")
	port := fs.Int("port", 9292, "port")
	preset := fs.String("preset", "", "theme style preset name")
	renderer := fs.String("renderer", "", rendererHelp)
	checker := fs.String("checker", "", checkerHelp)
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	root, err := rootArg(positional)
	if err != nil {
		return err
	}
	if err := themekit.Serve(ctx, root, *host, *port, *preset, a.tools(*checker, *renderer), a.stdout); err != nil {
		return fmt.Errorf("theme preview failed: %w", err)
	}
	return nil
}

func (a *app) themePackage(ctx context.Context, args []string) error {
	fs := newFlags("theme package [DIR] [OUTPUT] [--output FILE]", "Build the section sources in a temporary copy, validate the theme and write an upload-ready ZIP. A failed validation never replaces an existing package. Outputs inside the theme must be under dist/.")
	output := fs.String("output", "", "ZIP path (default: dist/<theme>.zip)")
	mode := modeFlag(fs)
	checker := fs.String("checker", "", checkerHelp)
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := checkMode(*mode); err != nil {
		return err
	}
	switch {
	case len(positional) > 2 || (len(positional) == 2 && *output != ""):
		return errors.New("theme packaging failed: expected [DIR] [OUTPUT], or [DIR] --output FILE")
	case len(positional) == 2:
		*output = positional[1]
		positional = positional[:1]
	case len(positional) == 1 && *output == "" && strings.EqualFold(filepath.Ext(positional[0]), ".zip"):
		// The theme tools form: package OUTPUT, from the current directory.
		*output = positional[0]
		positional = nil
	}
	root, err := rootArg(positional)
	if err != nil {
		return err
	}
	if *output == "" {
		name, err := theme.Name(root)
		if err != nil {
			return err
		}
		*output = filepath.Join(root, "dist", theme.Slug(name)+".zip")
	}
	out, err := filepath.Abs(*output)
	if err != nil {
		return err
	}
	return a.packageTo(ctx, root, out, *mode, *checker)
}

func (a *app) packageTo(ctx context.Context, root, out, mode, checker string) error {
	count, err := themekit.Package(ctx, root, out, mode, a.tools(checker, ""))
	if err != nil {
		return fmt.Errorf("theme packaging failed: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(out); err == nil {
		out = resolved
	}
	a.printf("packaged %d files: %s\n", count, out)
	return nil
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
	if err := a.packageTo(ctx, root, out, "sealed", ""); err != nil {
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

// listAt returns the list a response documents: the response itself when
// key is "", otherwise the field key of a response object.
func listAt(data any, key string) []map[string]any {
	list, _ := data.([]any)
	if key != "" {
		if m, ok := data.(map[string]any); ok {
			list, _ = m[key].([]any)
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
		return errors.New("--store is required. Find the ID with `tringify store list` or in the store's admin URL")
	}
	root, err := rootArg(positional)
	if err != nil {
		return err
	}
	target, err := a.storeTarget(ctx, *storeID, *storefrontID)
	if err != nil {
		return err
	}
	themeID, name, err := a.importTheme(ctx, target, root)
	if err != nil {
		return err
	}
	a.printf("Added %s to %s as an unpublished theme (theme %s).\n", name, target.session.Account.Target.Name, themeID)
	a.println("Preview and publish it from the store admin under Online Store → Themes.")
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
	// GET /org/themes without paging returns the listings as an array.
	list := listAt(data, "")
	if len(list) == 0 {
		a.printf("No theme listings in %s. Create one in the Developer Portal under Themes.\n", session.Account.Target.Name)
		return nil
	}
	for _, l := range list {
		a.printf("%s  %s  %s\n", str(l["id"]), str(l["name"]), str(l["status"]))
	}
	return nil
}

func (a *app) themePublish(ctx context.Context, args []string) error {
	fs := newFlags("theme publish --listing ID --version X.Y.Z [--yes] [DIR]", "Package the theme from a clean git checkout and publish it as a new version of an existing listing. It first shows the files that change compared with the newest published version and asks before publishing. The version records the source commit.")
	listing := fs.String("listing", "", "theme listing ID (see `tringify theme listings`)")
	versionFlag := fs.String("version", "", "version to publish, for example 1.2.0")
	notes := fs.String("notes", "", "release notes")
	breaking := fs.Bool("breaking", false, "mark the version as containing breaking changes")
	installStore := fs.String("install-store", "", "also install the published version into this organization development store")
	yes := fs.Bool("yes", false, "publish without showing the changes and asking first (needed when not in a terminal)")
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
	if !*yes {
		latest, ok, err := resolveVersion(ctx, api, *listing, "")
		if err != nil {
			return err
		}
		if ok {
			changes, err := a.compareWithVersion(ctx, api, *listing, latest, root)
			if err != nil {
				return err
			}
			a.printChanges(changes, latest.Version)
		} else {
			a.println("This will be the listing's first published version.")
		}
		approved, err := confirm(a.stdin, a.stdout, fmt.Sprintf("Publish version %s?", *versionFlag))
		if err != nil {
			return err
		}
		if !approved {
			return errors.New("not published")
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
	versionID := str(published["version_id"])
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
