package main

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tringify/cli/internal/upload"
)

func (a *app) themeCreate(ctx context.Context, args []string) error {
	fs := newFlags("theme create --name NAME --slug SLUG [--support-url URL] [--terms-url URL]", "Create a free theme listing in your developer organization. Publish versions to it with `tringify theme publish`.")
	name := fs.String("name", "", "listing name shown in the Theme Store")
	slug := fs.String("slug", "", "URL name, lowercase letters, numbers and hyphens")
	support := fs.String("support-url", "", "where merchants get help")
	terms := fs.String("terms-url", "", "terms of use page")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	if *name == "" || *slug == "" {
		return errors.New("--name and --slug are required")
	}
	api, session, err := a.devAPI()
	if err != nil {
		return err
	}
	body := map[string]any{"name": *name, "slug": *slug, "commercial_model": "free"}
	if *support != "" {
		body["support_url"] = *support
	}
	if *terms != "" {
		body["terms_url"] = *terms
	}
	var created map[string]any
	if err := api.Do(ctx, http.MethodPost, "/org/themes", body, &created); err != nil {
		return err
	}
	id := str(created["id"])
	if id == "" {
		id = str(created["listing_id"])
	}
	a.printf("Created listing %s (%s) in %s.\n", *name, id, session.Account.Target.Name)
	a.println("Next: `tringify theme update --listing " + id + "` for its text and screenshots, then `tringify theme publish`.")
	return nil
}

func (a *app) themeCategories(ctx context.Context, args []string) error {
	fs := newFlags("theme categories", "List the Theme Store categories, for `tringify theme update --category`.")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	api, _, err := a.devAPI()
	if err != nil {
		return err
	}
	var data map[string]any
	if err := api.Do(ctx, http.MethodGet, "/org/theme-categories", nil, &data); err != nil {
		return err
	}
	for _, c := range listAt(data, "categories") {
		a.printf("%s  %s\n", str(c["slug"]), str(c["name"]))
	}
	return nil
}

func (a *app) themeUpdate(ctx context.Context, args []string) error {
	fs := newFlags("theme update --listing ID [--tagline T] [--description D] [--category SLUG] [--screenshot FILE]... [--style-cover STYLE=FILE]...", "Change a listing's text, category, links, screenshots and style covers. Only the parts you pass change. Screenshots and covers are 1280×720 PNG, JPEG or WebP; passing --screenshot replaces all screenshots, in the order given, and passing --style-cover replaces all covers.")
	listing := fs.String("listing", "", "theme listing ID (see `tringify theme listings`)")
	name := fs.String("name", "", "new listing name")
	tagline := fs.String("tagline", "", "one-line tagline")
	description := fs.String("description", "", "listing description")
	descriptionFile := fs.String("description-file", "", "read the description from this file")
	category := fs.String("category", "", "category slug or ID (see `tringify theme categories`)")
	support := fs.String("support-url", "", "where merchants get help")
	terms := fs.String("terms-url", "", "terms of use page")
	var screenshots, covers stringList
	fs.Var(&screenshots, "screenshot", "screenshot image; repeat for each, in display order")
	fs.Var(&covers, "style-cover", "a style's cover as STYLE=FILE, where STYLE is the preset name; repeat for each style")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	if *listing == "" {
		return errors.New("--listing is required")
	}
	if *descriptionFile != "" {
		raw, err := os.ReadFile(*descriptionFile)
		if err != nil {
			return err
		}
		*description = strings.TrimSpace(string(raw))
	}
	type cover struct{ name, path string }
	var coverList []cover
	for _, c := range covers {
		style, path, ok := strings.Cut(c, "=")
		if !ok || strings.TrimSpace(style) == "" || path == "" {
			return fmt.Errorf("--style-cover %q must be STYLE=FILE", c)
		}
		coverList = append(coverList, cover{strings.TrimSpace(style), path})
	}
	for _, p := range append(append([]string{}, screenshots...), func() []string {
		var ps []string
		for _, c := range coverList {
			ps = append(ps, c.path)
		}
		return ps
	}()...) {
		if _, err := mediaType(p); err != nil {
			return err
		}
	}
	api, _, err := a.devAPI()
	if err != nil {
		return err
	}
	var current map[string]any
	if err := api.Do(ctx, http.MethodGet, "/org/themes/"+url.PathEscape(*listing), nil, &current); err != nil {
		return err
	}
	body := map[string]any{"name": str(current["name"])}
	if *name != "" {
		body["name"] = *name
	}
	if *tagline != "" {
		body["tagline"] = *tagline
	}
	if *description != "" {
		body["description"] = *description
	}
	if *support != "" {
		body["support_url"] = *support
	}
	if *terms != "" {
		body["terms_url"] = *terms
	}
	if *category != "" {
		id, err := categoryID(ctx, api, *category)
		if err != nil {
			return err
		}
		body["category_id"] = id
	}
	if len(screenshots) > 0 {
		var ids []string
		for _, p := range screenshots {
			id, err := a.uploadListingMedia(ctx, api, p)
			if err != nil {
				return err
			}
			ids = append(ids, id)
		}
		body["screenshot_file_ids"] = ids
	}
	if len(coverList) > 0 {
		var list []map[string]string
		for _, c := range coverList {
			id, err := a.uploadListingMedia(ctx, api, c.path)
			if err != nil {
				return err
			}
			list = append(list, map[string]string{"name": c.name, "file_id": id})
		}
		body["style_covers"] = list
	}
	if len(body) == 1 && *name == "" {
		return errors.New("nothing to change: pass at least one of the flags")
	}
	if err := api.Do(ctx, http.MethodPut, "/org/themes/"+url.PathEscape(*listing), body, nil); err != nil {
		return err
	}
	a.printf("Updated listing %s.\n", *listing)
	return nil
}

