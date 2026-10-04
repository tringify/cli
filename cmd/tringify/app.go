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
	"strconv"
	"time"

	"github.com/tringify/cli/internal/appconfig"
	"github.com/tringify/cli/internal/devapi"
)

func (a *app) appCommand(ctx context.Context, args []string) error {
	if len(args) == 0 {
		fmt.Fprint(a.stdout, usage)
		return nil
	}
	switch args[0] {
	case "list":
		return a.appList(ctx, args[1:])
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
	case "versions":
		return a.appVersions(ctx, args[1:])
	case "publish":
		return a.appPublish(ctx, args[1:])
	case "webhook":
		if len(args) > 1 && args[1] == "test" {
			return a.appWebhookTest(ctx, args[2:])
		}
		return errors.New("usage: tringify app webhook test")
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
	var data struct {
		Apps []map[string]any `json:"apps"`
	}
	if err := api.Do(ctx, http.MethodGet, "/apps?limit=100", nil, &data); err != nil {
		return appAccess(err)
	}
	if len(data.Apps) == 0 {
		a.printf("No apps in %s. Create one in the Developer Portal under Apps.\n", session.Account.Target.Name)
		return nil
	}
	for _, item := range data.Apps {
		a.printf("%s  %s  %s  %s\n", str(item["id"]), str(item["name"]), str(item["app_type"]), str(item["submission_status"]))
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
	a.println("Saved the draft. Release it with `tringify app release --bump patch|minor|major`.")
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

func (a *app) appRelease(ctx context.Context, args []string) error {
	fs := newFlags("app release --bump patch|minor|major [--notes TEXT]", "Submit the app's draft as a new version. The version number is the previous one with the part you name increased; use major for changes that need stores to approve new access.")
	appFlag := fs.String("app", "", "app ID (default: app_id in "+appconfig.FileName+")")
	file := fs.String("file", appconfig.FileName, "configuration file")
	bump := fs.String("bump", "", "patch, minor or major")
	notes := fs.String("notes", "", "release notes shown to stores")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	if *bump != "patch" && *bump != "minor" && *bump != "major" {
		return errors.New("--bump must be patch, minor or major")
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
