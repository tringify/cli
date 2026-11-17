package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/tringify/cli/internal/auth"
	"github.com/tringify/cli/internal/devapi"
	"github.com/tringify/cli/internal/mcp"
	"github.com/tringify/cli/internal/trustedpublishing"
)

// Trusted publishing: in a GitHub Actions job with `permissions: id-token:
// write` (or with --oidc), the publishing commands below sign in with the
// job's ID token instead of a keychain login. The publishing token lives in
// memory only and may publish one theme listing or app.

// publishingCommands are the commands trusted publishing can run. A
// publishing token is refused on every other Developer API route.
var publishingCommands = map[string]bool{
	"theme publish":     true,
	"theme docs":        true,
	"theme docs push":   true,
	"theme docs status": true,
	"theme update":      true,
	"app config check":  true,
	"app config push":   true,
	"app release":       true,
	"app publish":       true,
	"app docs push":     true,
	"app docs status":   true,
}

const publishingCommandList = "theme publish, theme docs (and theme docs status), theme update, app config check, app config push, app release, app publish, app docs push and app docs status"

// oidcHelp describes --oidc in the publishing commands' help.
const oidcHelp = "use trusted publishing: exchange this GitHub Actions job's ID token for a short-lived publishing token (no login or secret). On by default in a GitHub Actions job that has the id-token: write permission"

// oidcFlag shows --oidc in a publishing command's help. The flag itself is
// read by run before the command parses its flags.
func oidcFlag(fs *flag.FlagSet) { fs.Bool("oidc", false, oidcHelp) }

// commandName is the command args name, such as "theme docs status" or
// "app config push", without flags or positional arguments.
func commandName(args []string) string {
	var words []string
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") || len(words) == 3 {
			break
		}
		words = append(words, arg)
	}
	switch {
	case len(words) == 0:
		return ""
	case len(words) == 1:
		return words[0]
	case words[0] == "theme" && words[1] == "docs" && len(words) == 3 && (words[2] == "push" || words[2] == "status"):
		return strings.Join(words, " ")
	case words[0] == "app" && len(words) == 3:
		switch words[1] {
		case "config", "docs", "privacy", "terms", "api", "webhook", "secret", "block", "listing":
			return strings.Join(words, " ")
		}
	}
	return words[0] + " " + words[1]
}

// takeOIDCFlag removes --oidc from args and reports whether it was given.
func takeOIDCFlag(args []string) ([]string, bool, error) {
	out := make([]string, 0, len(args))
	on := false
	for i, arg := range args {
		if arg == "--" {
			out = append(out, args[i:]...)
			break
		}
		name, value, hasValue := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		if !strings.HasPrefix(arg, "-") || name != "oidc" {
			out = append(out, arg)
			continue
		}
		if !hasValue {
			on = true
			continue
		}
		b, err := strconv.ParseBool(value)
		if err != nil {
			return nil, false, fmt.Errorf("invalid value %q for --oidc", value)
		}
		on = b
	}
	return out, on, nil
}

// trusted reports whether this run uses trusted publishing: --oidc, or a
// GitHub Actions job that can request an ID token.
func (a *app) trusted() (bool, error) {
	if trustedpublishing.Available() {
		return true, nil
	}
	if a.oidc {
		return false, trustedpublishing.ErrNoIDToken
	}
	return false, nil
}

// publishingTokenInUse is set once a command signs in with trusted
// publishing, so error messages do not suggest `tringify login`.
var publishingTokenInUse bool

// publishingSource is the run's trusted publishing token source, created on
// first use. want is the listing or app the command publishes.
func (a *app) publishingSource(want trustedpublishing.Resource) (*trustedpublishing.Source, error) {
	if !publishingCommands[a.command] {
		return nil, fmt.Errorf("`tringify %s` cannot run with trusted publishing: a publishing token can only run %s. Run other commands on your own computer after `tringify login`", a.command, publishingCommandList)
	}
	if a.publisher == nil {
		a.publisher = &trustedpublishing.Source{
			DevAPIBase: a.devAPIBase, PortalBase: a.portalBase, Version: version, Want: want,
			Notify: func(g trustedpublishing.Grant) {
				fmt.Fprintf(a.stderr, "Trusted publishing: this workflow run may publish %s in %s.\n", g.Resource.Describe(), orgLabel(g))
			},
		}
		publishingTokenInUse = true
	} else if want.Kind != "" {
		a.publisher.Want = want
	}
	return a.publisher, nil
}

