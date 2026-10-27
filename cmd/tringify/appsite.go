package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/tringify/cli/internal/appconfig"
)

// Every app has three documents Tringify hosts on its support site: a
// privacy policy, terms of service and documentation pages. Each is saved as
// a draft, submitted for review and published as a numbered version. The
// limits a document must meet come from the Developer API (GET /site).

type siteLimits struct {
	Locales           []string `json:"locales"`
	RequiredLocale    string   `json:"required_locale"`
	BodyMaxChars      int      `json:"body_max_chars"`
	DocsMaxPages      int      `json:"docs_max_pages"`
	DocsTotalMaxChars int      `json:"docs_total_max_chars"`
	PageTitleMaxChars int      `json:"page_title_max_chars"`
	PageSlugPattern   string   `json:"page_slug_pattern"`
	PageSlugMaxChars  int      `json:"page_slug_max_chars"`
	ImageHosts        []string `json:"image_hosts"`
}

type sitePage struct {
	Slug         string `json:"slug"`
	Title        string `json:"title"`
	BodyMarkdown string `json:"body_markdown"`
}

type siteLocale struct {
	BodyMarkdown string     `json:"body_markdown,omitempty"`
	Pages        []sitePage `json:"pages,omitempty"`
}

type siteRevision struct {
	ID            string                `json:"id"`
	Status        string                `json:"status"`
	Version       *int                  `json:"version"`
	Source        string                `json:"source"`
	ContentSHA256 string                `json:"content_sha256"`
	Locales       map[string]siteLocale `json:"locales"`
	CreatedAt     string                `json:"created_at"`
	SubmittedAt   string                `json:"submitted_at"`
	ReviewedAt    string                `json:"reviewed_at"`
	ReviewNote    string                `json:"review_note"`
	PublishedAt   string                `json:"published_at"`
	URL           string                `json:"url"`
	SiteStatus    string                `json:"site_status"`
	SiteError     string                `json:"site_error"`
}

type siteDocument struct {
	Kind         string        `json:"kind"`
	Title        string        `json:"title"`
	PublicURL    string        `json:"public_url"`
	Draft        *siteRevision `json:"draft"`
	Submitted    *siteRevision `json:"submitted"`
	Published    *siteRevision `json:"published"`
	LastDecision *struct {
		Action     string `json:"action"`
		Note       string `json:"note"`
		ReviewedAt string `json:"reviewed_at"`
	} `json:"last_decision"`
}

type appSite struct {
	AppSlug   string                  `json:"app_slug"`
	BaseURL   string                  `json:"base_url"`
	Limits    siteLimits              `json:"limits"`
	Documents map[string]siteDocument `json:"documents"`
}

// appSiteCommand runs `tringify app privacy|terms|docs push|status`.
func (a *app) appSiteCommand(ctx context.Context, kind string, args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "push":
			return a.appSitePush(ctx, kind, args[1:])
		case "status":
			return a.appSiteStatus(ctx, kind, args[1:])
		}
	}
	return fmt.Errorf("usage: tringify app %s push|status", kind)
}

var siteKindNames = map[string]string{"privacy": "privacy policy", "terms": "terms of service", "docs": "documentation"}

func (a *app) fetchSite(ctx context.Context, appID string) (*appSite, error) {
	var site appSite
	if err := a.mustDevAPIDo(ctx, http.MethodGet, "/apps/"+url.PathEscape(appID)+"/site", nil, &site); err != nil {
		return nil, err
	}
	return &site, nil
}

// mustDevAPIDo calls the Developer API, explaining a refusal from an older login.
func (a *app) mustDevAPIDo(ctx context.Context, method, path string, body, out any) error {
	api, _, err := a.devAPI()
	if err != nil {
		return err
	}
	return appAccess(api.Do(ctx, method, path, body, out))
}

