// Package theme implements the theme workflow: create from the starter,
// package, push to a store, and publish a version to a listing.
package theme

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// StarterArchive is the starter theme source.
var StarterArchive = "https://codeload.github.com/tringify/theme-starter/tar.gz/refs/heads/main"

// DownloadStarter fetches and unpacks the starter theme into a new temporary
// directory and returns the theme root inside it.
func DownloadStarter(ctx context.Context, client *http.Client) (string, func(), error) {
	work, err := os.MkdirTemp("", "tringify-theme-starter-")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { os.RemoveAll(work) }
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, StarterArchive, nil)
	if err != nil {
		cleanup()
		return "", nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		cleanup()
		return "", nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		cleanup()
		return "", nil, fmt.Errorf("download the starter theme: HTTP %d", resp.StatusCode)
	}
	root, err := untar(resp.Body, work)
	if err != nil {
		cleanup()
		return "", nil, err
	}
	return root, cleanup, nil
}

func untar(r io.Reader, dest string) (string, error) {
	gz, err := gzip.NewReader(io.LimitReader(r, 256<<20))
	if err != nil {
		return "", err
	}
	tr := tar.NewReader(gz)
	root := ""
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", err
		}
		if h.Typeflag == tar.TypeXGlobalHeader {
			continue // GitHub archives start with a pax global header.
		}
		if strings.Contains(h.Name, "..") {
			return "", fmt.Errorf("unsafe path %q in the starter archive", h.Name)
		}
		name := filepath.Clean(filepath.FromSlash(h.Name))
		if name == "." || filepath.IsAbs(name) {
			return "", fmt.Errorf("unsafe path %q in the starter archive", h.Name)
		}
		top := strings.SplitN(name, string(os.PathSeparator), 2)[0]
		if root == "" {
			root = top
		} else if top != root {
			return "", errors.New("unexpected starter archive layout")
		}
		target := filepath.Join(dest, name)
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return "", err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return "", err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
			if err != nil {
				return "", err
			}
			_, err = io.Copy(f, tr)
			if closeErr := f.Close(); err == nil {
				err = closeErr
			}
			if err != nil {
				return "", err
			}
		default:
			// Links and devices are skipped; the theme tools reject them anyway.
		}
	}
	if root == "" {
		return "", errors.New("the starter archive is empty")
	}
	return filepath.Join(dest, root), nil
}

// Name reads the display name from theme.json.
func Name(root string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(root, "theme.json"))
	if err != nil {
		return "", fmt.Errorf("%s is not a theme directory (no theme.json)", root)
	}
	var meta struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &meta); err != nil {
		return "", fmt.Errorf("read theme.json: %w", err)
	}
	return meta.Name, nil
}

var slugUnsafe = regexp.MustCompile(`[^a-z0-9]+`)

// Slug turns a theme name into a file-name-safe slug.
func Slug(name string) string {
	s := strings.Trim(slugUnsafe.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if s == "" {
		return "theme"
	}
	return s
}

// Source describes the git state a bundle is built from.
type Source struct {
	Commit string
	Dirty  bool
}

// GitSource reports the commit at HEAD and whether the work tree has any
// uncommitted or untracked (non-ignored) change under root's repository.
func GitSource(ctx context.Context, root string) (*Source, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return nil, errors.New("git is required to publish, so the version records its source commit")
	}
	run := func(args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = root
		out, err := cmd.Output()
		return strings.TrimSpace(string(out)), err
	}
	if _, err := run("rev-parse", "--is-inside-work-tree"); err != nil {
		return nil, fmt.Errorf("%s is not in a git repository; commit the theme before publishing", root)
	}
	commit, err := run("rev-parse", "HEAD")
	if err != nil || commit == "" {
		return nil, errors.New("the repository has no commits yet; commit the theme before publishing")
	}
	status, err := run("status", "--porcelain")
	if err != nil {
		return nil, err
	}
	return &Source{Commit: commit, Dirty: status != ""}, nil
}

var semverPattern = regexp.MustCompile(`^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(-[0-9A-Za-z.-]+)?$`)

// ValidVersion reports whether v is a semantic version such as 1.2.0.
func ValidVersion(v string) bool { return semverPattern.MatchString(v) }

// Changelog adds the source commit to the release notes.
func Changelog(notes, commit string) string {
	line := "Source commit: " + commit
	notes = strings.TrimSpace(notes)
	if notes == "" {
		return line
	}
	return notes + "\n\n" + line
}

// Unzip writes a theme package into dest, which must not exist yet. Only
// regular files are written; paths that would leave dest are refused.
func Unzip(data []byte, dest string) (int, error) {
	if _, err := os.Lstat(dest); err == nil {
		return 0, fmt.Errorf("%s already exists; choose a new directory", dest)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return 0, fmt.Errorf("the theme package is not a ZIP: %w", err)
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return 0, err
	}
	count := 0
	for _, f := range zr.File {
		if strings.HasSuffix(f.Name, "/") || !f.Mode().IsRegular() {
			continue
		}
		name := filepath.Clean(filepath.FromSlash(f.Name))
		if strings.Contains(f.Name, "..") || name == "." || filepath.IsAbs(name) || strings.HasPrefix(name, string(os.PathSeparator)) {
			os.RemoveAll(dest)
			return 0, fmt.Errorf("unsafe path %q in the theme package", f.Name)
		}
		target := filepath.Join(dest, name)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return 0, err
		}
		rc, err := f.Open()
		if err != nil {
			return 0, err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err != nil {
			rc.Close()
			return 0, err
		}
		_, err = io.Copy(out, io.LimitReader(rc, 64<<20))
		rc.Close()
		if closeErr := out.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return 0, err
		}
		count++
	}
	return count, nil
}
