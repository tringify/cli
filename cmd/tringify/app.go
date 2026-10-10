package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/tringify/cli/internal/appconfig"
	"github.com/tringify/cli/internal/devapi"
)

func (a *app) appCommand(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprint(a.stdout, usage)
		return nil
	}
	switch args[0] {
	case "list":
		return a.appList(ctx, args[1:])
	case "create":
		return a.appCreate(ctx, args[1:])
	case "init":
		return a.appInit(ctx, args[1:])
	case "config":
		if len(args) > 1 {
			switch args[1] {
			case "pull":
				return a.appConfigPull(ctx, args[2:])
			case "push":
				return a.appConfigPush(ctx, args[2:])
			}
		}
		return errors.New("usage: tringify app config pull|push")
	case "release":
		return a.appRelease(ctx, args[1:])
	case "install-link":
		return a.appInstallLink(ctx, args[1:])
	case "versions":
		return a.appVersions(ctx, args[1:])
	case "publish":
		return a.appPublish(ctx, args[1:])
	case "webhook":
		if len(args) > 1 {
			switch args[1] {
			case "test":
				return a.appWebhookTest(ctx, args[2:])
			case "rotate-key":
				return a.appWebhookRotateKey(ctx, args[2:])
			}
		}
		return errors.New("usage: tringify app webhook test|rotate-key")
	case "secret":
		if len(args) > 1 && args[1] == "rotate" {
			return a.appSecretRotate(ctx, args[2:])
		}
		return errors.New("usage: tringify app secret rotate")
	case "deliveries":
		return a.appDeliveries(ctx, args[1:])
	}
	return fmt.Errorf("unknown app command %q", args[0])
}

// appAccess explains a refusal from a login made before app access was part
// of the sign-in.
func appAccess(err error) error {
	var apiErr *devapi.Error
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusForbidden && apiErr.Code == "FORBIDDEN" {
		return fmt.Errorf("%w. If you signed in before app commands existed, run `tringify login` again", err)
	}
	return err
}

// appTarget is the app a command acts on: --app, or the app_id in the
// configuration file.
func appTarget(appFlag, file string) (string, error) {
	if appFlag != "" {
		return appFlag, nil
	}
	doc, _, err := appconfig.Read(file)
	if errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("no %s here. Pass --app ID (see `tringify app list`)", file)
	}
	if err != nil {
		return "", err
	}
	if id := doc.AppID(); id != "" {
		return id, nil
	}
	return "", fmt.Errorf("%s has no app_id. Pass --app ID", file)
}

func (a *app) appList(ctx context.Context, args []string) error {
	fs := newFlags("app list", "List your organization's apps.")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	api, session, err := a.devAPI()
	if err != nil {
		return err
	}
	// The API returns at most 50 apps a page.
	var data struct {
		Apps []map[string]any `json:"apps"`
	}
	for page := 1; ; page++ {
		var batch struct {
			Apps       []map[string]any `json:"apps"`
			TotalCount int              `json:"total_count"`
		}
		if err := api.Do(ctx, http.MethodGet, "/apps?limit=50&page="+strconv.Itoa(page), nil, &batch); err != nil {
			return appAccess(err)
		}
		data.Apps = append(data.Apps, batch.Apps...)
		if len(batch.Apps) == 0 || len(data.Apps) >= batch.TotalCount {
			break
		}
	}
	if len(data.Apps) == 0 {
		a.printf("No apps in %s. Create one with `tringify app create`.\n", session.Account.Target.Name)
		return nil
	}
	for _, item := range data.Apps {
		a.printf("%s  %s  %s  %s  %s\n", str(item["id"]), str(item["name"]), str(item["app_type"]), str(item["distribution"]), str(item["submission_status"]))
	}
	return nil
}

func (a *app) fetchConfig(ctx context.Context, api *devapi.Client, appID string) ([]byte, error) {
	var raw json.RawMessage
	if err := api.Do(ctx, http.MethodGet, "/apps/"+url.PathEscape(appID)+"/config", nil, &raw); err != nil {
		return nil, appAccess(err)
	}
	return appconfig.Format(raw)
}

