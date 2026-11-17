// Package trustedpublishing signs the CLI in from a GitHub Actions workflow
// without a stored secret: it asks GitHub Actions for an OIDC ID token with
// the audience "tringify" and exchanges it at the Developer API for a
// short-lived publishing token (ttpa_…). The publishing token is kept in
// memory only, never in the keychain or on disk, and is never printed. It can
// only publish the one theme listing or app whose trusted publisher matched
// the workflow run.
package trustedpublishing

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// Audience is the audience of the ID token. It is not configurable.
const Audience = "tringify"

const (
	envRequestURL   = "ACTIONS_ID_TOKEN_REQUEST_URL"
	envRequestToken = "ACTIONS_ID_TOKEN_REQUEST_TOKEN"
)

// Resource kinds a trusted publisher can be on.
const (
	KindThemeListing = "theme_listing"
	KindApp          = "app"
)

// ErrNoIDToken means the job cannot request an ID token.
var ErrNoIDToken = errors.New("GitHub Actions did not give this job an ID token. Add `permissions: id-token: write` to the job that runs tringify.")

// Available reports whether GitHub Actions gave this job an ID token
// (the job has `permissions: id-token: write`).
func Available() bool {
	return os.Getenv(envRequestURL) != "" && os.Getenv(envRequestToken) != ""
}

// InGitHubActions reports whether the CLI runs in a GitHub Actions job.
func InGitHubActions() bool { return os.Getenv("GITHUB_ACTIONS") == "true" }

// Resource is a theme listing or app.
type Resource struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Describe names a resource for messages: `theme listing Haul (uuid)`.
func (r Resource) Describe() string {
	label := KindLabel(r.Kind)
	switch {
	case r.Name != "" && r.ID != "":
		return fmt.Sprintf("%s %s (%s)", label, r.Name, r.ID)
	case r.ID != "":
		return label + " " + r.ID
	}
	return label
}

// KindLabel is the resource kind in words.
func KindLabel(kind string) string {
	switch kind {
	case KindThemeListing:
		return "theme listing"
	case KindApp:
		return "app"
	}
	return kind
}

// Grant is what an exchange returns, without the token.
type Grant struct {
	OrganizationID   string
	OrganizationName string
	Resource         Resource
	Scopes           []string
	PublisherID      string
	ExpiresAt        time.Time
}

// Source exchanges GitHub Actions ID tokens for publishing tokens. It is an
// mcp.TokenSource: AccessToken exchanges on first use and again shortly
// before the publishing token expires, ForceRefresh (after a 401) exchanges
// at once. Every exchange uses a fresh ID token.
type Source struct {
	// DevAPIBase is the Developer API, for example https://api-dev.tringify.com.
	DevAPIBase string
	// PortalBase is the Developer Portal, named in messages.
	PortalBase string
	Version    string
	// Want is the listing or app the command publishes. Kind is checked
	// against the publishing token; ID, when set, is sent with the exchange
	// to choose among several matching publishers.
	Want Resource
	// Notify, when set, is told about each successful exchange.
	Notify func(Grant)
	HTTP   *http.Client
	Now    func() time.Time

	mu      sync.Mutex
	token   string
	grant   Grant
	granted bool
}

