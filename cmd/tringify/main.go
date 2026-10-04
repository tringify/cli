// Command tringify is the Tringify command-line tool.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/tringify/cli/internal/mcp"
	"github.com/tringify/cli/internal/themetools"
)

// version is set at build time.
var version = "dev"

const usage = `Tringify CLI

Usage:
  tringify <command> [flags]

Account:
  login                 Sign in to a developer organization
  login --store         Sign in to a store (for theme push and theme dev)
  logout                Sign out and revoke access
  whoami                Show who you are signed in as

Themes:
  theme init [DIR]      Create a theme from the starter theme
  theme preview [DIR]   Preview the theme locally with sample content
  theme build [DIR]     Compile src/sections into sections/*.vasc
  theme check [DIR]     Validate the theme
  theme package [DIR]   Write an upload-ready ZIP
  theme context [DIR]   Print the sample data a preview page receives
  theme contract        Print the theme author contract as JSON
  theme push            Add the theme to a store as a new, unpublished theme
  theme dev             Sync your edits into an unpublished theme on a store
  theme pull            Download a store theme or a published version into a new directory
  theme diff            Compare the theme with a published version of your listing
  theme listings        List your organization's theme listings
  theme publish         Publish the theme as a new version of a listing

Apps:
  app list              List your organization's apps
  app init              Create a project for an app from the app starter
  app config pull       Write the app's configuration to tringify.app.json
  app config push       Replace the app's draft configuration with tringify.app.json
  app release           Submit the draft as a new version
  app versions          List the app's versions
  app publish VERSION   Publish an approved version
  app webhook test      Send a test webhook to the app's webhook URL
  app deliveries        List recent webhook deliveries

Development stores:
  store list            List your organization's development stores
  store create          Create a development store

Other:
  version               Print the CLI version

Run "tringify <command> --help" for details.
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	app := newApp(os.Stdout, os.Stderr)
	if err := app.run(ctx, os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(2)
		}
		fmt.Fprintln(os.Stderr, "Error:", friendly(err))
		os.Exit(1)
	}
}

func friendly(err error) string {
	switch {
	case errors.Is(err, mcp.ErrUnauthorized):
		return "your sign-in has expired or was revoked. Run `tringify login` again."
	case errors.Is(err, mcp.ErrInsufficientScope):
		return err.Error()
	}
	return err.Error()
}

type app struct {
	stdin          io.Reader
	stdout, stderr io.Writer
	apiBase        string
	devAPIBase     string
	resolver       *themetools.Resolver
}

func newApp(stdout, stderr io.Writer) *app {
	a := &app{stdin: os.Stdin, stdout: stdout, stderr: stderr, apiBase: "https://api.tringify.com", devAPIBase: "https://api-dev.tringify.com"}
	if v := os.Getenv("TRINGIFY_API_BASE"); v != "" {
		a.apiBase = strings.TrimRight(v, "/")
	}
	if v := os.Getenv("TRINGIFY_DEVELOPER_API_BASE"); v != "" {
		a.devAPIBase = strings.TrimRight(v, "/")
	}
	return a
}

func (a *app) printf(format string, args ...any) { fmt.Fprintf(a.stdout, format, args...) }
func (a *app) println(s string)                  { fmt.Fprintln(a.stdout, s) }

func (a *app) run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		fmt.Fprint(a.stdout, usage)
		return nil
	}
	switch args[0] {
	case "login":
		return a.login(ctx, args[1:])
	case "logout":
		return a.logout(ctx, args[1:])
	case "whoami":
		return a.whoami(ctx, args[1:])
	case "theme":
		return a.theme(ctx, args[1:])
	case "store":
		return a.store(ctx, args[1:])
	case "app":
		return a.appCommand(ctx, args[1:])
	case "version", "--version", "-v":
		a.println("tringify " + version)
		return nil
	case "help", "--help", "-h":
		fmt.Fprint(a.stdout, usage)
		return nil
	}
	return fmt.Errorf("unknown command %q. Run `tringify help`", args[0])
}

func (a *app) theme(ctx context.Context, args []string) error {
	if len(args) == 0 {
		fmt.Fprint(a.stdout, usage)
		return nil
	}
	switch args[0] {
	case "init":
		return a.themeInit(ctx, args[1:])
	case "build":
		return a.themeBuild(ctx, args[1:])
	case "check":
		return a.themeCheck(ctx, args[1:])
	case "preview":
		return a.themePreview(ctx, args[1:])
	case "context":
		return a.themeContext(ctx, args[1:])
	case "contract":
		return a.themeContract(ctx, args[1:])
	case "package":
		return a.themePackage(ctx, args[1:])
	case "push":
		return a.themePush(ctx, args[1:])
	case "dev":
		return a.themeDev(ctx, args[1:])
	case "pull":
		return a.themePull(ctx, args[1:])
	case "diff":
		return a.themeDiff(ctx, args[1:])
	case "listings":
		return a.themeListings(ctx, args[1:])
	case "publish":
		return a.themePublish(ctx, args[1:])
	}
	return fmt.Errorf("unknown theme command %q", args[0])
}

func newFlags(name, summary string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: tringify %s\n\n%s\n", name, summary)
		hasFlags := false
		fs.VisitAll(func(*flag.Flag) { hasFlags = true })
		if hasFlags {
			fmt.Fprintln(fs.Output(), "\nFlags:")
			fs.PrintDefaults()
		}
	}
	return fs
}

// parse accepts flags before or after positional arguments.
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return positional, nil
		}
		positional = append(positional, args[0])
		args = args[1:]
	}
}