func (a *app) appConfigPull(ctx context.Context, args []string) error {
	fs := newFlags("app config pull [--app ID] [--file PATH]", "Write the app's draft configuration to "+appconfig.FileName+". If the file exists and differs, shows what would change and asks first.")
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
	if local, _, readErr := appconfig.Read(*file); readErr == nil {
		next, _ := appconfig.Parse(remote)
		changes := appconfig.Diff(local, next)
		if len(changes) == 0 {
			a.printf("%s is up to date.\n", *file)
			return nil
		}
		a.printf("Pulling would change %s:\n", *file)
		a.printConfigChanges(changes)
		if !*yes {
			ok, err := confirmWith(a.stdin, a.stdout, "Overwrite "+*file+"?", "Pass --yes to overwrite without confirming")
			if err != nil || !ok {
				if err == nil {
					a.println("Nothing written.")
				}
				return err
			}
		}
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return readErr
	}
	if err := os.WriteFile(*file, remote, 0o644); err != nil {
		return err
	}
	a.printf("Wrote %s.\n", *file)
	return nil
}

func (a *app) appConfigPush(ctx context.Context, args []string) error {
	fs := newFlags("app config push [--file PATH]", "Replace the app's draft configuration with "+appconfig.FileName+". Shows what changes and asks first. Fields left out of the file are cleared. The draft goes live only when you release it.")
	file := fs.String("file", appconfig.FileName, "configuration file")
	yes := fs.Bool("yes", false, "replace without asking")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	local, raw, err := appconfig.Read(*file)
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
		if err := os.WriteFile(*file, formatted, 0o644); err != nil {
			return err
		}
		a.printf("Updated %s with the values as saved.\n", *file)
	}
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

type appVersion struct {
	ID          string    `json:"id"`
	Version     string    `json:"version"`
	Status      string    `json:"status"`
	SubmittedAt time.Time `json:"submitted_at"`
	Notes       string    `json:"release_notes"`
}

func (a *app) listAppVersions(ctx context.Context, api *devapi.Client, appID string, limit int) ([]appVersion, error) {
	var data struct {
		Versions []appVersion `json:"versions"`
	}
	if err := api.Do(ctx, http.MethodGet, "/apps/"+url.PathEscape(appID)+"/versions?limit="+strconv.Itoa(limit), nil, &data); err != nil {
		return nil, appAccess(err)
	}
	return data.Versions, nil
}

// stringList is a flag that can be given more than once.
type stringList []string

func (l *stringList) String() string     { return strings.Join(*l, ",") }
func (l *stringList) Set(v string) error { *l = append(*l, v); return nil }

// appDistribution is "private" or "marketplace".
func (a *app) appDistribution(ctx context.Context, api *devapi.Client, appID string) (string, error) {
	var details struct {
		Distribution string `json:"distribution"`
	}
	if err := api.Do(ctx, http.MethodGet, "/apps/"+url.PathEscape(appID), nil, &details); err != nil {
		return "", appAccess(err)
	}
	return details.Distribution, nil
}

