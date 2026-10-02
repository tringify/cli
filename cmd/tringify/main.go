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

	"github.com/tringify/cli/internal/mcp"
)

// version is set at build time.
var version = "dev"

const usage = `Tringify CLI

Usage:
  tringify <command> [flags]

Account:
  login                 Sign in to a developer organization
  login --store         Sign in to a store (for theme push)
  logout                Sign out and revoke access
  whoami                Show who you are signed in as

Themes:
  theme init [DIR]      Create a theme from the starter theme
  theme preview [DIR]   Preview the theme locally with sample content
  theme check [DIR]     Validate the theme
  theme package [DIR]   Write an upload-ready ZIP
  theme build|context   Run the matching theme tools command
  theme push            Add the theme to a store as a new, unpublished theme
  theme listings        List your organization's theme listings
  theme publish         Publish the theme as a new version of a listing

Other:
  version               Print the CLI version

Run "tringify <command> --help" for details.
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
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
	stdout, stderr io.Writer
	apiBase        string
	devAPIBase     string
}

func newApp(stdout, stderr io.Writer) *app {
	a := &app{stdout: stdout, stderr: stderr, apiBase: "https://api.tringify.com", devAPIBase: "https://api-dev.tringify.com"}
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
	case "preview", "check", "build", "context", "contract":
		return a.themeTool(ctx, args[0], args[1:])
	case "package":
		return a.themePackage(ctx, args[1:])
	case "push":
		return a.themePush(ctx, args[1:])
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
