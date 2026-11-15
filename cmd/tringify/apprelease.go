package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/tringify/cli/internal/appconfig"
	"github.com/tringify/cli/internal/devapi"
)

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
	fs := newFlags("app release [--bump patch|minor|major] [--notes TEXT]", "Release the app's draft. Checks the draft's app blocks first (see `tringify app block check`) and releases nothing if they have problems. A marketplace app submits it as a new version: the previous version number with the part you name in --bump increased, where major marks the version as breaking. A private app releases it as a new version to every store that has the app. Stores move to a new version by themselves; one that asks for more access waits until the store approves it.")
	appFlag := fs.String("app", "", "app ID (default: app_id in "+appconfig.FileName+")")
	file := fs.String("file", appconfig.FileName, "configuration file")
	bump := fs.String("bump", "", "marketplace apps: patch, minor or major")
	notes := fs.String("notes", "", "release notes shown to stores")
	yes := fs.Bool("yes", false, "private apps: release without asking")
	renderer := fs.String("renderer", "", rendererHelp)
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
	// Releasing with unpushed edits would ship something other than the files.
	if doc, _, readErr := appconfig.Read(*file); readErr == nil && doc.AppID() == appID {
		local, _, _, err := appconfig.Load(*file)
		if err != nil {
			return err
		}
		current, _ := appconfig.Parse(remote)
		if len(appconfig.Diff(current, local)) > 0 {
			return fmt.Errorf("%s or a block source differs from the draft. Run `tringify app config push` first", *file)
		}
	}
	if err := a.checkBlocks(ctx, *file, remote, *renderer); err != nil {
		return fmt.Errorf("%w; nothing released", err)
	}
	distribution, err := a.appDistribution(ctx, api, appID)
	if err != nil {
		return err
	}
	if distribution == "private" {
		if *bump != "" {
			return errors.New("--bump is for marketplace apps; private updates are numbered for you")
		}
		return a.sendPrivateUpdate(ctx, api, appID, *notes, *yes)
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

// unavailable is the API's unavailable_regions list: stores an answer is
// missing. Users are told that some stores are missing, not where they are.
type unavailable []struct{}

// printUnavailable says when an answer is missing some stores.
func (a *app) printUnavailable(missing unavailable) {
	if len(missing) > 0 {
		a.println("Some stores couldn't be reached right now and are not included. Try again in a few minutes.")
	}
}

type privateUpdatePreview struct {
	SnapshotSHA256     string      `json:"snapshot_sha256"`
	Installed          int         `json:"installed"`
	OutOfDate          int         `json:"out_of_date"`
	UnavailableRegions unavailable `json:"unavailable_regions"`
}

func (a *app) privateUpdates(ctx context.Context, api *devapi.Client, appID string) (privateUpdatePreview, error) {
	var preview privateUpdatePreview
	err := api.Do(ctx, http.MethodGet, "/apps/"+url.PathEscape(appID)+"/private-updates", nil, &preview)
	return preview, appAccess(err)
}

type privateUpdateSent struct {
	Release struct {
		Version string `json:"version"`
	} `json:"release"`
	Offered int `json:"offered"`
	Regions []struct {
		Name    string `json:"name"`
		Status  string `json:"status"`
		Message string `json:"message"`
	} `json:"regions"`
	NextCursor string `json:"next_cursor"`
}

func (a *app) sendPrivateUpdate(ctx context.Context, api *devapi.Client, appID string, notes string, yes bool) error {
	preview, err := a.privateUpdates(ctx, api, appID)
	if err != nil {
		return err
	}
	a.printUnavailable(preview.UnavailableRegions)
	if preview.Installed == 0 && len(preview.UnavailableRegions) == 0 {
		return errors.New("no store has installed the app yet. Create an install link with `tringify app install-link`")
	}
	if preview.OutOfDate == 0 {
		a.println("Every store that has the app already runs this draft or has it waiting for approval.")
		return nil
	}
	if !yes {
		ok, err := confirmWith(a.stdin, a.stdout, fmt.Sprintf("Release the draft to %d store(s)?", preview.OutOfDate), "Pass --yes to release without confirming")
		if err != nil || !ok {
			if err == nil {
				a.println("Nothing released.")
			}
			return err
		}
	}
	path := "/apps/" + url.PathEscape(appID) + "/private-updates"
	failed, offered, version := 0, 0, ""
	// Each region moves its own out-of-date stores; a release that has more
	// to do returns next_cursor and the same request continues it.
	cursor := ""
	for {
		var sent privateUpdateSent
		err := api.Do(ctx, http.MethodPost, path, map[string]any{"snapshot_sha256": preview.SnapshotSHA256, "cursor": cursor, "release_notes": notes}, &sent)
		for _, r := range sent.Regions {
			a.println(r.Message)
			if r.Status == "failed" {
				failed++
			}
		}
		if err != nil {
			return appAccess(err)
		}
		offered, version = offered+sent.Offered, sent.Release.Version
		if cursor = sent.NextCursor; cursor == "" {
			break
		}
	}
	if offered > 0 {
		a.printf("Released version %s to %d store(s). Stores move to it by themselves; any that it asks for more access approve it in their admin.\n", version, offered)
	} else {
		a.println("Nothing released.")
	}
	if failed > 0 {
		return errors.New("the release did not reach every store (see above). Run the command again to reach the rest")
	}
	return nil
}

func (a *app) appVersions(ctx context.Context, args []string) error {
	fs := newFlags("app versions", "List the app's versions, newest first. For a private app, each version shows how many stores run it, how many are waiting to approve the more access it asks for, how many moved on to a later version, and its release notes.")
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
		var history struct {
			Versions []struct {
				Version    string `json:"version"`
				Notes      string `json:"release_notes"`
				Pending    int    `json:"pending"`
				Accepted   int    `json:"accepted"`
				Superseded int    `json:"superseded"`
			} `json:"versions"`
			UnavailableRegions unavailable `json:"unavailable_regions"`
		}
		if err := api.Do(ctx, http.MethodGet, "/apps/"+url.PathEscape(appID)+"/private-updates/versions", nil, &history); err != nil {
			return appAccess(err)
		}
		a.printUnavailable(history.UnavailableRegions)
		if len(history.Versions) == 0 {
			a.println("No updates sent yet. Stores that install from a link get the draft as it is then.")
		}
		for _, v := range history.Versions {
			a.printf("%-10s %d running, %d waiting for approval, %d moved to a later version\n", v.Version, v.Accepted, v.Pending, v.Superseded)
			if notes := strings.TrimSpace(v.Notes); notes != "" {
				for _, line := range strings.Split(notes, "\n") {
					a.printf("           %s\n", line)
				}
			}
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

// installLinkChoices are the install link form's choices and defaults, from
// the Developer API's install registry.
type installLinkChoices struct {
	Registry struct {
		Expiries []struct {
			ID    string `json:"id"`
			Label string `json:"label"`
		} `json:"link_expiries"`
		DefaultExpiry  string `json:"default_link_expiry"`
		DefaultMaxUses int    `json:"default_link_max_uses"`
	} `json:"install_lifecycle_registry"`
	MaxUsesMax int `json:"install_token_max_uses_max"`
}

func (a *app) appInstallLink(ctx context.Context, args []string) error {
	fs := newFlags("app install-link [--store DOMAIN] [--draft] [--expires CHOICE] [--uses N]", "Create a link that installs the app on a store. A private app installs its current draft. A marketplace app installs its published version, or with --draft its current draft, which only development and transfer stores of your organization can install. Without --expires and --uses the link gets the Developer Portal's defaults.")
	appFlag := fs.String("app", "", "app ID (default: app_id in "+appconfig.FileName+")")
	file := fs.String("file", appconfig.FileName, "configuration file")
	store := fs.String("store", "", "only this store may use the link, for example my-store.mytringify.com")
	draft := fs.Bool("draft", false, "marketplace apps: install the current draft instead of the published version")
	expires := fs.String("expires", "", "how long the link works, for example 7d or never; a value the API does not offer is refused with the list of choices")
	uses := fs.Int("uses", -1, "how many installs the link allows; 0 for unlimited")
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
	var choices installLinkChoices
	if err := api.Do(ctx, http.MethodGet, "/apps/limits", nil, &choices); err != nil {
		return appAccess(err)
	}
	expiry := *expires
	if expiry == "" {
		expiry = choices.Registry.DefaultExpiry
	}
	var offered []string
	known := false
	for _, choice := range choices.Registry.Expiries {
		offered = append(offered, choice.ID+" ("+choice.Label+")")
		known = known || choice.ID == expiry
	}
	if !known {
		return fmt.Errorf("--expires must be one of: %s", strings.Join(offered, ", "))
	}
	maxUses := *uses
	if maxUses < 0 {
		maxUses = choices.Registry.DefaultMaxUses
	}
	if maxUses > choices.MaxUsesMax {
		return fmt.Errorf("--uses can be at most %d, or 0 for unlimited", choices.MaxUsesMax)
	}
	distribution, err := a.appDistribution(ctx, api, appID)
	if err != nil {
		return err
	}
	mode := "approved_release"
	if distribution == "private" || *draft {
		mode = "development_release"
	}
	body := map[string]any{"release_mode": mode, "expires_in": expiry}
	if maxUses > 0 {
		body["max_uses"] = maxUses
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