func (a *app) appSitePush(ctx context.Context, kind string, args []string) error {
	name := siteKindNames[kind]
	usage, summary, sourceFlag, sourceDefault, sourceHelp := "app "+kind+" push [--file "+kind+".md] [--submit] [--app ID]",
		"Save a markdown file as the draft of the app's "+name+" on its Tringify support site. With --submit the draft is also submitted for review; once approved, it is published as the new version and stores that installed the app are notified.",
		"file", kind+".md", "markdown file"
	if kind == "docs" {
		usage, summary, sourceFlag, sourceDefault, sourceHelp = "app docs push [--dir docs] [--submit] [--app ID]",
			"Save the docs/*.md files as the draft of the app's documentation on its Tringify support site. Each file is one page: its name without .md is the page's address (lowercase letters, numbers and hyphens), and it starts with front matter giving the title and, optionally, the position:\n\n  ---\n  title: Getting started\n  order: 1\n  ---\n\nPages are ordered by order, then by file name; the first is the docs home. Only the .md files directly in the folder are pushed. With --submit the draft is also submitted for review; once approved, it is published as the new version.",
			"dir", "docs", "folder of markdown pages"
	}
	fs := newFlags(usage, summary)
	source := fs.String(sourceFlag, sourceDefault, sourceHelp)
	submit := fs.Bool("submit", false, "also submit the draft for review")
	appFlag := fs.String("app", "", "app ID (default: app_id in "+appconfig.FileName+")")
	config := fs.String("config", appconfig.FileName, "configuration file")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	appID, err := appTarget(*appFlag, *config)
	if err != nil {
		return err
	}
	site, err := a.fetchSite(ctx, appID)
	if err != nil {
		return err
	}
	limits := site.Limits
	if limits.RequiredLocale == "" {
		return errors.New("the Developer API did not say which language the content is in")
	}
	var content siteLocale
	if kind == "docs" {
		pages, err := readDocsPages(*source, limits)
		if err != nil {
			return err
		}
		content.Pages = pages
	} else {
		raw, err := os.ReadFile(*source)
		if err != nil {
			return err
		}
		content.BodyMarkdown = strings.TrimSpace(string(raw))
		if content.BodyMarkdown == "" {
			return fmt.Errorf("%s is empty", *source)
		}
		if n := utf8.RuneCountInString(content.BodyMarkdown); n > limits.BodyMaxChars {
			return fmt.Errorf("%s has %d characters; the %s can have at most %d", *source, n, name, limits.BodyMaxChars)
		}
		a.warnImages(*source, content.BodyMarkdown, limits.ImageHosts)
	}
	if kind == "docs" {
		for _, page := range content.Pages {
			a.warnImages(filepath.Join(*source, page.Slug+".md"), page.BodyMarkdown, limits.ImageHosts)
		}
	}

	base := "/apps/" + url.PathEscape(appID) + "/site/" + kind
	var doc siteDocument
	body := map[string]any{"locales": map[string]siteLocale{limits.RequiredLocale: content}}
	if err := a.mustDevAPIDo(ctx, http.MethodPut, base+"/draft", body, &doc); err != nil {
		return err
	}
	if doc.Draft == nil {
		return errors.New("the Developer API saved the draft but did not return it")
	}
	a.printf("Saved the draft of the %s.\n", strings.ToLower(doc.Title))
	if !*submit {
		a.printf("Submit it for review: tringify app %s push --submit%s\n", kind, appArg(*appFlag, appID))
		return nil
	}
	draftID := doc.Draft.ID
	if err := a.mustDevAPIDo(ctx, http.MethodPost, base+"/submit", map[string]any{"expected_sha256": doc.Draft.ContentSHA256}, &doc); err != nil {
		return err
	}
	if doc.Published != nil && doc.Published.ID == draftID {
		a.printf("Published version %s: %s\n", versionLabel(doc.Published), doc.PublicURL)
		return nil
	}
	a.printf("Submitted for review. Once approved it is published at %s\nCheck on it with: tringify app %s status%s\n", doc.PublicURL, kind, appArg(*appFlag, appID))
	return nil
}

// readDocsPages reads the pages of a docs folder in their order, checked
// against the server's limits.
func readDocsPages(dir string, limits siteLimits) ([]sitePage, error) {
	slugPattern, err := regexp.Compile(limits.PageSlugPattern)
	if err != nil {
		return nil, fmt.Errorf("the Developer API sent an unreadable page address pattern: %w", err)
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.md"))
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no .md files in %s", dir)
	}
	if len(files) > limits.DocsMaxPages {
		return nil, fmt.Errorf("%s has %d pages; the documentation can have at most %d", dir, len(files), limits.DocsMaxPages)
	}
	type orderedPage struct {
		page  sitePage
		order int
	}
	var pages []orderedPage
	total := 0
	for _, path := range files {
		slug := strings.TrimSuffix(filepath.Base(path), ".md")
		if !slugPattern.MatchString(slug) || utf8.RuneCountInString(slug) > limits.PageSlugMaxChars {
			return nil, fmt.Errorf("%s: the file name without .md is the page's address and must match %s, at most %d characters", path, limits.PageSlugPattern, limits.PageSlugMaxChars)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		title, order, body, err := parseFrontMatter(string(raw))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if n := utf8.RuneCountInString(title); n > limits.PageTitleMaxChars {
			return nil, fmt.Errorf("%s: the title has %d characters; at most %d", path, n, limits.PageTitleMaxChars)
		}
		if body == "" {
			return nil, fmt.Errorf("%s has no content after its front matter", path)
		}
		n := utf8.RuneCountInString(body)
		if n > limits.BodyMaxChars {
			return nil, fmt.Errorf("%s has %d characters; a page can have at most %d", path, n, limits.BodyMaxChars)
		}
		total += n
		pages = append(pages, orderedPage{sitePage{Slug: slug, Title: title, BodyMarkdown: body}, order})
	}
	if total > limits.DocsTotalMaxChars {
		return nil, fmt.Errorf("the pages in %s have %d characters together; the documentation can have at most %d", dir, total, limits.DocsTotalMaxChars)
	}
	sort.SliceStable(pages, func(i, j int) bool {
		if pages[i].order != pages[j].order {
			return pages[i].order < pages[j].order
		}
		return pages[i].page.Slug < pages[j].page.Slug
	})
	out := make([]sitePage, len(pages))
	for i, p := range pages {
		out[i] = p.page
	}
	return out, nil
}

// parseFrontMatter reads a page's front matter: title (required) and order
// (optional integer; pages without one come after those with one).
func parseFrontMatter(text string) (title string, order int, body string, err error) {
	lines := strings.Split(strings.TrimPrefix(text, "\ufeff"), "\n")
	if strings.TrimSpace(lines[0]) != "---" {
		return "", 0, "", errors.New("missing front matter: start the file with ---, a title: line and ---")
	}
	order = int(^uint(0) >> 1)
	for i := 1; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "---" {
			if title == "" {
				return "", 0, "", errors.New("the front matter needs a title")
			}
			return title, order, strings.TrimSpace(strings.Join(lines[i+1:], "\n")), nil
		}
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		key, value, ok := strings.Cut(trimmed, ":")
		if !ok {
			return "", 0, "", fmt.Errorf("front matter line %q is not key: value", trimmed)
		}
		key, value = strings.TrimSpace(key), unquote(strings.TrimSpace(value))
		switch key {
		case "title":
			title = value
		case "order":
			if order, err = strconv.Atoi(value); err != nil {
				return "", 0, "", fmt.Errorf("order must be a whole number, not %q", value)
			}
		default:
			return "", 0, "", fmt.Errorf("unknown front matter key %q (use title and order)", key)
		}
	}
	return "", 0, "", errors.New("the front matter is not closed with ---")
}

