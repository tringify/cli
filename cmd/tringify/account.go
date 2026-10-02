package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/tringify/cli/internal/auth"
	"github.com/tringify/cli/internal/browser"
	"github.com/tringify/cli/internal/credentials"
	"github.com/tringify/cli/internal/mcp"
)

func (a *app) authClient() *auth.Client { return auth.New(a.apiBase) }

func (a *app) mcpEndpoint(kind string) string { return a.authClient().Resource(kind) }

// signIn runs the browser flow and saves the resulting login.
func (a *app) signIn(ctx context.Context, kind string) (*credentials.Account, error) {
	store, err := credentials.Open()
	if err != nil {
		return nil, err
	}
	client := a.authClient()
	scopes := auth.OrganizationScopes
	if kind == "store" {
		scopes = auth.StoreScopes
	}
	tokens, resource, err := client.Login(ctx, auth.LoginOptions{Kind: kind, Scopes: scopes, OpenBrowser: browser.Open, Print: a.println})
	if err != nil {
		return nil, err
	}
	account := &credentials.Account{APIBase: a.apiBase, Resource: resource, ClientID: client.ClientID()}
	auth.Apply(account, tokens, time.Now())
	// Learn which organization or store was approved.
	session := &auth.Session{Auth: client, Account: account}
	info, err := mcp.New(a.mcpEndpoint(kind), staticTokens{session}, version).Call(ctx, "get_connection_context", nil)
	if err != nil {
		_ = client.Revoke(ctx, account)
		return nil, fmt.Errorf("read the approved connection: %w", err)
	}
	infoMap, _ := info.(map[string]any)
	target, _ := infoMap["target"].(map[string]any)
	account.Target = credentials.Target{Kind: str(target["kind"]), ID: str(target["id"]), Name: str(target["name"])}
	if account.Target.Kind != kind || account.Target.ID == "" {
		_ = client.Revoke(ctx, account)
		return nil, errors.New("the approved connection is not for the expected kind of account")
	}
	// Replace an earlier login for the same target, revoking it.
	if previous, _ := store.Load(a.apiBase, kind, account.Target.ID); previous != nil {
		_ = client.Revoke(ctx, previous)
	}
	if err := store.Save(*account); err != nil {
		_ = client.Revoke(ctx, account)
		return nil, err
	}
	return account, nil
}

// staticTokens serves a fresh, unsaved token pair during sign-in.
type staticTokens struct{ s *auth.Session }

func (t staticTokens) AccessToken(context.Context) (string, error) {
	return t.s.Account.AccessToken, nil
}
func (t staticTokens) ForceRefresh(context.Context) (string, error) {
	return "", mcp.ErrUnauthorized
}

func str(v any) string { s, _ := v.(string); return s }

func (a *app) login(ctx context.Context, args []string) error {
	fs := newFlags("login [--store]", "Sign in with your Tringify account in the browser. Without --store you choose a developer organization; with --store you choose a store.")
	storeLogin := fs.Bool("store", false, "sign in to a store instead of a developer organization")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	kind := "organization"
	if *storeLogin {
		kind = "store"
	}
	account, err := a.signIn(ctx, kind)
	if err != nil {
		return err
	}
	if kind == "store" {
		a.printf("Signed in to store %s (%s).\n", account.Target.Name, account.Target.ID)
	} else {
		a.printf("Signed in to developer organization %s (%s).\n", account.Target.Name, account.Target.ID)
	}
	a.printf("Granted: %s\n", account.Scope)
	return nil
}

// session loads a saved login. For a store, id selects which one.
func (a *app) session(kind, id string) (*auth.Session, error) {
	store, err := credentials.Open()
	if err != nil {
		return nil, err
	}
	if kind == "organization" {
		id = ""
	}
	account, err := store.Load(a.apiBase, kind, id)
	if err != nil {
		return nil, err
	}
	if account == nil {
		if kind == "store" {
			return nil, nil
		}
		return nil, errors.New("you are not signed in. Run `tringify login`")
	}
	return &auth.Session{Auth: a.authClient(), Store: store, Account: account}, nil
}

func (a *app) logout(ctx context.Context, args []string) error {
	fs := newFlags("logout [--store ID | --all]", "Revoke access and remove the saved login. Without flags this signs out of the developer organization.")
	storeID := fs.String("store", "", "sign out of this store")
	all := fs.Bool("all", false, "sign out of every organization and store")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	store, err := credentials.Open()
	if err != nil {
		return err
	}
	var targets []credentials.Account
	accounts, err := store.List(a.apiBase)
	if err != nil {
		return err
	}
	for _, acct := range accounts {
		switch {
		case *all:
			targets = append(targets, acct)
		case *storeID != "" && acct.Target.Kind == "store" && acct.Target.ID == *storeID:
			targets = append(targets, acct)
		case *storeID == "" && acct.Target.Kind == "organization":
			targets = append(targets, acct)
		}
	}
	if len(targets) == 0 {
		a.println("Not signed in.")
		return nil
	}
	client := a.authClient()
	for _, t := range targets {
		full, err := store.Load(a.apiBase, t.Target.Kind, t.Target.ID)
		if err == nil && full != nil {
			if err := client.Revoke(ctx, full); err != nil {
				fmt.Fprintf(a.stderr, "Warning: could not revoke access for %s on the server: %v\n", t.Target.Name, err)
			}
		}
		if err := store.Delete(a.apiBase, t.Target.Kind, t.Target.ID); err != nil {
			return err
		}
		a.printf("Signed out of %s %s.\n", t.Target.Kind, t.Target.Name)
	}
	return nil
}

func (a *app) whoami(ctx context.Context, args []string) error {
	fs := newFlags("whoami", "Show the developer organization and stores you are signed in to, checked against Tringify.")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	store, err := credentials.Open()
	if err != nil {
		return err
	}
	accounts, err := store.List(a.apiBase)
	if err != nil {
		return err
	}
	if len(accounts) == 0 {
		a.println("Not signed in. Run `tringify login`.")
		return nil
	}
	for _, acct := range accounts {
		s, err := a.session(acct.Target.Kind, acct.Target.ID)
		if err != nil || s == nil {
			continue
		}
		result, err := mcp.New(a.mcpEndpoint(acct.Target.Kind), s, version).Call(ctx, "get_connection_context", nil)
		label := "Developer organization"
		if acct.Target.Kind == "store" {
			label = "Store"
		}
		if err != nil {
			a.printf("%s %s (%s): %s\n", label, acct.Target.Name, acct.Target.ID, friendly(err))
			continue
		}
		info, _ := result.(map[string]any)
		scopes := []string{}
		if list, ok := info["scopes"].([]any); ok {
			for _, s := range list {
				scopes = append(scopes, str(s))
			}
		}
		a.printf("%s: %s (%s)\n  Access: %v\n", label, acct.Target.Name, acct.Target.ID, scopes)
	}
	a.printf("Credentials are stored in %s.\n", store.Backend())
	return nil
}
