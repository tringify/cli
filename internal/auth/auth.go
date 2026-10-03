// Package auth signs the CLI in with OAuth 2.1: authorization code with PKCE
// (S256), a loopback redirect on 127.0.0.1 (RFC 8252), resource indicators
// (RFC 8707) and rotating refresh tokens. The CLI is a public client with no
// secret; its client ID is a metadata document hosted by Tringify.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/tringify/cli/internal/credentials"
)

// Scopes requested at sign-in. Each is limited to what your own role allows;
// the consent screen shows exactly what is granted.
var (
	OrganizationScopes = []string{"connection:read", "organization:themes:read", "organization:themes:write", "organization:dev_stores:read", "organization:dev_stores:write"}
	StoreScopes        = []string{"connection:read", "store:online_store.storefronts:read", "store:online_store.storefronts:write"}
)

// ErrSignInRequired means the saved login can no longer be used.
var ErrSignInRequired = errors.New("sign-in required")

// Client talks to the Tringify authorization server.
type Client struct {
	APIBase string
	HTTP    *http.Client
}

func New(apiBase string) *Client {
	return &Client{APIBase: strings.TrimRight(apiBase, "/"), HTTP: &http.Client{Timeout: 30 * time.Second}}
}

func (c *Client) ClientID() string { return c.APIBase + "/oauth/connectors/clients/tringify-cli" }
func (c *Client) issuer() string   { return c.APIBase + "/oauth/connectors" }

// Resource returns the resource indicator for a target kind.
func (c *Client) Resource(kind string) string {
	if kind == "organization" {
		return c.APIBase + "/mcp/developer"
	}
	return c.APIBase + "/mcp/store"
}

// TokenResponse is the token endpoint's reply.
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
	Error        string `json:"error"`
	Description  string `json:"error_description"`
}

// LoginOptions configures an interactive sign-in.
type LoginOptions struct {
	Kind   string // "organization" or "store"
	Scopes []string
	// OpenBrowser opens the authorization URL; the URL is always printed too.
	OpenBrowser func(string) error
	Print       func(string)
	Timeout     time.Duration
}

func randomString(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Challenge derives the S256 PKCE challenge for a verifier.
func Challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// Login runs the browser flow and returns tokens. The loopback listener binds
// to 127.0.0.1 only, accepts one callback whose state matches, and closes.
func (c *Client) Login(ctx context.Context, opts LoginOptions) (*TokenResponse, string, error) {
	verifier, err := randomString(48) // 64 characters
	if err != nil {
		return nil, "", err
	}
	state, err := randomString(24)
	if err != nil {
		return nil, "", err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, "", fmt.Errorf("start the sign-in listener: %w", err)
	}
	defer listener.Close()
	redirectURI := fmt.Sprintf("http://127.0.0.1:%d/callback", listener.Addr().(*net.TCPAddr).Port)
	resource := c.Resource(opts.Kind)
	query := url.Values{
		"client_id":             {c.ClientID()},
		"redirect_uri":          {redirectURI},
		"response_type":         {"code"},
		"scope":                 {strings.Join(opts.Scopes, " ")},
		"resource":              {resource},
		"state":                 {state},
		"code_challenge":        {Challenge(verifier)},
		"code_challenge_method": {"S256"},
	}
	authorizeURL := c.APIBase + "/oauth/connectors/authorize?" + query.Encode()

	type result struct {
		code string
		err  error
	}
	results := make(chan result, 1)
	server := &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/callback" {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		var res result
		switch {
		case q.Get("state") != state:
			res.err = errors.New("the sign-in response did not match this request; try again")
		case q.Get("iss") != "" && q.Get("iss") != c.issuer():
			res.err = errors.New("the sign-in response came from an unexpected issuer")
		case q.Get("error") != "":
			if q.Get("error") == "access_denied" {
				res.err = errors.New("access was not approved")
			} else {
				res.err = fmt.Errorf("sign-in failed: %s", q.Get("error"))
			}
		case q.Get("code") == "":
			res.err = errors.New("the sign-in response had no authorization code")
		default:
			res.code = q.Get("code")
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		message := "You are signed in to the Tringify CLI. You can close this window."
		if res.err != nil {
			message = "Sign-in did not complete: " + res.err.Error()
		}
		fmt.Fprintf(w, "<!doctype html><meta charset=utf-8><title>Tringify CLI</title><body style=\"font-family:system-ui;padding:3rem\"><p>%s</p>", html.EscapeString(message))
		select {
		case results <- res:
		default:
		}
	})}
	go func() { _ = server.Serve(listener) }()
	defer server.Close()

	opts.Print("Opening your browser to sign in. If it does not open, visit:\n\n  " + authorizeURL + "\n")
	if opts.OpenBrowser != nil {
		_ = opts.OpenBrowser(authorizeURL)
	}
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = 5 * time.Minute
	}
	var res result
	select {
	case res = <-results:
	case <-time.After(timeout):
		return nil, "", errors.New("timed out waiting for sign-in")
	case <-ctx.Done():
		return nil, "", ctx.Err()
	}
	if res.err != nil {
		return nil, "", res.err
	}
	tokens, err := c.token(ctx, url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {c.ClientID()},
		"code":          {res.code},
		"redirect_uri":  {redirectURI},
		"code_verifier": {verifier},
		"resource":      {resource},
	})
	return tokens, resource, err
}