func (a *app) appRelease(ctx context.Context, args []string) error {
	fs := newFlags("app release [--bump patch|minor|major] [--store ID]... [--notes TEXT]", "Release the app's draft. A marketplace app submits it as a new version: the previous version number with the part you name in --bump increased, where major marks the version as breaking. A private app sends it as an update to the installed stores you name with --store, or to all of them with --all; each store accepts it in its admin.")
	appFlag := fs.String("app", "", "app ID (default: app_id in "+appconfig.FileName+")")
	file := fs.String("file", appconfig.FileName, "configuration file")
	bump := fs.String("bump", "", "marketplace apps: patch, minor or major")
	notes := fs.String("notes", "", "release notes shown to stores")
	var stores stringList
	fs.Var(&stores, "store", "private apps: an installed store to update (repeatable)")
	all := fs.Bool("all", false, "private apps: update every installed store")
	yes := fs.Bool("yes", false, "private apps: send without asking")
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
	// Releasing with unpushed edits would ship something other than the file.
	if local, _, readErr := appconfig.Read(*file); readErr == nil && local.AppID() == appID {
		remote, err := a.fetchConfig(ctx, api, appID)
		if err != nil {
			return err
		}
		current, _ := appconfig.Parse(remote)
		if len(appconfig.Diff(current, local)) > 0 {
			return fmt.Errorf("%s differs from the draft. Run `tringify app config push` first", *file)
		}
	}
	distribution, err := a.appDistribution(ctx, api, appID)
	if err != nil {
		return err
	}
	if distribution == "private" {
		if *bump != "" {
			return errors.New("--bump is for marketplace apps; private updates are numbered for you")
		}
		return a.sendPrivateUpdate(ctx, api, appID, stores, *all, *notes, *yes)
	}
	if len(stores) > 0 || *all {
		return errors.New("--store and --all are for private apps; a marketplace version reaches stores once it is published")
	}
	if *bump != "patch" && *bump != "minor" && *bump != "major" {
		return errors.New("--bump must be patch, minor or major")
	}
	body := map[string]string{"bump": *bump, "release_notes": *notes}
	if err := api.Do(ctx, http.MethodPost, "/apps/"+url.PathEscape(appID)+"/submit", body, nil); err != nil {
		return appAccess(err)
	}
	versions, err := a.listAppVersions(ctx, api, appID, 1)
	if err != nil || len(versions) == 0 {
		a.println("Submitted the draft.")
		return err
	}
	v := versions[0]
	switch v.Status {
	case "approved":
		a.printf("Version %s is approved. Publish it with `tringify app publish %s`.\n", v.Version, v.Version)
	default:
		a.printf("Submitted version %s for review (%s). After approval, publish it with `tringify app publish %s`.\n", v.Version, v.Status, v.Version)
	}
	return nil
}

type privateUpdatePreview struct {
	SnapshotSHA256 string `json:"snapshot_sha256"`
	Installations  []struct {
		StoreID          string `json:"store_id"`
		StoreName        string `json:"store_name"`
		InstalledVersion string `json:"installed_version"`
		SnapshotSHA256   string `json:"snapshot_sha256"`
		PendingVersion   string `json:"pending_version"`
		PendingSHA256    string `json:"pending_sha256"`
	} `json:"installations"`
	Versions []struct {
		Version  string `json:"version"`
		Notes    string `json:"release_notes"`
		Pending  int    `json:"pending"`
		Accepted int    `json:"accepted"`
	} `json:"versions"`
}

func (a *app) privateUpdates(ctx context.Context, api *devapi.Client, appID string) (privateUpdatePreview, error) {
	var preview privateUpdatePreview
	err := api.Do(ctx, http.MethodGet, "/apps/"+url.PathEscape(appID)+"/private-updates", nil, &preview)
	return preview, appAccess(err)
}

