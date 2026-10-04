package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/tringify/cli/internal/devapi"
	"github.com/tringify/cli/internal/theme"
)

// maxPackageBytes caps a downloaded theme package.
const maxPackageBytes = 256 << 20

type listingVersion struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	Status  string `json:"status"`
}

// resolveVersion finds a version of the listing: the one named, or with an
// empty name the newest published version. ok is false when the listing has
// no published version yet.
func resolveVersion(ctx context.Context, api *devapi.Client, listing, name string) (listingVersion, bool, error) {
	// The listing itself first, so another organization's listing or a typo
	// is reported as such rather than as a listing with no versions.
	if err := api.Do(ctx, http.MethodGet, "/org/themes/"+url.PathEscape(listing), nil, nil); err != nil {
		var apiErr *devapi.Error
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
			return listingVersion{}, false, fmt.Errorf("listing %s is not one of your organization's theme listings. See `tringify theme listings`", listing)
		}
		return listingVersion{}, false, err
	}
	var versions []listingVersion
	if err := api.Do(ctx, http.MethodGet, "/org/themes/"+url.PathEscape(listing)+"/versions", nil, &versions); err != nil {
		return listingVersion{}, false, err
	}
	// Versions come newest first.
	for _, v := range versions {
		if name == "" && v.Status == "published" {
			return v, true, nil
		}
		if name != "" && v.Version == name {
			return v, true, nil
		}
	}
	if name != "" {
		return listingVersion{}, false, fmt.Errorf("listing %s has no version %s. List its versions in the Developer Portal", listing, name)
	}
	return listingVersion{}, false, nil
}

// downloadVersion returns the exact package a version was published with.
func downloadVersion(ctx context.Context, api *devapi.Client, listing string, v listingVersion) ([]byte, string, error) {
	raw, header, err := api.Download(ctx, "/org/themes/"+url.PathEscape(listing)+"/versions/"+url.PathEscape(v.ID)+"/package", maxPackageBytes)
	if err != nil {
		return nil, "", err
	}
	_, params, err := mime.ParseMediaType(header.Get("Content-Disposition"))
	if err != nil || params["filename"] == "" {
		return nil, "", errors.New("the Developer API returned the package without a file name")
	}
	return raw, filepath.Base(params["filename"]), nil
}

// pullListingVersion writes a published version's package into a new directory.
func (a *app) pullListingVersion(ctx context.Context, listing, name string, positional []string) error {
	api, _, err := a.devAPI()
	if err != nil {
		return err
	}
	v, ok, err := resolveVersion(ctx, api, listing, name)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("listing %s has no published version yet", listing)
	}
	raw, filename, err := downloadVersion(ctx, api, listing, v)
	if err != nil {
		return err
	}
	if len(positional) > 1 {
		return fmt.Errorf("unexpected argument %q", positional[1])
	}
	dir := strings.TrimSuffix(filename, filepath.Ext(filename))
	if len(positional) == 1 {
		dir = positional[0]
	}
	dest, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	count, err := theme.Unzip(raw, dest)
	if err != nil {
		return err
	}
	a.printf("Downloaded version %s (%d files) into %s.\n", v.Version, count, dest)
	return nil
}

func (a *app) themeDiff(ctx context.Context, args []string) error {
	fs := newFlags("theme diff --listing ID [--version X.Y.Z] [DIR]", "Compare the theme with a published version of your listing: the files publishing would add, change or remove. Without --version it compares with the newest published version.")
	listing := fs.String("listing", "", "theme listing ID (see `tringify theme listings`)")
	versionFlag := fs.String("version", "", "published version to compare with (default: the newest)")
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	if *listing == "" {
		return errors.New("--listing is required")
	}
	root, err := rootArg(positional)
	if err != nil {
		return err
	}
	api, _, err := a.devAPI()
	if err != nil {
		return err
	}
	v, ok, err := resolveVersion(ctx, api, *listing, *versionFlag)
	if err != nil {
		return err
	}
	if !ok {
		a.println("The listing has no published version yet, so every file would be new.")
		return nil
	}
	changes, err := a.compareWithVersion(ctx, api, *listing, v, root)
	if err != nil {
		return err
	}
	a.printChanges(changes, v.Version)
	return nil
}

// compareWithVersion packages the theme and compares it with a published
// version's package.
func (a *app) compareWithVersion(ctx context.Context, api *devapi.Client, listing string, v listingVersion, root string) ([]theme.Change, error) {
	published, _, err := downloadVersion(ctx, api, listing, v)
	if err != nil {
		return nil, err
	}
	bundle, _, cleanup, err := a.packageTemp(ctx, root)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	local, err := os.ReadFile(bundle)
	if err != nil {
		return nil, err
	}
	base, err := theme.PackageFiles(published)
	if err != nil {
		return nil, err
	}
	next, err := theme.PackageFiles(local)
	if err != nil {
		return nil, err
	}
	return theme.Diff(base, next), nil
}

func (a *app) printChanges(changes []theme.Change, version string) {
	if len(changes) == 0 {
		a.printf("No differences from version %s.\n", version)
		return
	}
	counts := map[string]int{}
	for _, c := range changes {
		mark := map[string]string{"added": "+", "changed": "~", "removed": "-"}[c.Kind]
		a.printf("  %s %s\n", mark, c.Path)
		counts[c.Kind]++
	}
	a.printf("Compared with version %s: %d added, %d changed, %d removed.\n", version, counts["added"], counts["changed"], counts["removed"])
}

// confirm asks a yes/no question on a terminal. Without a terminal it
// refuses, so scripts must pass --yes.
func confirm(in io.Reader, out io.Writer, question string) (bool, error) {
	if f, ok := in.(*os.File); ok {
		info, err := f.Stat()
		if err != nil || info.Mode()&os.ModeCharDevice == 0 {
			return false, errors.New("not running in a terminal. Pass --yes to publish without confirming")
		}
	}
	fmt.Fprintf(out, "%s [y/N] ", question)
	answer, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && answer == "" {
		fmt.Fprintln(out)
		return false, errors.New("no answer was given. Pass --yes to publish without confirming")
	}
	answer = strings.ToLower(strings.TrimSpace(answer))
	return answer == "y" || answer == "yes", nil
}
