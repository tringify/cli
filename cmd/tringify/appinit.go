package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tringify/cli/internal/appconfig"
	"github.com/tringify/cli/internal/theme"
)

// appStarterArchive is the app starter source.
var appStarterArchive = "https://codeload.github.com/tringify/app-starter/tar.gz/refs/heads/main"

func (a *app) appInit(ctx context.Context, args []string) error {
	flags := newFlags("app init --app ID [DIR]", "Create a project for an app from the Tringify app starter (github.com/tringify/app-starter): a TypeScript Cloudflare Worker with an embedded admin page, signed webhook handling and Store API calls. Writes the app's configuration to "+appconfig.FileName+". Create the app in the Developer Portal first.")
	appFlag := flags.String("app", "", "app ID (see `tringify app list`)")
	positional, err := parse(flags, args)
	if err != nil {
		return err
	}
	if *appFlag == "" {
		return errors.New("--app is required. Create the app in the Developer Portal, then see `tringify app list`")
	}
	if len(positional) > 1 {
		return fmt.Errorf("unexpected argument %q", positional[1])
	}
	api, _, err := a.devAPI()
	if err != nil {
		return err
	}
	var details struct {
		Name     string `json:"name"`
		Slug     string `json:"slug"`
		ClientID string `json:"client_id"`
	}
	if err := api.Do(ctx, http.MethodGet, "/apps/"+url.PathEscape(*appFlag), nil, &details); err != nil {
		return appAccess(err)
	}
	dir := details.Slug
	if len(positional) == 1 {
		dir = positional[0]
	}
	dest, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(dest); err == nil {
		return fmt.Errorf("%s already exists; choose a new directory", dest)
	}
	config, err := a.fetchConfig(ctx, api, *appFlag)
	if err != nil {
		return err
	}

	a.println("Downloading the app starter…")
	source, cleanup, err := theme.DownloadArchive(ctx, &http.Client{Timeout: 2 * time.Minute}, appStarterArchive, "the app starter")
	if err != nil {
		return err
	}
	defer cleanup()
	if err := copyTree(source, dest); err != nil {
		os.RemoveAll(dest)
		return err
	}
	if err := writeStarterSettings(dest, *appFlag, details.ClientID, details.Slug, config); err != nil {
		os.RemoveAll(dest)
		return err
	}
	a.printf(`Created %s for %s.

Next:
  cd %s
  npm install
  npm run db:migrate:local
  npm run dev

Before running it, copy two secrets from the Developer Portal into .dev.vars:
  TRINGIFY_CLIENT_SECRET   Apps > %s > Credentials
  TRINGIFY_WEBHOOK_SECRET  Apps > %s > Webhooks > Signing Key
Each is shown when you create or regenerate it.

%s holds the app's configuration. Edit it, then run
`+"`tringify app config push`"+`. The README explains deploying the Worker.
`, dest, details.Name, dir, details.Name, details.Name, appconfig.FileName)
	return nil
}

// writeStarterSettings points the starter at this app: its configuration,
// the Worker's name and app ID, and a .dev.vars with a fresh encryption key.
func writeStarterSettings(dest, appID, clientID, slug string, config []byte) error {
	if err := os.WriteFile(filepath.Join(dest, appconfig.FileName), config, 0o644); err != nil {
		return err
	}
	wranglerPath := filepath.Join(dest, "wrangler.jsonc")
	raw, err := os.ReadFile(wranglerPath)
	if err != nil {
		return err
	}
	text := string(raw)
	for _, r := range []struct{ from, to string }{
		{`"name": "my-tringify-app"`, `"name": "` + slug + `"`},
		{`"database_name": "my-tringify-app"`, `"database_name": "` + slug + `"`},
		{`"TRINGIFY_APP_ID": ""`, `"TRINGIFY_APP_ID": "` + appID + `"`},
		{`"TRINGIFY_CLIENT_ID": ""`, `"TRINGIFY_CLIENT_ID": "` + clientID + `"`},
	} {
		if !strings.Contains(text, r.from) {
			return errors.New("the app starter's wrangler.jsonc has an unexpected layout; update the CLI")
		}
		text = strings.Replace(text, r.from, r.to, 1)
	}
	if err := os.WriteFile(wranglerPath, []byte(text), 0o644); err != nil {
		return err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return err
	}
	vars := "# Local secrets for `npm run dev`. Never commit this file.\n" +
		"# Client secret: Developer Portal > Apps > your app > Credentials.\n" +
		"TRINGIFY_CLIENT_SECRET=\n" +
		"# Webhook signing secret: Developer Portal > Apps > your app > Webhooks > Signing Key.\n" +
		"TRINGIFY_WEBHOOK_SECRET=\n" +
		"# Encrypts stored access tokens. Use a different key in production.\n" +
		"TOKEN_ENCRYPTION_KEY=" + base64.StdEncoding.EncodeToString(key) + "\n"
	return os.WriteFile(filepath.Join(dest, ".dev.vars"), []byte(vars), 0o600)
}

// copyTree copies a directory of regular files and directories.
func copyTree(src, dest string) error {
	return filepath.WalkDir(src, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dest, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, raw, 0o644)
	})
}