func orgLabel(g trustedpublishing.Grant) string {
	if g.OrganizationName != "" {
		return g.OrganizationName
	}
	return "organization " + g.OrganizationID
}

// publishTarget names the listing or app the next devAPI call is for, so a
// trusted publishing exchange asks for exactly it.
func (a *app) publishTarget(kind, id string) {
	a.want = trustedpublishing.Resource{Kind: kind, ID: id}
	if a.publisher != nil {
		a.publisher.Want = a.want
	}
}

// devAPI returns a Developer API client: with trusted publishing, one that
// uses a publishing token (session is then nil); otherwise one with the
// saved developer organization login.
func (a *app) devAPI() (*devapi.Client, *auth.Session, error) {
	on, err := a.trusted()
	if err != nil {
		return nil, nil, err
	}
	if on {
		source, err := a.publishingSource(a.want)
		if err != nil {
			return nil, nil, err
		}
		return devapi.New(a.devAPIBase, "", source, version), nil, nil
	}
	session, err := a.session("organization", "")
	if err != nil {
		if trustedpublishing.InGitHubActions() && publishingCommands[a.command] {
			return nil, nil, trustedpublishing.ErrNoIDToken
		}
		return nil, nil, err
	}
	return devapi.New(a.devAPIBase, session.Account.Target.ID, session, version), session, nil
}

// organizationName names the organization a command acts in.
func (a *app) organizationName(ctx context.Context, session *auth.Session) string {
	if session != nil {
		return session.Account.Target.Name
	}
	if a.publisher != nil {
		if g, err := a.publisher.Grant(ctx); err == nil {
			return orgLabel(g)
		}
	}
	return "your organization"
}

// themeListing is the listing a theme publishing command acts on: --listing,
// which trusted publishing does not need since the publishing token names
// its listing.
func (a *app) themeListing(ctx context.Context, listing string) (string, error) {
	on, err := a.trusted()
	if err != nil {
		return "", err
	}
	if !on {
		if listing == "" {
			if trustedpublishing.InGitHubActions() {
				// Trusted publishing would name the listing.
				return "", trustedpublishing.ErrNoIDToken
			}
			return "", errors.New("--listing is required")
		}
		return listing, nil
	}
	a.publishTarget(trustedpublishing.KindThemeListing, listing)
	source, err := a.publishingSource(a.want)
	if err != nil {
		return "", err
	}
	g, err := source.Grant(ctx)
	if err != nil {
		return "", err
	}
	return g.Resource.ID, nil
}

// appPublishTarget prepares the app commands that trusted publishing can
// run: the exchange asks for this app, and the token must be for it.
func (a *app) appPublishTarget(appID string) {
	a.publishTarget(trustedpublishing.KindApp, appID)
}

// trimV accepts a leading "v" on a version, as in a tag name: v1.2.3 is 1.2.3.
func trimV(v string) string {
	v = strings.TrimSpace(v)
	if len(v) > 1 && (v[0] == 'v' || v[0] == 'V') && v[1] >= '0' && v[1] <= '9' {
		return v[1:]
	}
	return v
}

// readNotes returns release notes from --notes or --notes-file (- reads
// standard input).
func (a *app) readNotes(notes, file string) (string, error) {
	if file == "" {
		return notes, nil
	}
	if notes != "" {
		return "", errors.New("pass --notes or --notes-file, not both")
	}
	var raw []byte
	var err error
	if file == "-" {
		raw, err = io.ReadAll(io.LimitReader(a.stdin, 1<<20))
	} else {
		raw, err = os.ReadFile(file)
	}
	if err != nil {
		return "", fmt.Errorf("read the release notes: %w", err)
	}
	return strings.TrimSpace(string(raw)), nil
}

// canAsk reports whether a question can be asked: never with trusted
// publishing or without a terminal.
func (a *app) canAsk() bool {
	if on, _ := a.trusted(); on {
		return false
	}
	return interactive(a.stdin)
}

// friendlyError explains an error for the user.
func (a *app) friendlyError(err error) string {
	if publishingTokenInUse && errors.Is(err, mcp.ErrUnauthorized) {
		return "Tringify refused the publishing token. It lasts 15 minutes and stops working when its trusted publisher is removed; re-run the job, and check the trusted publisher in the Developer Portal (" + a.portalBase + ")."
	}
	return friendly(err)
}
