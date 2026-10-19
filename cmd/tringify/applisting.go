package main

import (
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/tringify/cli/internal/appconfig"
)

// App Store listing: the text, links, icon and screenshots merchants see.
// A listing change reaches the App Store with the app's next release.

var appListingFields = []string{"tagline", "short_description", "description", "support_email", "support_url", "privacy_url", "terms_url"}

func (a *app) appListing(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: tringify app listing show|update")
	}
	switch args[0] {
	case "show":
		return a.appListingShow(ctx, args[1:])
	case "update":
		return a.appListingUpdate(ctx, args[1:])
	}
	return fmt.Errorf("unknown app listing command %q", args[0])
}

func (a *app) appListingShow(ctx context.Context, args []string) error {
	fs := newFlags("app listing show [--app ID]", "Show the app's App Store listing.")
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
	var current map[string]any
	if err := api.Do(ctx, http.MethodGet, "/apps/"+url.PathEscape(appID), nil, &current); err != nil {
		return err
	}
	a.printf("%s\n", str(current["name"]))
	for _, row := range [][2]string{{"Tagline", "tagline"}, {"Short description", "short_description"}, {"Icon", "icon_url"}, {"Support email", "support_email"}, {"Support URL", "support_url"}, {"Privacy URL", "privacy_url"}, {"Terms URL", "terms_url"}} {
		v := str(current[row[1]])
		if v == "" {
			v = "—"
		}
		a.printf("  %-18s %s\n", row[0]+":", v)
	}
	shots, _ := current["screenshot_file_ids"].([]any)
	a.printf("  %-18s %d\n", "Screenshots:", len(shots))
	if desc := str(current["description"]); desc != "" {
		a.printf("\n%s\n", desc)
	}
	return nil
}

func (a *app) appListingUpdate(ctx context.Context, args []string) error {
	fs := newFlags("app listing update [--app ID] [--icon FILE] [--screenshot FILE]... [--tagline T] [--description D]", "Change the app's App Store listing. Only the parts you pass change. The icon is a 512×512 PNG, JPEG or WebP; passing --screenshot replaces all screenshots, in the order given. The change reaches the App Store with the next `tringify app release` and `tringify app publish`.")
	appFlag := fs.String("app", "", "app ID (default: app_id in "+appconfig.FileName+")")
	file := fs.String("file", appconfig.FileName, "configuration file")
	icon := fs.String("icon", "", "app icon image, 512×512")
	tagline := fs.String("tagline", "", "one-line tagline")
	short := fs.String("short-description", "", "short description")
	description := fs.String("description", "", "listing description")
	descriptionFile := fs.String("description-file", "", "read the description from this file")
	supportEmail := fs.String("support-email", "", "where merchants email for help")
	supportURL := fs.String("support-url", "", "where merchants get help")
	privacyURL := fs.String("privacy-url", "", "privacy policy page")
	termsURL := fs.String("terms-url", "", "terms of use page")
	var screenshots stringList
	fs.Var(&screenshots, "screenshot", "screenshot image; repeat for each, in display order")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	appID, err := appTarget(*appFlag, *file)
	if err != nil {
		return err
	}
	if *descriptionFile != "" {
		raw, err := os.ReadFile(*descriptionFile)
		if err != nil {
			return err
		}
		*description = strings.TrimSpace(string(raw))
	}
	if *icon != "" {
		if err := checkIcon(*icon); err != nil {
			return err
		}
	}
	for _, p := range screenshots {
		if _, err := mediaType(p); err != nil {
			return err
		}
	}
	api, _, err := a.devAPI()
	if err != nil {
		return err
	}
	return a.updateAppListing(ctx, api, appID, appListingChange{
		icon: *icon, screenshots: screenshots,
		text: map[string]string{"tagline": *tagline, "short_description": *short, "description": *description, "support_email": *supportEmail, "support_url": *supportURL, "privacy_url": *privacyURL, "terms_url": *termsURL},
	})
}

type appListingChange struct {
	icon        string
	screenshots []string
	text        map[string]string
}

// updateAppListing applies a change to the app's listing, keeping every
// field the change does not name.
func (a *app) updateAppListing(ctx context.Context, api devAPIDoer, appID string, change appListingChange) error {
	var current map[string]any
	if err := api.Do(ctx, http.MethodGet, "/apps/"+url.PathEscape(appID), nil, &current); err != nil {
		return err
	}
	// The listing update replaces every field it is sent and clears the ones
	// it is not, so start from the listing as it is and change only what was asked.
	body := map[string]any{}
	for _, key := range appListingFields {
		body[key] = str(current[key])
	}
	body["icon_file_id"] = str(current["icon_file_id"])
	ids := []any{}
	if list, ok := current["screenshot_file_ids"].([]any); ok {
		ids = list
	}
	body["screenshot_file_ids"] = ids
	changed := false
	for key, v := range change.text {
		if v != "" {
			body[key] = v
			changed = true
		}
	}
	if change.icon != "" {
		id, err := a.uploadMedia(ctx, api, change.icon, "/org/files/upload-intents", map[string]any{"category": "app_icon"})
		if err != nil {
			return err
		}
		body["icon_file_id"] = id
		changed = true
	}
	if len(change.screenshots) > 0 {
		var shots []string
		for _, p := range change.screenshots {
			id, err := a.uploadMedia(ctx, api, p, "/org/files/upload-intents", map[string]any{"category": "app_screenshot"})
			if err != nil {
				return err
			}
			shots = append(shots, id)
		}
		body["screenshot_file_ids"] = shots
		changed = true
	}
	if !changed {
		return errors.New("nothing to change: pass at least one of the flags")
	}
	if err := api.Do(ctx, http.MethodPut, "/apps/"+url.PathEscape(appID)+"/listing", body, nil); err != nil {
		return err
	}
	a.printf("Updated the listing of %s. Release and publish the app to show it in the App Store:\n  tringify app release --app %s --bump patch --notes \"Listing update.\"\n  tringify app publish VERSION --app %s\n", str(current["name"]), appID, appID)
	return nil
}

// checkIcon rejects an icon that is not a 512×512 image before uploading it.
func checkIcon(path string) error {
	t, err := mediaType(path)
	if err != nil {
		return err
	}
	if t == "image/webp" {
		return nil // the App Store checks WebP dimensions on upload
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return fmt.Errorf("%s could not be read as an image: %w", path, err)
	}
	if cfg.Width != 512 || cfg.Height != 512 {
		return fmt.Errorf("%s is %d×%d; the app icon must be 512×512", path, cfg.Width, cfg.Height)
	}
	return nil
}