func (a *app) sendPrivateUpdate(ctx context.Context, api *devapi.Client, appID string, stores []string, all bool, notes string, yes bool) error {
	preview, err := a.privateUpdates(ctx, api, appID)
	if err != nil {
		return err
	}
	if len(preview.Installations) == 0 {
		return errors.New("no store has installed the app yet. Create an install link with `tringify app install-link`")
	}
	installed := map[string]string{}
	var targets []string
	for _, install := range preview.Installations {
		installed[install.StoreID] = install.StoreName
		current := install.SnapshotSHA256 == preview.SnapshotSHA256 || install.PendingSHA256 == preview.SnapshotSHA256
		if all && !current {
			targets = append(targets, install.StoreID)
		}
	}
	for _, id := range stores {
		if _, ok := installed[id]; !ok {
			return fmt.Errorf("store %s has not installed the app", id)
		}
		targets = append(targets, id)
	}
	if !all && len(stores) == 0 {
		a.println("Installed stores:")
		for _, install := range preview.Installations {
			a.printf("  %s  %s  %s\n", install.StoreID, install.StoreName, install.InstalledVersion)
		}
		return errors.New("name the stores to update with --store ID, or use --all")
	}
	if len(targets) == 0 {
		a.println("Every installed store already has this draft or has it waiting for approval.")
		return nil
	}
	if !yes {
		ok, err := confirmWith(a.stdin, a.stdout, fmt.Sprintf("Send the draft to %d store(s)?", len(targets)), "Pass --yes to send without confirming")
		if err != nil || !ok {
			if err == nil {
				a.println("Nothing sent.")
			}
			return err
		}
	}
	body := map[string]any{"snapshot_sha256": preview.SnapshotSHA256, "store_ids": targets, "release_notes": notes}
	var sent struct {
		Version string `json:"version"`
	}
	if err := api.Do(ctx, http.MethodPost, "/apps/"+url.PathEscape(appID)+"/private-updates", body, &sent); err != nil {
		return appAccess(err)
	}
	a.printf("Sent version %s to %d store(s). Each store accepts the update in its admin.\n", sent.Version, len(targets))
	return nil
}

func (a *app) appVersions(ctx context.Context, args []string) error {
	fs := newFlags("app versions", "List the app's versions, newest first.")
	appFlag := fs.String("app", "", "app ID (default: app_id in "+appconfig.FileName+")")
	file := fs.String("file", appconfig.FileName, "configuration file")
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
	distribution, err := a.appDistribution(ctx, api, appID)
	if err != nil {
		return err
	}
	if distribution == "private" {
		preview, err := a.privateUpdates(ctx, api, appID)
		if err != nil {
			return err
		}
		if len(preview.Versions) == 0 {
			a.println("No updates sent yet. Stores that install from a link get the draft as it is then.")
		}
		for _, v := range preview.Versions {
			a.printf("%-10s %d accepted, %d waiting\n", v.Version, v.Accepted, v.Pending)
		}
		return nil
	}
	versions, err := a.listAppVersions(ctx, api, appID, 50)
	if err != nil {
		return err
	}
	if len(versions) == 0 {
		a.println("No versions yet. Submit the draft with `tringify app release --bump minor`.")
		return nil
	}
	for _, v := range versions {
		a.printf("%-10s %-15s %s\n", v.Version, v.Status, v.SubmittedAt.UTC().Format("2006-01-02 15:04 UTC"))
	}
	return nil
}

func (a *app) appPublish(ctx context.Context, args []string) error {
	fs := newFlags("app publish VERSION", "Publish an approved version so stores can install or update to it.")
	appFlag := fs.String("app", "", "app ID (default: app_id in "+appconfig.FileName+")")
	file := fs.String("file", appconfig.FileName, "configuration file")
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return errors.New("name the version to publish, for example `tringify app publish 1.2.0`")
	}
	appID, err := appTarget(*appFlag, *file)
	if err != nil {
		return err
	}
	api, _, err := a.devAPI()
	if err != nil {
		return err
	}
	versions, err := a.listAppVersions(ctx, api, appID, 50)
	if err != nil {
		return err
	}
	for _, v := range versions {
		if v.Version != positional[0] {
			continue
		}
		if v.Status != "approved" {
			return fmt.Errorf("version %s is %s; only an approved version can be published", v.Version, v.Status)
		}
		if err := api.Do(ctx, http.MethodPost, "/apps/"+url.PathEscape(appID)+"/versions/"+url.PathEscape(v.ID)+"/publish", nil, nil); err != nil {
			return appAccess(err)
		}
		a.printf("Published version %s.\n", v.Version)
		return nil
	}
	return fmt.Errorf("the app has no version %s. See `tringify app versions`", positional[0])
}

