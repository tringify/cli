package main

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/base64"
	"encoding/json"
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
	"github.com/tringify/cli/internal/themeops"
)

// appStarterArchive is the app starter source.
var appStarterArchive = "https://codeload.github.com/tringify/app-starter/tar.gz/refs/heads/main"

// starterBlock is the example template block a new project starts with.
//
//go:embed starter/instalments.vasc
var starterBlock string

const starterBlockKey = "instalments"

func (a *app) appInit(ctx context.Context, args []string) error {
	flags := newFlags("app init --app ID [DIR]", "Create a project for an app from the Tringify app starter (github.com/tringify/app-starter): a TypeScript Cloudflare Worker with an embedded admin page, signed webhook handling and Store API calls. Writes the app's configuration to "+appconfig.FileName+", with app block sources in blocks/, and adds an example template block, blocks/instalments.vasc, that shows the product price split into payments. Create the app first with `tringify app create` (or in the Developer Portal).")
	appFlag := flags.String("app", "", "app ID (see `tringify app list`)")
	positional, err := parse(flags, args)
	if err != nil {
		return err
	}
	if *appFlag == "" {
		return errors.New("--app is required. Create the app with `tringify app create`; `tringify app list` lists your apps")
	}
	if len(positional) > 1 {
		return fmt.Errorf("unexpected argument %q", positional[1])
	}
	api, _, err := a.devAPI()
	if err != nil {
		return err
	}
	var details struct {
		Name         string `json:"name"`
		Slug         string `json:"slug"`
		ClientID     string `json:"client_id"`
		Distribution string `json:"distribution"`
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
	source, cleanup, err := themeops.DownloadArchive(ctx, &http.Client{Timeout: 2 * time.Minute}, appStarterArchive, "the app starter")
	if err != nil {
		return err
	}
	defer cleanup()
	if err := copyTree(source, dest); err != nil {
		os.RemoveAll(dest)
		return err
	}
	added, err := writeStarterSettings(dest, *appFlag, details.ClientID, details.Slug, config)
	if err != nil {
		os.RemoveAll(dest)
		return err
	}
	if err := writeReleaseWorkflow(dest, details.Distribution); err != nil {
		os.RemoveAll(dest)
		return err
	}
	a.printf(`Created %s for %s.

Next:
  cd %s
  npm install
  npm run db:migrate:local
  npm run dev

Before running it, put two secrets in .dev.vars:
  TRINGIFY_CLIENT_SECRET   printed once by `+"`tringify app create`"+`; for a new one run `+"`tringify app secret rotate`"+`
  TRINGIFY_WEBHOOK_SECRET  run `+"`tringify app webhook rotate-key`"+` (or Developer Portal: Apps > %s > Webhooks > Signing Key)
Both are shown only once; keep them out of version control.

%s holds the app's configuration and blocks/ its app block sources.
Edit them, then run `+"`tringify app config push`"+`. The README explains deploying the Worker.

.github/workflows/release.yml releases the app from GitHub Actions when you push
a tag such as v1.0.0, with trusted publishing (no stored secret). Before the
first release, add a trusted publisher on the app in the Developer Portal
(Apps > %s > Trusted publishing): your repository, workflow release.yml,
environment release.
`, dest, details.Name, dir, details.Name, appconfig.FileName, details.Name)
	if added {
		a.printf(`
blocks/%s.vasc is an example template block: "or 3 interest-free payments
of …" under the product price, with the number of payments as a setting
merchants change in the theme editor. See it in a theme with
`+"`tringify app block preview`"+`. It is not in the app's draft until you push; remove
its entry from %s and the file if you do not want it.
`, starterBlockKey, appconfig.FileName)
	} else {
		a.printf("\nThe app already has a block named %s or the most blocks an app may have, so the example template block was not added.\n", starterBlockKey)
	}
	return nil
}

// appReleaseWorkflowHeader starts every app project's release workflow.
const appReleaseWorkflowHeader = `# Releases the app when you push a tag such as v1.2.0:
#   git tag -a v1.2.0 -m "What changed"   # the tag message becomes the release notes
#   git push origin v1.2.0
# Uses trusted publishing: no secret is stored. Before the first release, add a
# trusted publisher on the app in the Developer Portal (Apps > your app >
# Trusted publishing) with this repository, workflow file release.yml and
# environment "release".
name: Release
on:
  push:
    tags: ["v*"]
permissions:
  contents: read
jobs:
  release:
    runs-on: ubuntu-latest
    environment: release
    permissions:
      contents: read
      id-token: write
    steps:
      - uses: actions/checkout@v4
        with: {fetch-depth: 0}
      - run: curl -fsSL https://raw.githubusercontent.com/tringify/cli/main/install.sh | sh && echo "$HOME/.local/bin" >> "$GITHUB_PATH"
      - name: Release notes from the tag message
        run: |
          git fetch --force origin "refs/tags/$GITHUB_REF_NAME:refs/tags/$GITHUB_REF_NAME"
          git tag -l --format='%(contents)' "$GITHUB_REF_NAME" > "$RUNNER_TEMP/notes.md"
      - run: tringify app config push --yes
`

// appReleaseWorkflowMarketplace submits the tag's version for review and,
// in a second job, publishes it. Review takes time: re-run the publish job
// once Tringify has approved the version.
const appReleaseWorkflowMarketplace = `      - run: tringify app release --version "$GITHUB_REF_NAME" --notes-file "$RUNNER_TEMP/notes.md" --yes
      - if: hashFiles('docs/*.md') != ''
        run: tringify app docs push --submit
  # Fails while the version is in review: once Tringify approves it, re-run
  # this job from the workflow run's page (it gets a fresh ID token).
  publish:
    needs: release
    runs-on: ubuntu-latest
    environment: release
    permissions:
      contents: read
      id-token: write
    steps:
      - uses: actions/checkout@v4
      - run: curl -fsSL https://raw.githubusercontent.com/tringify/cli/main/install.sh | sh && echo "$HOME/.local/bin" >> "$GITHUB_PATH"
      - run: tringify app publish "$GITHUB_REF_NAME"
`

// appReleaseWorkflowPrivate releases the draft to every store that has the
// app; Tringify numbers private updates, so the tag only starts it.
const appReleaseWorkflowPrivate = `      - run: tringify app release --notes-file "$RUNNER_TEMP/notes.md" --yes
      - if: hashFiles('docs/*.md') != ''
        run: tringify app docs push --submit
`

// writeReleaseWorkflow adds .github/workflows/release.yml to a new app
// project, for the app's distribution.
func writeReleaseWorkflow(dest, distribution string) error {
	workflow := appReleaseWorkflowHeader + appReleaseWorkflowMarketplace
	if distribution == "private" {
		workflow = appReleaseWorkflowHeader + appReleaseWorkflowPrivate
	}
	dir := filepath.Join(dest, ".github", "workflows")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "release.yml"), []byte(workflow), 0o644)
}

