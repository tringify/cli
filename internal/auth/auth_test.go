package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tringify/cli/internal/credentials"
)

// The expected value is BASE64URL(SHA256(verifier)) without padding,
// computed independently.
func TestChallengeIsS256(t *testing.T) {
	if got := Challenge("dBjftJeZ4CVP-mJ92K1bU2r4FpXH4E7cXpUXhVvcY9Q"); got != "FzIA6LV18zImcRH5nKJfrJUm9en9AFKLRHdKcH79Ur0" {
		t.Fatalf("challenge = %s", got)
	}
}

// fakeServer is a minimal authorization server: it approves immediately by
// redirecting to the loopback URI, and rotates refresh tokens.
type fakeServer struct {
	mu        sync.Mutex
	verifiers map[string]string // code -> challenge
	refreshes map[string]bool   // refresh token -> used
	forms     []url.Values
	badState  bool
}

func (f *fakeServer) handler(t *testing.T) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/connectors/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("code_challenge_method") != "S256" || q.Get("resource") == "" || !strings.HasPrefix(q.Get("redirect_uri"), "http://127.0.0.1:") {
			http.Error(w, "bad request", 400)
			return
		}
		f.mu.Lock()
		f.verifiers["code-1"] = q.Get("code_challenge")
		f.mu.Unlock()
		state := q.Get("state")
		if f.badState {
			state = "forged"
		}
		w.WriteHeader(204)
		go http.Get(q.Get("redirect_uri") + "?code=code-1&state=" + url.QueryEscape(state))
	})
	mux.HandleFunc("/oauth/connectors/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f.mu.Lock()
		defer f.mu.Unlock()
		f.forms = append(f.forms, r.PostForm)
		w.Header().Set("Content-Type", "application/json")
		switch r.PostForm.Get("grant_type") {
		case "authorization_code":
			if Challenge(r.PostForm.Get("code_verifier")) != f.verifiers[r.PostForm.Get("code")] {
				w.WriteHeader(400)
				w.Write([]byte(`{"error":"invalid_grant"}`))
				return
			}
			f.refreshes["tcor_1"] = false
			w.Write([]byte(`{"access_token":"tcoa_1","token_type":"Bearer","expires_in":900,"refresh_token":"tcor_1","scope":"connection:read"}`))
		case "refresh_token":
			used, ok := f.refreshes[r.PostForm.Get("refresh_token")]
			if !ok || used {
				w.WriteHeader(400)
				w.Write([]byte(`{"error":"invalid_grant"}`))
				return
			}
			f.refreshes[r.PostForm.Get("refresh_token")] = true
			f.refreshes["tcor_2"] = false
			w.Write([]byte(`{"access_token":"tcoa_2","token_type":"Bearer","expires_in":900,"refresh_token":"tcor_2","scope":"connection:read"}`))
		}
	})
	return mux
}

func newFake(t *testing.T) (*fakeServer, *httptest.Server) {
	f := &fakeServer{verifiers: map[string]string{}, refreshes: map[string]bool{}}
	server := httptest.NewServer(f.handler(t))
	t.Cleanup(server.Close)
	return f, server
}

func login(t *testing.T, c *Client) (*TokenResponse, string, error) {
	return c.Login(context.Background(), LoginOptions{Kind: "organization", Scopes: OrganizationScopes, Print: func(string) {}, Timeout: 5 * time.Second,
		OpenBrowser: func(u string) error { go http.Get(u); return nil }})
}

func TestLoginUsesPKCEAndResource(t *testing.T) {
	f, server := newFake(t)
	c := New(server.URL)
	tokens, resource, err := login(t, c)
	if err != nil {
		t.Fatal(err)
	}
	if tokens.AccessToken != "tcoa_1" || tokens.RefreshToken != "tcor_1" || resource != server.URL+"/mcp/developer" {
		t.Fatalf("tokens=%+v resource=%s", tokens, resource)
	}
	form := f.forms[0]
	if form.Get("client_id") != server.URL+"/oauth/connectors/clients/tringify-cli" || form.Get("resource") != resource || len(form.Get("code_verifier")) < 43 || form.Get("client_secret") != "" {
		t.Fatalf("token request = %v", form)
	}
}

func TestLoginRejectsAForgedState(t *testing.T) {
	f, server := newFake(t)
	f.badState = true
	if _, _, err := login(t, New(server.URL)); err == nil || !strings.Contains(err.Error(), "did not match") {
		t.Fatalf("forged state accepted: %v", err)
	}
	if len(f.forms) != 0 {
		t.Fatal("a code from a forged response was exchanged")
	}
}

func TestRefreshRotatesAndPersists(t *testing.T) {
	_, server := newFake(t)
	t.Setenv("TRINGIFY_CONFIG_DIR", t.TempDir())
	t.Setenv("TRINGIFY_CREDENTIALS_STORE", "file")
	store, err := credentials.Open()
	if err != nil {
		t.Fatal(err)
	}
	c := New(server.URL)
	tokens, resource, err := login(t, c)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	account := &credentials.Account{APIBase: server.URL, Resource: resource, ClientID: c.ClientID(), Target: credentials.Target{Kind: "organization", ID: "org-1", Name: "Org"}}
	Apply(account, tokens, now)
	if err := store.Save(*account); err != nil {
		t.Fatal(err)
	}
	// Near expiry the session refreshes once and saves the rotated pair.
	session := &Session{Auth: c, Store: store, Account: account, Now: func() time.Time { return now.Add(14*time.Minute + 30*time.Second) }}
	token, err := session.AccessToken(context.Background())
	if err != nil || token != "tcoa_2" {
		t.Fatalf("refresh: %s %v", token, err)
	}
	saved, err := store.Load(server.URL, "organization", "")
	if err != nil || saved.RefreshToken != "tcor_2" || saved.AccessToken != "tcoa_2" {
		t.Fatalf("rotated tokens not saved: %+v %v", saved, err)
	}
	// The old refresh token is never sent again; reusing it fails.
	stale := *account
	stale.RefreshToken = "tcor_1"
	if _, err := c.Refresh(context.Background(), &stale); err != ErrSignInRequired {
		t.Fatalf("reused refresh token: %v", err)
	}
	info, err := os.Stat(os.Getenv("TRINGIFY_CONFIG_DIR") + "/credentials.json")
	if err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o600) {
		t.Fatalf("credentials file mode: %v %v", info.Mode(), err)
	}
}