func (c *Client) token(ctx context.Context, form url.Values) (*TokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.APIBase+"/oauth/connectors/token", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var out TokenResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("unexpected token response (HTTP %d)", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK || out.AccessToken == "" {
		if out.Error == "invalid_grant" {
			return nil, ErrSignInRequired
		}
		if out.Error != "" {
			return nil, fmt.Errorf("token request failed: %s", out.Error)
		}
		return nil, fmt.Errorf("token request failed (HTTP %d)", resp.StatusCode)
	}
	return &out, nil
}

// Refresh exchanges the refresh token. Refresh tokens rotate: the account's
// new tokens must be saved before the old ones are discarded, and a refresh
// token is never used twice (reuse revokes the whole connection).
func (c *Client) Refresh(ctx context.Context, a *credentials.Account) (*TokenResponse, error) {
	if a.RefreshToken == "" {
		return nil, ErrSignInRequired
	}
	return c.token(ctx, url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {a.ClientID},
		"refresh_token": {a.RefreshToken},
		"resource":      {a.Resource},
	})
}

// Revoke ends the connection on the server.
func (c *Client) Revoke(ctx context.Context, a *credentials.Account) error {
	token := a.RefreshToken
	if token == "" {
		token = a.AccessToken
	}
	if token == "" {
		return nil
	}
	form := url.Values{"client_id": {a.ClientID}, "token": {token}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.APIBase+"/oauth/connectors/revoke", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 500 {
		return fmt.Errorf("revocation failed (HTTP %d)", resp.StatusCode)
	}
	return nil
}

// Apply copies a token response onto an account.
func Apply(a *credentials.Account, t *TokenResponse, now time.Time) {
	a.AccessToken = t.AccessToken
	if t.RefreshToken != "" {
		a.RefreshToken = t.RefreshToken
	}
	a.Scope = t.Scope
	a.ExpiresAt = now.Add(time.Duration(t.ExpiresIn) * time.Second)
}

// Session keeps an account's access token fresh and persists rotations.
type Session struct {
	Auth    *Client
	Store   *credentials.Store
	Account *credentials.Account
	Now     func() time.Time
}

// AccessToken returns a usable access token, refreshing it if it expires
// within a minute.
func (s *Session) AccessToken(ctx context.Context) (string, error) {
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	if s.Account.AccessToken != "" && now().Add(time.Minute).Before(s.Account.ExpiresAt) {
		return s.Account.AccessToken, nil
	}
	return s.ForceRefresh(ctx)
}

// ForceRefresh rotates the tokens now.
func (s *Session) ForceRefresh(ctx context.Context) (string, error) {
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	t, err := s.Auth.Refresh(ctx, s.Account)
	if err != nil {
		return "", err
	}
	Apply(s.Account, t, now())
	if err := s.Store.Save(*s.Account); err != nil {
		return "", err
	}
	return s.Account.AccessToken, nil
}
