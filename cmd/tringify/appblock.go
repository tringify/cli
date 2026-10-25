package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"time"

	"github.com/tringify/cli/internal/appconfig"
	"github.com/tringify/cli/internal/pyjson"
	"github.com/tringify/cli/internal/theme"
	"github.com/tringify/cli/internal/themekit"
)

func (a *app) appBlock(ctx context.Context, args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "check":
			return a.appBlockCheck(ctx, args[1:])
		case "context":
			return a.appBlockContext(ctx, args[1:])
		case "preview":
			return a.appBlockPreview(ctx, args[1:])
		}
	}
	return errors.New("usage: tringify app block check|context|preview")
}

// checkBlocks runs the app platform's block rules on config (block sources
// inline) and prints each problem against the file it is in: the block's
// source file from file, or the configuration itself.
func (a *app) checkBlocks(ctx context.Context, file string, config []byte, renderer string) error {
	var declared struct {
		Blocks []json.RawMessage `json:"storefront_blocks"`
	}
	if json.Unmarshal(config, &declared) == nil && len(declared.Blocks) == 0 {
		return nil
	}
	count, problems, err := themekit.CheckAppBlocks(ctx, config, a.tools("", renderer))
	if err != nil {
		return fmt.Errorf("app block check failed: %w", err)
	}
	if len(problems) == 0 {
		if count > 0 {
			a.printf("App blocks: %d checked, no problems.\n", count)
		}
		return nil
	}
	sources := appconfig.SourceFiles(file)
	dir := filepath.Dir(file)
	for _, p := range problems {
		where := file
		if p.Part == "source" {
			if name, ok := sources[p.Block]; ok {
				where = filepath.Join(dir, filepath.FromSlash(name))
			} else {
				where = fmt.Sprintf("block %s", p.Block)
			}
			if p.Line > 0 {
				where = fmt.Sprintf("%s:%d", where, p.Line)
			}
		}
		fmt.Fprintf(a.stderr, "%s: %s\n", where, p.Message)
	}
	if len(problems) == 1 {
		return errors.New("app block check found 1 problem")
	}
	return fmt.Errorf("app block check found %d problems", len(problems))
}

func (a *app) appBlockCheck(ctx context.Context, args []string) error {
	fs := newFlags("app block check [--file PATH]", "Check the app's blocks with the rules the app platform applies when the configuration is pushed: each declaration (mode, pages, surface), and each source file compiled as a block that reads only the data its mode allows and writes only the markup app blocks may use. Problems are printed as FILE:LINE: message. `app config push` and `app release` run this check first.")
	file := fs.String("file", appconfig.FileName, "configuration file")
	renderer := fs.String("renderer", "", rendererHelp)
	if _, err := parse(fs, args); err != nil {
		return err
	}
	_, config, _, err := appconfig.Load(*file)
	if err != nil {
		return err
	}
	if len(appconfig.SourceFiles(*file)) == 0 {
		a.printf("%s declares no app blocks.\n", *file)
		return nil
	}
	return a.checkBlocks(ctx, *file, config, *renderer)
}

func (a *app) appBlockContext(ctx context.Context, args []string) error {
	fs := newFlags("app block context [--page product] [--theme DIR] [--entity HANDLE]", "Print the sample data each of the app's blocks receives on a page: the page's sample data cut to the ctx_needs roots the block declares. A template block may read shop, routes, product and collection; a hosted_surface block may also read cart. Uses the built-in sample catalog, or a theme's demo content with --theme.")
	file := fs.String("file", appconfig.FileName, "configuration file")
	page := fs.String("page", "product", "page type: product, collection, cart, page, account, blog or home")
	themeDir := fs.String("theme", "", "theme directory whose demo content to use (default: the built-in sample catalog)")
	entity := fs.String("entity", "", "product, collection, article, or page handle (default: the first in the sample data)")
	preset := fs.String("preset", "", "theme style preset name, with --theme")
	renderer := fs.String("renderer", "", rendererHelp)
	checker := fs.String("checker", "", checkerHelp)
	if _, err := parse(fs, args); err != nil {
		return err
	}
	if *preset != "" && *themeDir == "" {
		return errors.New("--preset needs --theme")
	}
	_, config, _, err := appconfig.Load(*file)
	if err != nil {
		return err
	}
	root := ""
	if *themeDir != "" {
		if root, err = filepath.Abs(*themeDir); err != nil {
			return err
		}
	}
	payload, err := themekit.AppBlockContext(ctx, config, root, *page, *entity, *preset, a.tools(*checker, *renderer))
	if err != nil {
		return fmt.Errorf("app block context failed: %w", err)
	}
	a.println(pyjson.DumpsUnicode(payload))
	return nil
}

func (a *app) appBlockPreview(ctx context.Context, args []string) error {
	fs := newFlags("app block preview [--theme DIR] [--page product] [--after TYPE] [--port 9292]", "Serve a local preview of the app's blocks inside a theme, rendered as a store renders them: each block that lists --page goes into that page's app block slot, reads only the data its mode allows, and must pass the app markup policy and the page's app block budget; template blocks get the tf-ab-* styles. Uses the starter theme (the one `tringify theme init` starts from, downloaded from github.com/tringify/theme-starter) unless --theme names another theme. The preview rebuilds when tringify.app.json, a block source or the theme changes. Press Ctrl+C to stop.")
	file := fs.String("file", appconfig.FileName, "configuration file")
	themeDir := fs.String("theme", "", "theme directory (default: the starter theme)")
	page := fs.String("page", "product", "page type whose app block slot shows the blocks")
	after := fs.String("after", "price", "theme block type the app blocks follow in the slot; last when the slot has none")
	host := fs.String("host", "127.0.0.1", "specific IPv4 interface; pass this computer's LAN address to preview from another device")
	port := fs.Int("port", 9292, "port")
	preset := fs.String("preset", "", "theme style preset name")
	renderer := fs.String("renderer", "", rendererHelp)
	checker := fs.String("checker", "", checkerHelp)
	if _, err := parse(fs, args); err != nil {
		return err
	}
	// Fail at once on a configuration that cannot be read; later problems
	// show in the preview while it runs.
	if _, _, _, err := appconfig.Load(*file); err != nil {
		return err
	}
	root := ""
	if *themeDir != "" {
		var err error
		if root, err = filepath.Abs(*themeDir); err != nil {
			return err
		}
	} else {
		a.println("Downloading the starter theme…")
		starter, cleanup, err := theme.DownloadStarter(ctx, &http.Client{Timeout: 2 * time.Minute})
		if err != nil {
			return err
		}
		defer cleanup()
		root = starter
		a.println("Previewing in the starter theme. Pass --theme DIR to use another theme.")
	}
	apps := themekit.AppBlocks{
		Load: func() ([]byte, error) {
			_, config, _, err := appconfig.Load(*file)
			return config, err
		},
		Page:  *page,
		After: *after,
	}
	if err := themekit.ServeAppBlocks(ctx, root, apps, *host, *port, *preset, a.tools(*checker, *renderer), a.stdout); err != nil {
		return fmt.Errorf("app block preview failed: %w", err)
	}
	return nil
}
