package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
)

// theme docs: a theme listing's documentation on
// themes-support.tringify.com, in the same file format as `tringify app docs
// push`: one .md file per page, the file name is the page address, front
// matter gives the title and order.

type themeSite struct {
	ListingSlug  string        `json:"listing_slug"`
	PublicURL    string        `json:"public_url"`
	Limits       siteLimits    `json:"limits"`
	Draft        *siteRevision `json:"draft"`
	Submitted    *siteRevision `json:"submitted"`
	Published    *siteRevision `json:"published"`
	LastDecision *struct {
		Action string `json:"action"`
		Note   string `json:"note"`
	} `json:"last_decision"`
}

func (a *app) themeDocs(ctx context.Context, args []string) error {
	if len(args) > 0 && args[0] == "status" {
		return a.themeDocsStatus(ctx, args[1:])
	}
	if len(args) > 0 && args[0] == "push" {
		args = args[1:]
	}
	fs := newFlags("theme docs [push] --listing ID [--draft] [DIR] | theme docs status --listing ID", "Publish the theme's documentation on themes-support.tringify.com/<slug>/docs from the .md files in DIR (default ./docs), the same format as `tringify app docs push`: one file per page, the file name (without .md) is the page address, and each file starts with front matter (---, title: ..., order: N, ---). The pages are saved as the draft and submitted; Tringify reviews them before they go live (Tringify's own themes publish at once). --draft only saves the draft.")
	listing := fs.String("listing", "", "theme listing ID (see `tringify theme listings`)")
	draftOnly := fs.Bool("draft", false, "save the draft without submitting it")
	rest, err := parse(fs, args)
	if err != nil {
		return err
	}
	if *listing == "" {
		return errors.New("--listing is required")
	}
	dir := "docs"
	if len(rest) > 0 {
		dir = rest[0]
	}
	base := "/org/themes/" + *listing + "/docs"
	var site themeSite
	if err := a.mustDevAPIDo(ctx, http.MethodGet, base, nil, &site); err != nil {
		return err
	}
	pages, err := readDocsPages(dir, site.Limits)
	if err != nil {
		return err
	}
	for _, p := range pages {
		a.warnImages(filepath.Join(dir, p.Slug+".md"), p.BodyMarkdown, site.Limits.ImageHosts)
	}
	body := map[string]any{"locales": map[string]any{site.Limits.RequiredLocale: map[string]any{"pages": pages}}}
	if err := a.mustDevAPIDo(ctx, http.MethodPut, base+"/draft", body, &site); err != nil {
		return err
	}
	if site.Draft == nil {
		return errors.New("the draft was not saved")
	}
	a.printf("Saved the draft: %d pages.\n", len(pages))
	if *draftOnly {
		return nil
	}
	draftID := site.Draft.ID
	if err := a.mustDevAPIDo(ctx, http.MethodPost, base+"/submit", map[string]any{"expected_sha256": site.Draft.ContentSHA256}, &site); err != nil {
		return err
	}
	if site.Published != nil && site.Published.ID == draftID {
		a.printf("Published version %s: %s\n", versionLabel(site.Published), site.PublicURL)
		return nil
	}
	a.printf("Submitted for review. Once approved it is published at %s\n", site.PublicURL)
	return nil
}

func (a *app) themeDocsStatus(ctx context.Context, args []string) error {
	fs := newFlags("theme docs status --listing ID", "Show the theme documentation's published version, submission and draft.")
	listing := fs.String("listing", "", "theme listing ID")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	if *listing == "" {
		return errors.New("--listing is required")
	}
	var site themeSite
	if err := a.mustDevAPIDo(ctx, http.MethodGet, "/org/themes/"+*listing+"/docs", nil, &site); err != nil {
		return err
	}
	a.printf("Documentation of %s: %s\n", site.ListingSlug, site.PublicURL)
	show := func(label string, r *siteRevision) {
		if r == nil {
			return
		}
		line := fmt.Sprintf("  %-10s %s, %s", label, versionLabel(r), docsPageCount(r))
		if r.SiteStatus != "" {
			line += ", site " + r.SiteStatus
		}
		if r.SiteError != "" {
			line += ": " + r.SiteError
		}
		a.println(line)
	}
	show("Published", site.Published)
	show("Submitted", site.Submitted)
	show("Draft", site.Draft)
	if site.Published == nil && site.Submitted == nil && site.Draft == nil {
		a.println("  Nothing yet. Write the pages in ./docs and run `tringify theme docs --listing " + *listing + "`.")
	}
	if site.LastDecision != nil && site.LastDecision.Action == "reject" {
		a.println("  Changes requested: " + site.LastDecision.Note)
	}
	return nil
}