func (s *Source) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Source) client() *http.Client {
	if s.HTTP != nil {
		return s.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// AccessToken returns the publishing token, exchanging a fresh ID token when
// there is none yet or it expires within a minute.
func (s *Source) AccessToken(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.token != "" && s.now().Add(time.Minute).Before(s.grant.ExpiresAt) {
		return s.token, nil
	}
	return s.exchange(ctx)
}

// ForceRefresh exchanges a fresh ID token now.
func (s *Source) ForceRefresh(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exchange(ctx)
}

// OrganizationID is the organization the publishing token acts in. The
// Developer API client sends it as X-Organization-ID.
func (s *Source) OrganizationID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.grant.OrganizationID
}

// Grant exchanges if needed and returns what the publishing token allows.
func (s *Source) Grant(ctx context.Context) (Grant, error) {
	if _, err := s.AccessToken(ctx); err != nil {
		return Grant{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.grant, nil
}

func (s *Source) exchange(ctx context.Context) (string, error) {
	idToken, err := requestIDToken(ctx, s.client())
	if err != nil {
		return "", err
	}
	body := map[string]string{"token": idToken}
	if s.Want.Kind != "" && s.Want.ID != "" {
		body["resource_kind"], body["resource_id"] = s.Want.Kind, s.Want.ID
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	endpoint := strings.TrimRight(s.DevAPIBase, "/") + "/api/v1/trusted-publishing/token"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "tringify-cli/"+s.Version)
	resp, err := s.client().Do(req)
	if err != nil {
		return "", fmt.Errorf("trusted publishing: could not reach the Tringify Developer API: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode >= 300 {
		return "", s.exchangeError(resp.StatusCode, raw)
	}
	var envelope struct {
		Success *bool           `json:"success"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return "", fmt.Errorf("trusted publishing: the Developer API returned an unreadable reply (HTTP %d)", resp.StatusCode)
	}
	if envelope.Success != nil && !*envelope.Success {
		return "", s.exchangeError(resp.StatusCode, raw)
	}
	var data struct {
		AccessToken      string   `json:"access_token"`
		ExpiresIn        int      `json:"expires_in"`
		OrganizationID   string   `json:"organization_id"`
		OrganizationName string   `json:"organization_name"`
		Resource         Resource `json:"resource"`
		Scopes           []string `json:"scopes"`
		PublisherID      string   `json:"publisher_id"`
	}
	if err := json.Unmarshal(envelope.Data, &data); err != nil || data.AccessToken == "" || data.OrganizationID == "" {
		return "", errors.New("trusted publishing: the Developer API's reply had no publishing token or organization")
	}
	if s.Want.Kind != "" && data.Resource.Kind != s.Want.Kind {
		return "", fmt.Errorf("the trusted publisher that matched this workflow run is on %s, but this command publishes %s. Use this workflow for the %s commands, or add a trusted publisher for it on the %s in the Developer Portal (%s)",
			data.Resource.Describe(), withArticle(KindLabel(s.Want.Kind)), KindLabel(data.Resource.Kind), KindLabel(s.Want.Kind), s.PortalBase)
	}
	if s.Want.ID != "" && !strings.EqualFold(data.Resource.ID, s.Want.ID) {
		return "", fmt.Errorf("this workflow run may publish %s, not %s %s. Pass that ID, or leave the flag out under trusted publishing", data.Resource.Describe(), KindLabel(s.Want.Kind), s.Want.ID)
	}
	expiresIn := data.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 900
	}
	first := !s.granted
	s.token = data.AccessToken
	s.grant = Grant{
		OrganizationID: data.OrganizationID, OrganizationName: data.OrganizationName,
		Resource: data.Resource, Scopes: data.Scopes, PublisherID: data.PublisherID,
		ExpiresAt: s.now().Add(time.Duration(expiresIn) * time.Second),
	}
	s.granted = true
	if s.Notify != nil && first {
		s.Notify(s.grant)
	}
	return s.token, nil
}

// requestIDToken asks GitHub Actions for an ID token with the audience
// "tringify".
func requestIDToken(ctx context.Context, client *http.Client) (string, error) {
	base, bearer := os.Getenv(envRequestURL), os.Getenv(envRequestToken)
	if base == "" || bearer == "" {
		return "", ErrNoIDToken
	}
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+sep+"audience="+url.QueryEscape(Audience), nil)
	if err != nil {
		return "", fmt.Errorf("trusted publishing: %s is not a URL: %w", envRequestURL, err)
	}
	req.Header.Set("Authorization", "bearer "+bearer)
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("trusted publishing: could not request an ID token from GitHub Actions: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("trusted publishing: GitHub Actions refused to issue an ID token (HTTP %d). The job needs `permissions: id-token: write`", resp.StatusCode)
	}
	var out struct {
		Value string `json:"value"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil || out.Value == "" {
		return "", errors.New("trusted publishing: GitHub Actions returned no ID token")
	}
	return out.Value, nil
}

// Error is a refused exchange, with a message that says what to change.
type Error struct {
	Status  int
	Code    string
	Message string // the API's own message
	text    string
}

func (e *Error) Error() string { return e.text }

func (s *Source) exchangeError(status int, raw []byte) error {
	var envelope struct {
		Code    string          `json:"code"`
		Message string          `json:"message"`
		Details json.RawMessage `json:"details"`
		Error   *struct {
			Code    string          `json:"code"`
			Message string          `json:"message"`
			Details json.RawMessage `json:"details"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &envelope)
	code, message, details := envelope.Code, envelope.Message, envelope.Details
	if envelope.Error != nil {
		code, message, details = envelope.Error.Code, envelope.Error.Message, envelope.Error.Details
	}
	e := &Error{Status: status, Code: code, Message: message}
	e.text = s.describe(status, code, message, details)
	return e
}

func (s *Source) describe(status int, code, message string, rawDetails json.RawMessage) string {
	var details map[string]any
	if len(rawDetails) > 0 {
		dec := json.NewDecoder(bytes.NewReader(rawDetails))
		dec.UseNumber()
		_ = dec.Decode(&details)
	}
	portal := s.PortalBase
	if portal == "" {
		portal = "the Developer Portal"
	}
	run := runContext()
	switch code {
	case "TRUSTED_PUBLISHING_NO_MATCH":
		var b strings.Builder
		b.WriteString("trusted publishing: no trusted publisher matches this workflow run (TRUSTED_PUBLISHING_NO_MATCH). GitHub reported:\n")
		repo := detail(details, "repository")
		if id := detail(details, "repository_id"); id != "" {
			repo += " (repository_id " + id + ")"
		}
		env := detail(details, "environment")
		if env == "" {
			env = "none (the job has no `environment:`)"
		}
		for _, line := range [][2]string{
			{"repository", repo},
			{"workflow", detail(details, "workflow_path")},
			{"ref", detail(details, "ref")},
			{"environment", env},
		} {
			fmt.Fprintf(&b, "  %-12s %s\n", line[0]+":", line[1])
		}
		what := "theme listing or app"
		if s.Want.Kind != "" {
			what = KindLabel(s.Want.Kind)
		}
		target := "the " + what
		if s.Want.ID != "" {
			target += " " + s.Want.ID
		}
		fmt.Fprintf(&b, "Add a trusted publisher with exactly these values on %s in the Developer Portal (%s: open the %s, then Trusted publishing). A publisher that names an environment matches only jobs in that environment, and the tag must match its ref pattern (default refs/tags/v*).", target, portal, what)
		return b.String()
	case "TRUSTED_PUBLISHING_EVENT_REFUSED":
		event := run.event
		if event == "" {
			event = "another event"
		}
		return "trusted publishing only works in a workflow run started by pushing a tag; this run was started by " + event + " (TRUSTED_PUBLISHING_EVENT_REFUSED). Pull requests, workflow_dispatch, workflow_run, schedules and other events cannot publish. Trigger the workflow with\n  on:\n    push:\n      tags: [\"v*\"]\nand push a tag such as v1.2.3."
	case "TRUSTED_PUBLISHING_REF_REFUSED":
		ref := run.ref
		if ref == "" {
			ref = "not a tag"
		}
		return "trusted publishing only works for a tag push (refs/tags/…); this run's ref is " + ref + " (TRUSTED_PUBLISHING_REF_REFUSED). Branch pushes cannot publish. Trigger the workflow with `on: push: tags: [\"v*\"]` and push a tag such as v1.2.3."
	case "TRUSTED_PUBLISHING_WORKFLOW_REFUSED":
		msg := "trusted publishing: the publishing step must run in the trusted workflow itself, not in a reusable workflow it calls with `uses:` (TRUSTED_PUBLISHING_WORKFLOW_REFUSED). Move the tringify steps into the workflow file named on the trusted publisher (for example .github/workflows/release.yml)."
		if run.workflowRef != "" {
			msg += " This run's workflow is " + run.workflowRef + "."
		}
		return msg
	case "TRUSTED_PUBLISHING_AMBIGUOUS":
		var b strings.Builder
		b.WriteString("trusted publishing: several trusted publishers match this workflow run (TRUSTED_PUBLISHING_AMBIGUOUS):\n")
		var lines []string
		if list, ok := details["resources"].([]any); ok {
			for _, item := range list {
				if m, ok := item.(map[string]any); ok {
					lines = append(lines, "  "+Resource{Kind: fmt.Sprint(m["kind"]), ID: fmt.Sprint(m["id"])}.Describe())
				}
			}
		}
		sort.Strings(lines)
		for _, l := range lines {
			b.WriteString(l + "\n")
		}
		b.WriteString("Name the one to publish: pass --listing ID to the theme commands, or --app ID (or app_id in tringify.app.json) to the app commands.")
		return b.String()
	case "TRUSTED_PUBLISHING_INVALID_TOKEN":
		return "trusted publishing: Tringify did not accept GitHub's ID token: " + orDefault(message, "it failed a check") + " (TRUSTED_PUBLISHING_INVALID_TOKEN). Re-run the job; if it keeps failing, check that the runner's clock is correct and that nothing changes the ID token request."
	case "TRUSTED_PUBLISHING_INVALID_REQUEST":
		return "trusted publishing: the Developer API refused the exchange request: " + orDefault(message, "invalid request") + " (TRUSTED_PUBLISHING_INVALID_REQUEST). Check --listing/--app, or update the Tringify CLI."
	case "TRUSTED_PUBLISHING_REPLAYED":
		return "trusted publishing: this ID token was already used (TRUSTED_PUBLISHING_REPLAYED). Every exchange needs a fresh ID token; re-run the job."
	case "TRUSTED_PUBLISHING_MEMBER_INACTIVE":
		return "trusted publishing: the organization member who added this trusted publisher is no longer an active member with the permission to publish (TRUSTED_PUBLISHING_MEMBER_INACTIVE). Someone who has the permission must remove the trusted publisher and add it again in the Developer Portal (" + portal + ")."
	case "TRUSTED_PUBLISHING_UNAVAILABLE":
		return "trusted publishing: Tringify could not fetch GitHub's signing keys to check the ID token (TRUSTED_PUBLISHING_UNAVAILABLE). Re-run the job in a few minutes."
	case "RATE_LIMITED":
		return "trusted publishing: too many token exchanges from this repository or runner (RATE_LIMITED). Wait a few minutes, then re-run the job."
	}
	if status == http.StatusNotFound && code == "" {
		return "trusted publishing: this Developer API (" + s.DevAPIBase + ") does not offer trusted publishing (HTTP 404)"
	}
	text := orDefault(message, fmt.Sprintf("the token exchange failed (HTTP %d)", status))
	if code != "" {
		text += " (" + code + ")"
	}
	return "trusted publishing: " + text
}

func withArticle(s string) string {
	if s != "" && strings.ContainsRune("aeiou", rune(s[0])) {
		return "an " + s
	}
	return "a " + s
}

func detail(details map[string]any, key string) string {
	v, ok := details[key]
	if !ok || v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

func orDefault(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return strings.TrimRight(s, ".")
}

type runInfo struct{ event, ref, workflowRef string }

// runContext is what the GitHub Actions environment says about this run,
// for messages.
func runContext() runInfo {
	return runInfo{event: os.Getenv("GITHUB_EVENT_NAME"), ref: os.Getenv("GITHUB_REF"), workflowRef: os.Getenv("GITHUB_WORKFLOW_REF")}
}