func (a *app) appWebhookTest(ctx context.Context, args []string) error {
	fs := newFlags("app webhook test", "Send a signed app.test webhook to the app's webhook URL and report the answer.")
	appFlag := fs.String("app", "", "app ID (default: app_id in "+appconfig.FileName+")")
	file := fs.String("file", appconfig.FileName, "configuration file")
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
	if err := api.Do(ctx, http.MethodPost, "/apps/"+url.PathEscape(appID)+"/webhooks/test", nil, nil); err != nil {
		return appAccess(err)
	}
	a.println("Your webhook URL accepted the test webhook.")
	return nil
}

func (a *app) appDeliveries(ctx context.Context, args []string) error {
	fs := newFlags("app deliveries [--status failed]", "List recent webhook deliveries to the app, newest first.")
	appFlag := fs.String("app", "", "app ID (default: app_id in "+appconfig.FileName+")")
	file := fs.String("file", appconfig.FileName, "configuration file")
	status := fs.String("status", "", "only deliveries with this status, such as failed or delivered")
	limit := fs.Int("limit", 20, "how many to list (at most 100)")
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
	query := url.Values{"limit": {strconv.Itoa(min(max(*limit, 1), 100))}}
	if *status != "" {
		query.Set("status", *status)
	}
	var data struct {
		Deliveries []struct {
			DeliveryID   string    `json:"delivery_id"`
			EventType    string    `json:"event_type"`
			Status       string    `json:"status"`
			Attempts     int       `json:"attempts"`
			ResponseCode *int      `json:"last_response_code"`
			LastError    *string   `json:"last_error"`
			CreatedAt    time.Time `json:"created_at"`
		} `json:"deliveries"`
		Total int `json:"total"`
	}
	if err := api.Do(ctx, http.MethodGet, "/apps/"+url.PathEscape(appID)+"/webhooks/deliveries?"+query.Encode(), nil, &data); err != nil {
		return appAccess(err)
	}
	if len(data.Deliveries) == 0 {
		a.println("No webhook deliveries.")
		return nil
	}
	for _, d := range data.Deliveries {
		var answer bytes.Buffer
		if d.ResponseCode != nil {
			fmt.Fprintf(&answer, "HTTP %d", *d.ResponseCode)
		}
		if d.LastError != nil && *d.LastError != "" {
			if answer.Len() > 0 {
				answer.WriteString(", ")
			}
			answer.WriteString(*d.LastError)
		}
		a.printf("%s  %-28s %-10s %d attempt(s)  %s\n", d.CreatedAt.UTC().Format("2006-01-02 15:04 UTC"), d.EventType, d.Status, d.Attempts, answer.String())
	}
	if data.Total > len(data.Deliveries) {
		a.printf("Showing %d of %d.\n", len(data.Deliveries), data.Total)
	}
	return nil
}

func (a *app) appInstallLink(ctx context.Context, args []string) error {
	fs := newFlags("app install-link [--store DOMAIN] [--draft] [--expires 7d] [--uses 1]", "Create a link that installs the app on a store. A private app installs its current draft. A marketplace app installs its published version, or with --draft its current draft, which only development and transfer stores of your organization can install.")
	appFlag := fs.String("app", "", "app ID (default: app_id in "+appconfig.FileName+")")
	file := fs.String("file", appconfig.FileName, "configuration file")
	store := fs.String("store", "", "only this store may use the link, for example my-store.mytringify.com")
	draft := fs.Bool("draft", false, "marketplace apps: install the current draft instead of the published version")
	expires := fs.String("expires", "7d", "how long the link works: 24h, 7d, 30d, 90d or never")
	uses := fs.Int("uses", 1, "how many installs the link allows; 0 for unlimited")
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
	distribution, err := a.appDistribution(ctx, api, appID)
	if err != nil {
		return err
	}
	mode := "approved_release"
	if distribution == "private" || *draft {
		mode = "development_release"
	}
	body := map[string]any{"release_mode": mode, "expires_in": *expires}
	if *uses > 0 {
		body["max_uses"] = *uses
	}
	if *store != "" {
		body["store_domain"] = *store
	}
	var link struct {
		InstallURL string `json:"install_url"`
	}
	if err := api.Do(ctx, http.MethodPost, "/apps/"+url.PathEscape(appID)+"/install-tokens", body, &link); err != nil {
		return appAccess(err)
	}
	a.println(link.InstallURL)
	return nil
}