// withStarterBlock adds the example template block to a configuration as
// the API serves it, unless the app has a block with its key or no room.
func withStarterBlock(config []byte) ([]byte, bool, error) {
	var doc struct {
		Blocks []struct {
			Key string `json:"key"`
		} `json:"storefront_blocks"`
	}
	if err := json.Unmarshal(config, &doc); err != nil {
		return nil, false, err
	}
	if len(doc.Blocks) >= 10 {
		return config, false, nil
	}
	for _, b := range doc.Blocks {
		if b.Key == starterBlockKey {
			return config, false, nil
		}
	}
	block := struct {
		Key         string   `json:"key"`
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Mode        string   `json:"mode"`
		Pages       []string `json:"pages"`
		Source      string   `json:"source"`
	}{starterBlockKey, "Instalments", "Shows the product price split into equal interest-free payments.", "template", []string{"product"}, starterBlock}
	out, err := appconfig.AppendBlock(config, block)
	return out, err == nil, err
}

// writeStarterSettings points the starter at this app: its configuration
// with the example block, the Worker's name and app ID, and a .dev.vars
// with a fresh encryption key. It reports whether the example block was
// added.
func writeStarterSettings(dest, appID, clientID, slug string, config []byte) (bool, error) {
	config, added, err := withStarterBlock(config)
	if err != nil {
		return false, err
	}
	if _, _, err := appconfig.Write(filepath.Join(dest, appconfig.FileName), config); err != nil {
		return false, err
	}
	wranglerPath := filepath.Join(dest, "wrangler.jsonc")
	raw, err := os.ReadFile(wranglerPath)
	if err != nil {
		return false, err
	}
	text := string(raw)
	for _, r := range []struct{ from, to string }{
		{`"name": "my-tringify-app"`, `"name": "` + slug + `"`},
		{`"database_name": "my-tringify-app"`, `"database_name": "` + slug + `"`},
		{`"TRINGIFY_APP_ID": ""`, `"TRINGIFY_APP_ID": "` + appID + `"`},
		{`"TRINGIFY_CLIENT_ID": ""`, `"TRINGIFY_CLIENT_ID": "` + clientID + `"`},
	} {
		if !strings.Contains(text, r.from) {
			return false, errors.New("the app starter's wrangler.jsonc has an unexpected layout; update the CLI")
		}
		text = strings.Replace(text, r.from, r.to, 1)
	}
	if err := os.WriteFile(wranglerPath, []byte(text), 0o644); err != nil {
		return false, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return false, err
	}
	vars := "# Local secrets for `npm run dev`. Never commit this file.\n" +
		"# Client secret: printed once by `tringify app create`; for a new one run `tringify app secret rotate`.\n" +
		"TRINGIFY_CLIENT_SECRET=\n" +
		"# Webhook signing secret: Developer Portal > Apps > your app > Webhooks > Signing Key.\n" +
		"TRINGIFY_WEBHOOK_SECRET=\n" +
		"# Encrypts stored access tokens. Use a different key in production.\n" +
		"TOKEN_ENCRYPTION_KEY=" + base64.StdEncoding.EncodeToString(key) + "\n"
	return added, os.WriteFile(filepath.Join(dest, ".dev.vars"), []byte(vars), 0o600)
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