func unquote(s string) string {
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	return s
}

var markdownImage = regexp.MustCompile(`!\[[^\]]*\]\(\s*<?([^)\s>]+)`)

// warnImages says which images the support site will show as links because
// they are not on an allowed image host.
func (a *app) warnImages(path, markdown string, hosts []string) {
	for _, m := range markdownImage.FindAllStringSubmatch(markdown, -1) {
		u, err := url.Parse(m[1])
		allowed := false
		if err == nil {
			for _, h := range hosts {
				allowed = allowed || strings.EqualFold(u.Hostname(), h)
			}
		}
		if !allowed {
			fmt.Fprintf(a.stderr, "Warning: %s: the image %s is shown as a link; images must be on %s\n", path, m[1], strings.Join(hosts, ", "))
		}
	}
}

func (a *app) appSiteStatus(ctx context.Context, kind string, args []string) error {
	fs := newFlags("app "+kind+" status [--app ID]", "Show the draft, the submission in review and the published version of the app's "+siteKindNames[kind]+" on its Tringify support site.")
	appFlag := fs.String("app", "", "app ID (default: app_id in "+appconfig.FileName+")")
	config := fs.String("config", appconfig.FileName, "configuration file")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	appID, err := appTarget(*appFlag, *config)
	if err != nil {
		return err
	}
	site, err := a.fetchSite(ctx, appID)
	if err != nil {
		return err
	}
	doc, ok := site.Documents[kind]
	if !ok {
		return fmt.Errorf("the Developer API did not return the %s", siteKindNames[kind])
	}
	a.printf("%s\n", doc.Title)
	a.printf("  %-11s %s\n", "Public URL:", doc.PublicURL)
	if p := doc.Published; p != nil {
		state := p.SiteStatus
		if p.SiteError != "" {
			state += ": " + p.SiteError
		}
		line := fmt.Sprintf("version %s, %s (site: %s)", versionLabel(p), day(p.PublishedAt), state)
		if p.Source == "release" {
			line += ", republished by Tringify for the app's current release"
		}
		a.printf("  %-11s %s\n", "Published:", line)
		if p.URL != "" {
			a.printf("  %-11s %s\n", "", p.URL)
		}
	} else {
		a.printf("  %-11s %s\n", "Published:", "never")
	}
	if s := doc.Submitted; s != nil {
		a.printf("  %-11s %s, in review%s\n", "Submitted:", day(s.SubmittedAt), docsPageCount(s))
	} else {
		a.printf("  %-11s %s\n", "Submitted:", "nothing in review")
	}
	if d := doc.Draft; d != nil {
		a.printf("  %-11s saved %s%s\n", "Draft:", day(d.CreatedAt), docsPageCount(d))
	} else {
		a.printf("  %-11s %s\n", "Draft:", "none")
	}
	if dec := doc.LastDecision; dec != nil {
		a.printf("  %-11s %s on %s", "Review:", dec.Action, day(dec.ReviewedAt))
		if dec.Note != "" {
			a.printf(": %s", dec.Note)
		}
		a.println("")
	}
	return nil
}

func versionLabel(r *siteRevision) string {
	if r.Version == nil {
		return "?"
	}
	return strconv.Itoa(*r.Version)
}

func docsPageCount(r *siteRevision) string {
	for _, l := range r.Locales {
		if len(l.Pages) > 0 {
			return fmt.Sprintf(" (%d pages)", len(l.Pages))
		}
	}
	return ""
}

// day shows an API timestamp as a date in the local time zone.
func day(ts string) string {
	if ts == "" {
		return "—"
	}
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return ts
	}
	return t.Local().Format("2 Jan 2006 15:04")
}