// appCreate creates an app. Its client secret is issued once, in this
// command's output; nothing can read it again later.
func (a *app) appCreate(ctx context.Context, args []string) error {
	fs := newFlags("app create --name NAME --type standard|sales_channel --distribution private|marketplace [--category ID --subcategory ID] [--json]",
		"Create an app in your organization. Sales channel apps need a category and subcategory; the subcategory sets the channel type. Run `tringify app init --app ID` next to start a project for it.")
	name := fs.String("name", "", "app name")
	appType := fs.String("type", "", "standard or sales_channel")
	distribution := fs.String("distribution", "", "private (install links) or marketplace (App Store listing)")
	category := fs.String("category", "", "category ID, for example sales_channels")
	subcategory := fs.String("subcategory", "", "subcategory ID")
	description := fs.String("description", "", "short description")
	asJSON := fs.Bool("json", false, "print the result as JSON, for scripts and piping into a secret store")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	body := map[string]any{"name": *name, "app_type": *appType, "distribution": *distribution}
	for key, value := range map[string]string{"category_id": *category, "subcategory_id": *subcategory, "description": *description} {
		if value != "" {
			body[key] = value
		}
	}
	api, _, err := a.devAPI()
	if err != nil {
		return err
	}
	return a.createApp(ctx, api, body, *asJSON)
}

func (a *app) createApp(ctx context.Context, api *devapi.Client, body map[string]any, asJSON bool) error {
	var created struct {
		App struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			Slug string `json:"slug"`
		} `json:"app"`
		Secrets struct {
			ClientID     string `json:"client_id"`
			ClientSecret string `json:"client_secret"`
		} `json:"secrets"`
	}
	if err := api.Do(ctx, http.MethodPost, "/apps", body, &created); err != nil {
		return appAccess(err)
	}
	if asJSON {
		return json.NewEncoder(a.stdout).Encode(map[string]string{
			"app_id": created.App.ID, "name": created.App.Name, "slug": created.App.Slug,
			"client_id": created.Secrets.ClientID, "client_secret": created.Secrets.ClientSecret,
		})
	}
	a.printf("Created %s\n\n", created.App.Name)
	a.printf("App ID:         %s\n", created.App.ID)
	a.printf("Client ID:      %s\n", created.Secrets.ClientID)
	a.printf("Client secret:  %s\n\n", created.Secrets.ClientSecret)
	a.println("Store the client secret now. It is shown only once; to issue a new one, run tringify app secret rotate.")
	return nil
}

// appSecretRotate issues a new client secret. The previous one stops
// working at once, so update the app's secret right after.
func (a *app) appSecretRotate(ctx context.Context, args []string) error {
	fs := newFlags("app secret rotate [--pipe-to COMMAND | --json]", "Issue a new client secret. The previous secret stops working immediately, so update your app right after. The new secret is shown only in this command's output, or, with --pipe-to, handed to your secret store without being shown.")
	appFlag := fs.String("app", "", "app ID (default: app_id in "+appconfig.FileName+")")
	file := fs.String("file", appconfig.FileName, "configuration file")
	asJSON := fs.Bool("json", false, "print the result as JSON, for scripts and piping into a secret store")
	pipeTo := fs.String("pipe-to", "", "command that stores the new secret, given on its standard input; the secret is not printed")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	if *pipeTo != "" && *asJSON {
		return errors.New("use --pipe-to or --json, not both")
	}
	appID, err := appTarget(*appFlag, *file)
	if err != nil {
		return err
	}
	api, _, err := a.devAPI()
	if err != nil {
		return err
	}
	return a.rotateClientSecret(ctx, api, appID, *asJSON, *pipeTo)
}

