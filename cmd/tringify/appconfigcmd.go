package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"

	"github.com/tringify/cli/internal/appconfig"
	"github.com/tringify/cli/internal/devapi"
)

func (a *app) fetchConfig(ctx context.Context, api *devapi.Client, appID string) ([]byte, error) {
	var raw json.RawMessage
	if err := api.Do(ctx, http.MethodGet, "/apps/"+url.PathEscape(appID)+"/config", nil, &raw); err != nil {
		return nil, appAccess(err)
	}
	return appconfig.Format(raw)
}

func (a *app) appConfigPull(ctx context.Context, args []string) error {
	fs := newFlags("app config pull [--app ID] [--file PATH]", "Write the app's draft configuration to "+appconfig.FileName+", with each app block's source in blocks/<key>.vasc beside it. If the files exist and differ, shows what would change and asks first.")
	appFlag := fs.String("app", "", "app ID (default: app_id in the file)")
	file := fs.String("file", appconfig.FileName, "configuration file")
	yes := fs.Bool("yes", false, "overwrite without asking")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	appID, err := appTarget(*appFlag, *file)
	if err != nil {
		return err
	}
	api, _, err := a.devAPI()
	if err != nil {
		return err
	}
	remote, err := a.fetchConfig(ctx, api, appID)
	if err != nil {
		return err
	}
	if fileExists(*file) {
		local, _, _, readErr := appconfig.Load(*file)
		if readErr != nil {
			a.printf("Cannot compare with the local files: %v\n", readErr)
		} else {
			next, _ := appconfig.Parse(remote)
			changes := appconfig.Diff(local, next)
			if len(changes) == 0 {
				a.printf("%s is up to date.\n", *file)
				return nil
			}
			a.printf("Pulling would change %s:\n", *file)
			a.printConfigChanges(changes)
		}
		if !*yes {
			ok, err := confirmWith(a.stdin, a.stdout, "Overwrite "+*file+" and its block sources?", "Pass --yes to overwrite without confirming")
			if err != nil || !ok {
				if err == nil {
					a.println("Nothing written.")
				}
				return err
			}
		}
	}
	if err := a.writeConfig(*file, remote); err != nil {
		return err
	}
	a.printf("Wrote %s.\n", *file)
	return nil
}

func fileExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// writeConfig writes a configuration as the API serves it: file, and each
// block's source in its own file. It says which block files changed and
// which in blocks/ no block uses any more.
func (a *app) writeConfig(file string, config []byte) error {
	written, unused, err := appconfig.Write(file, config)
	if err != nil {
		return err
	}
	for _, name := range written {
		a.printf("Wrote %s.\n", name)
	}
	for _, name := range unused {
		a.printf("%s is not a source of any block in %s; delete it if you no longer need it.\n", name, file)
	}
	return nil
}