func (a *app) themeSubmit(ctx context.Context, args []string) error {
	fs := newFlags("theme submit --listing ID", "Submit a listing for Theme Store review. Listings from the Tringify organization are approved straight away.")
	listing := fs.String("listing", "", "theme listing ID (see `tringify theme listings`)")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	if *listing == "" {
		return errors.New("--listing is required")
	}
	api, _, err := a.devAPI()
	if err != nil {
		return err
	}
	path := "/org/themes/" + url.PathEscape(*listing) + "/review"
	var review map[string]any
	if err := api.Do(ctx, http.MethodGet, path, nil, &review); err != nil {
		return err
	}
	var next map[string]any
	body := map[string]any{"action": "submit", "expected_event_id": str(review["latest_event_id"]), "expected_revision": str(review["review_revision"])}
	if err := api.Do(ctx, http.MethodPost, path, body, &next); err != nil {
		return err
	}
	status := str(next["status"])
	if status == "" {
		status = "submitted"
	}
	a.printf("Submitted listing %s for review (%s).\n", *listing, status)
	return nil
}

// mediaType checks a listing image and returns its MIME type.
func mediaType(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s is a directory", path)
	}
	t := mime.TypeByExtension(strings.ToLower(filepath.Ext(path)))
	if i := strings.IndexByte(t, ';'); i >= 0 {
		t = t[:i]
	}
	switch t {
	case "image/png", "image/jpeg", "image/webp":
		return t, nil
	}
	return "", fmt.Errorf("%s must be a PNG, JPEG or WebP image", path)
}

// uploadListingMedia uploads one listing image and returns its file ID.
func (a *app) uploadListingMedia(ctx context.Context, api devAPIDoer, path string) (string, error) {
	t, err := mediaType(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	var grant upload.Grant
	if err := api.Do(ctx, http.MethodPost, "/org/themes/media/upload-intents", map[string]any{"original_name": filepath.Base(path), "mime_type": t, "size_bytes": info.Size()}, &grant); err != nil {
		return "", fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	if grant.Headers == nil {
		grant.Headers = map[string]string{}
	}
	if grant.Headers["Content-Type"] == "" {
		grant.Headers["Content-Type"] = t
	}
	if err := upload.Put(ctx, &http.Client{Timeout: 2 * time.Minute}, grant, path); err != nil {
		return "", fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	ids := map[string]any{"intent_ids": []string{grant.IntentID}}
	if err := api.Do(ctx, http.MethodPost, "/org/themes/media/upload-intents/finalize", ids, nil); err != nil {
		return "", fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	deadline := time.Now().Add(2 * time.Minute)
	for {
		var status struct {
			Intents []struct {
				Status string `json:"status"`
				File   *struct {
					ID string `json:"id"`
				} `json:"file"`
				Error *struct {
					Message string `json:"message"`
				} `json:"error"`
			} `json:"intents"`
		}
		if err := api.Do(ctx, http.MethodPost, "/org/themes/media/upload-intents/status", ids, &status); err != nil {
			return "", fmt.Errorf("%s: %w", filepath.Base(path), err)
		}
		if len(status.Intents) == 1 {
			s := status.Intents[0]
			switch s.Status {
			case "ready":
				if s.File == nil || s.File.ID == "" {
					return "", fmt.Errorf("%s: the upload finished without a file", filepath.Base(path))
				}
				a.printf("Uploaded %s.\n", filepath.Base(path))
				return s.File.ID, nil
			case "pending", "processing":
			default:
				msg := s.Status
				if s.Error != nil && s.Error.Message != "" {
					msg = s.Error.Message
				}
				return "", fmt.Errorf("%s was not accepted: %s", filepath.Base(path), msg)
			}
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("%s: still processing after two minutes", filepath.Base(path))
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// categoryID accepts a category slug, name or ID.
func categoryID(ctx context.Context, api devAPIDoer, want string) (string, error) {
	var data map[string]any
	if err := api.Do(ctx, http.MethodGet, "/org/theme-categories", nil, &data); err != nil {
		return "", err
	}
	for _, c := range listAt(data, "categories") {
		if str(c["id"]) == want || strings.EqualFold(str(c["slug"]), want) || strings.EqualFold(str(c["name"]), want) {
			return str(c["id"]), nil
		}
	}
	return "", fmt.Errorf("no category %q. See `tringify theme categories`", want)
}

// devAPIDoer is the part of the developer API client these commands use.
type devAPIDoer interface {
	Do(ctx context.Context, method, path string, body, out any) error
}