func (a *app) rotateClientSecret(ctx context.Context, api *devapi.Client, appID string, asJSON bool, pipeTo string) error {
	var rotated struct {
		ClientSecret string `json:"client_secret"`
	}
	if err := api.Do(ctx, http.MethodPost, "/apps/"+url.PathEscape(appID)+"/regenerate-secret", nil, &rotated); err != nil {
		return appAccess(err)
	}
	secret := rotated.ClientSecret
	if secret == "" {
		return errors.New("the API did not return the new client secret")
	}
	if pipeTo != "" {
		return a.storeSecret(ctx, "client secret", secret, pipeTo)
	}
	if asJSON {
		return json.NewEncoder(a.stdout).Encode(map[string]string{"app_id": appID, "client_secret": secret})
	}
	a.printf("Client secret:  %s\n\n", secret)
	a.println("Store it now; it is shown only once. The previous client secret no longer works.")
	return nil
}

// appWebhookRotateKey issues a new webhook signing secret. The old one stops
// working at once, so update the app's secret right after.
func (a *app) appWebhookRotateKey(ctx context.Context, args []string) error {
	fs := newFlags("app webhook rotate-key [--pipe-to COMMAND | --json]", "Issue a new webhook signing secret. The previous secret stops verifying deliveries immediately, so update your app right after. The new secret is shown only in this command's output, or, with --pipe-to, handed to your secret store without being shown.")
	appFlag := fs.String("app", "", "app ID (default: app_id in "+appconfig.FileName+")")
	file := fs.String("file", appconfig.FileName, "configuration file")
	asJSON := fs.Bool("json", false, "print the result as JSON, for scripts and piping into a secret store")
	pipeTo := fs.String("pipe-to", "", "command that stores the new secret, given on its standard input; the secret is not printed")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	if *pipeTo != "" && *asJSON {
		return errors.New("use --pipe-to or --json, not both")
	}
	appID, err := appTarget(*appFlag, *file)
	if err != nil {
		return err
	}
	api, _, err := a.devAPI()
	if err != nil {
		return err
	}
	return a.rotateWebhookKey(ctx, api, appID, *asJSON, *pipeTo)
}

func (a *app) rotateWebhookKey(ctx context.Context, api *devapi.Client, appID string, asJSON bool, pipeTo string) error {
	var rotated struct {
		SigningKey string `json:"signing_key"`
	}
	if err := api.Do(ctx, http.MethodPost, "/apps/"+url.PathEscape(appID)+"/webhooks/rotate-signing-key", nil, &rotated); err != nil {
		return appAccess(err)
	}
	secret := rotated.SigningKey
	if secret == "" {
		return errors.New("the API did not return the new signing secret")
	}
	if pipeTo != "" {
		return a.storeSecret(ctx, "webhook signing secret", secret, pipeTo)
	}
	if asJSON {
		return json.NewEncoder(a.stdout).Encode(map[string]string{"app_id": appID, "webhook_signing_secret": secret})
	}
	a.printf("Webhook signing secret:  %s\n\n", secret)
	a.println("Store it now; it is shown only once. Deliveries signed with the previous secret no longer verify.")
	return nil
}

// storeSecret hands a newly issued secret to the developer's own secret store
// command on its standard input, so it never appears on screen. Any host
// works: wrangler, gcloud, fly, vercel, a password manager. The command runs
// in the system shell so it can be written as it would be typed.
func (a *app) storeSecret(ctx context.Context, what, secret, command string) error {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/C", command)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", command)
	}
	cmd.Stdin = strings.NewReader(secret)
	cmd.Stdout, cmd.Stderr = a.stderr, a.stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("a new %s was issued, but %q failed (%v). The previous %s no longer works and the new one was not shown; run this command again to issue another", what, command, err, what)
	}
	a.printf("Issued a new %s and stored it with: %s\n", what, command)
	return nil
}