func (a *app) appConfigPush(ctx context.Context, args []string) error {
	fs := newFlags("app config push [--file PATH]", "Replace the app's draft configuration with "+appconfig.FileName+" and the block sources it names. Runs `tringify app config check` first and pushes nothing if it finds a problem. Shows what changes and asks first. Fields left out of the file are cleared. The draft goes live only when you release it.")
	file := fs.String("file", appconfig.FileName, "configuration file")
	yes := fs.Bool("yes", false, "replace without asking")
	renderer := fs.String("renderer", "", rendererHelp)
	if _, err := parse(fs, args); err != nil {
		return err
	}
	local, raw, _, err := appconfig.Load(*file)
	if err != nil {
		return err
	}
	appID := local.AppID()
	if appID == "" {
		return fmt.Errorf("%s has no app_id. Run `tringify app config pull --app ID` to start from the app's current configuration", *file)
	}
	api, _, err := a.devAPI()
	if err != nil {
		return err
	}
	remote, err := a.fetchConfig(ctx, api, appID)
	if err != nil {
		return err
	}
	current, _ := appconfig.Parse(remote)
	changes := appconfig.Diff(current, local)
	if len(changes) == 0 {
		a.println("No changes to push.")
		return nil
	}
	if err := a.checkBlocks(ctx, *file, raw, *renderer); err != nil {
		return fmt.Errorf("%w; nothing pushed", err)
	}
	if err := checkConfig(ctx, api, appID, raw); err != nil {
		return fmt.Errorf("%w; nothing pushed", err)
	}
	a.println("Pushing changes the draft:")
	a.printConfigChanges(changes)
	if !*yes {
		ok, err := confirmWith(a.stdin, a.stdout, "Replace the draft configuration?", "Pass --yes to push without confirming")
		if err != nil || !ok {
			if err == nil {
				a.println("Nothing pushed.")
			}
			return err
		}
	}
	var saved json.RawMessage
	if err := api.Do(ctx, http.MethodPut, "/apps/"+url.PathEscape(appID)+"/config", json.RawMessage(raw), &saved); err != nil {
		return appAccess(err)
	}
	a.println("Saved the draft. Release it with `tringify app release`.")
	// The saved draft is normalized (sorted scopes, the admin URL's origin
	// added to App Bridge origins). Keep the file identical to it.
	formatted, err := appconfig.Format(saved)
	if err != nil {
		return err
	}
	savedDoc, _ := appconfig.Parse(formatted)
	if len(appconfig.Diff(local, savedDoc)) > 0 {
		if err := a.writeConfig(*file, formatted); err != nil {
			return err
		}
		a.printf("Updated %s with the values as saved.\n", *file)
	}
	return nil
}

// checkConfig has the Developer API check the configuration exactly as a
// push would, with the same errors, and save nothing. The platform's rules
// (event delivery and its attribution declaration, scopes, origins) live
// there, so the CLI never keeps its own copy of them.
func checkConfig(ctx context.Context, api *devapi.Client, appID string, raw []byte) error {
	if err := api.Do(ctx, http.MethodPost, "/apps/"+url.PathEscape(appID)+"/config/check", json.RawMessage(raw), nil); err != nil {
		return appAccess(err)
	}
	return nil
}

func (a *app) appConfigCheck(ctx context.Context, args []string) error {
	fs := newFlags("app config check [--file PATH]", "Check "+appconfig.FileName+" and the block sources it names without changing the draft: the app blocks first (see `tringify app block check`), then the whole configuration, which the Developer API checks exactly as it would on push, for example event delivery and the click IDs and cookies it declares under server_events.attribution. A problem is printed with the field it concerns, such as server_events.attribution.cookies. `app config push` runs this check first.")
	file := fs.String("file", appconfig.FileName, "configuration file")
	renderer := fs.String("renderer", "", rendererHelp)
	if _, err := parse(fs, args); err != nil {
		return err
	}
	local, raw, _, err := appconfig.Load(*file)
	if err != nil {
		return err
	}
	appID := local.AppID()
	if appID == "" {
		return fmt.Errorf("%s has no app_id. Run `tringify app config pull --app ID` to start from the app's current configuration", *file)
	}
	if err := a.checkBlocks(ctx, *file, raw, *renderer); err != nil {
		return err
	}
	api, _, err := a.devAPI()
	if err != nil {
		return err
	}
	if err := checkConfig(ctx, api, appID, raw); err != nil {
		return err
	}
	a.printf("%s passes the app platform's checks. Nothing was pushed.\n", *file)
	return nil
}

func (a *app) printConfigChanges(changes []appconfig.Change) {
	for _, c := range changes {
		switch c.Kind {
		case "added":
			a.printf("  + %s: %s\n", c.Field, c.To)
		case "removed":
			a.printf("  - %s: %s\n", c.Field, c.From)
		default:
			a.printf("  ~ %s: %s -> %s\n", c.Field, c.From, c.To)
		}
	}
}
