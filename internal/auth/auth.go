// Package auth signs the CLI in with OAuth 2.1: authorization code with PKCE
// (S256), a loopback redirect on 127.0.0.1 (RFC 8252), resource indicators
// (RFC 8707) and rotating refresh tokens. The CLI is a public client with no
// secret; its client ID is a metadata document hosted by Tringify.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
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
	OrganizationScopes = []string{"connection:read", "organization:themes:read", "organization:themes:write", "organization:dev_stores:read", "organization:dev_stores:write", "organization:apps:read", "organization:apps:write"}
	StoreScopes        = []string{"connection:read", "store:online_store.storefronts:read", "store:online_store.storefronts:write"}
	// SampleContentScopes are asked for only when adding sample products.
	SampleContentScopes = []string{"store:products:write", "store:files:write"}
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
	// Version is shown in the browser page's footer.
	Version string
	Timeout time.Duration
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
				res.err = errors.New("the sign-in service could not finish signing you in")
			}
		case q.Get("code") == "":
			res.err = errors.New("the sign-in response had no authorization code")
		default:
			res.code = q.Get("code")
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		fmt.Fprint(w, callbackPage(res.err, opts.Version))
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

// ForceRefresh rotates the tokens now. With a store, it holds the account's
// refresh lock and re-reads the stored tokens first: when another process has
// already rotated them, it uses that result instead of spending a refresh
// token that is no longer current (which would revoke the connection).
func (s *Session) ForceRefresh(ctx context.Context) (string, error) {
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	if s.Store == nil {
		t, err := s.Auth.Refresh(ctx, s.Account)
		if err != nil {
			return "", err
		}
		Apply(s.Account, t, now())
		return s.Account.AccessToken, nil
	}
	a := s.Account
	var token string
	err := s.Store.WithAccountLock(a.APIBase, a.Target.Kind, a.Target.ID, func() error {
		stored, err := s.Store.Load(a.APIBase, a.Target.Kind, a.Target.ID)
		if err != nil {
			return err
		}
		if stored == nil {
			return ErrSignInRequired
		}
		if stored.RefreshToken != a.RefreshToken && stored.AccessToken != "" && now().Add(time.Minute).Before(stored.ExpiresAt) {
			*s.Account = *stored
			token = stored.AccessToken
			return nil
		}
		t, err := s.Auth.Refresh(ctx, stored)
		if err != nil {
			return err
		}
		Apply(stored, t, now())
		if err := s.Store.Save(*stored); err != nil {
			return err
		}
		*s.Account = *stored
		token = stored.AccessToken
		return nil
	})
	if err != nil {
		return "", err
	}
	return token, nil
}

//go:embed logo-white.webp
var logoWhite []byte

//go:embed logo-black.webp
var logoBlack []byte

// callbackPage is the page the browser shows after sign-in. It mirrors the
// accounts.tringify.com sign-in surface (light and dark, monochrome) and is
// self-contained, so it loads nothing from the network.
func callbackPage(failure error, version string) string {
	title, page, icon := "You're signed in", "Signed in · Tringify CLI", `<path d="M5 12.5l4.5 4.5L19 7.5"/>`
	body := `<p>Return to your terminal to continue. You can close this tab.</p>`
	if failure != nil {
		title, page, icon = "Sign-in didn't complete", "Sign-in failed · Tringify CLI", `<path d="M6.5 6.5l11 11M17.5 6.5l-11 11"/>`
		msg := failure.Error()
		if msg != "" {
			msg = strings.ToUpper(msg[:1]) + msg[1:]
		}
		if !strings.HasSuffix(msg, ".") {
			msg += "."
		}
		body = `<p>` + html.EscapeString(msg) + `</p><p>Return to your terminal and run <code>tringify login</code> again.</p>`
	}
	footer := "Tringify CLI"
	if version != "" && version != "dev" {
		footer += " · v" + strings.TrimPrefix(version, "v")
	}
	black := "data:image/webp;base64," + base64.StdEncoding.EncodeToString(logoBlack)
	white := "data:image/webp;base64," + base64.StdEncoding.EncodeToString(logoWhite)
	return `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta name="color-scheme" content="light dark"><title>` + page + `</title><style>
:root{--page:#f5f5f5;--card:#fff;--border:#e5e5e5;--title:#0a0a0a;--body:#525252;--muted:#737373;--chip:#f5f5f5;--shadow:0 1px 2px rgba(0,0,0,.04),0 8px 24px rgba(0,0,0,.06)}
@media (prefers-color-scheme:dark){:root{--page:#0a0a0a;--card:#171717;--border:#262626;--title:#fafafa;--body:#a3a3a3;--muted:#737373;--chip:#262626;--shadow:none}}
*{box-sizing:border-box}html,body{height:100%}body{margin:0;display:grid;place-items:center;padding:24px 16px;background:var(--page);color:var(--body);font:15px/1.6 -apple-system,BlinkMacSystemFont,"Segoe UI",Inter,Roboto,sans-serif}
main{width:100%;max-width:400px;animation:in .15s ease-out}
.logo{display:block;width:36px;height:36px;margin:0 auto 32px}.logo.dark{display:none}
@media (prefers-color-scheme:dark){.logo.light{display:none}.logo.dark{display:block}}
.card{background:var(--card);border:1px solid var(--border);border-radius:16px;padding:32px;box-shadow:var(--shadow)}
.icon{width:44px;height:44px;border-radius:50%;border:1.5px solid var(--title);display:grid;place-items:center;margin-bottom:20px}
.icon svg{width:22px;height:22px;fill:none;stroke:var(--title);stroke-width:1.75;stroke-linecap:round;stroke-linejoin:round}
h1{margin:0 0 8px;color:var(--title);font-size:22px;line-height:1.3;font-weight:600;letter-spacing:-.01em}
p{margin:0}p+p{margin-top:8px}
code{font:13px ui-monospace,SFMono-Regular,Menlo,monospace;background:var(--chip);color:var(--title);border-radius:6px;padding:2px 6px}
.foot{margin-top:16px;text-align:center;font-size:13px;color:var(--muted)}
@keyframes in{from{opacity:0}to{opacity:1}}@media (prefers-reduced-motion:reduce){main{animation:none}}
</style></head><body><main><img class="logo light" src="` + black + `" alt="Tringify"><img class="logo dark" src="` + white + `" alt="Tringify"><div class="card"><div class="icon"><svg viewBox="0 0 24 24" aria-hidden="true">` + icon + `</svg></div><h1>` + title + `</h1>` + body + `</div><div class="foot">` + html.EscapeString(footer) + `</div></main></body></html>`
}
