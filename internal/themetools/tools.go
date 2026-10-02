// Package themetools finds the prebuilt theme validator (themecheck) and
// preview renderer (theme-preview-render) published with the Tringify theme
// tools (https://github.com/tringify/theme-tools).
//
// Each binary is taken from, in order: its command-line option, its
// environment variable (TRINGIFY_THEME_CHECK, TRINGIFY_THEME_PREVIEW), PATH,
// and finally a copy the CLI downloads on first use. The download is the
// release archive for this platform, verified against the release's
// SHA256SUMS, and only the two binaries are kept, in the user cache
// directory (for example ~/Library/Caches/tringify/theme-tools on macOS or
// ~/.cache/tringify/theme-tools on Linux). TRINGIFY_THEME_TOOLS_HOME names a
// directory to use instead, and TRINGIFY_THEME_TOOLS_VERSION pins a release.
package themetools

import (
	"archive/zip"
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/tringify/cli/internal/themekit"
)

const repository = "tringify/theme-tools"

// Base is the GitHub address of the theme tools repository.
var Base = "https://github.com/" + repository

// latestTTL is how long a resolved "latest" release is trusted before the
// CLI asks GitHub again. Offline, the last resolved release keeps working.
const latestTTL = 24 * time.Hour

// Resolver locates, and when needed downloads, the theme tools binaries.
type Resolver struct {
	// Version is a release tag, or "latest".
	Version string
	// Home, when set, is a directory holding the binaries directly
	// (TRINGIFY_THEME_TOOLS_HOME). Otherwise Cache is used.
	Home string
	// Cache holds one directory per release tag.
	Cache string
	// CacheErr explains why no cache directory is available.
	CacheErr error
	HTTP     *http.Client
	// Log receives download progress. It is stderr, so commands that print
	// JSON keep a clean stdout.
	Log io.Writer

	once sync.Mutex
	dir  string
}

// New returns a resolver configured from the environment.
func New(log io.Writer) *Resolver {
	r := &Resolver{
		Version: os.Getenv("TRINGIFY_THEME_TOOLS_VERSION"),
		Home:    os.Getenv("TRINGIFY_THEME_TOOLS_HOME"),
		HTTP:    &http.Client{Timeout: 5 * time.Minute},
		Log:     log,
	}
	if r.Version == "" {
		r.Version = "latest"
	}
	if cache, err := os.UserCacheDir(); err == nil {
		r.Cache = filepath.Join(cache, "tringify", "theme-tools")
	} else {
		r.CacheErr = err
	}
	return r
}

// Tools returns the binaries for one command. A non-empty checker or
// renderer is an explicit path from the command line.
func (r *Resolver) Tools(checker, renderer string) themekit.Tools {
	return themekit.Tools{
		Checker: func(ctx context.Context) (string, error) {
			return r.binary(ctx, checker, "TRINGIFY_THEME_CHECK", "themecheck")
		},
		Renderer: func(ctx context.Context) (string, error) {
			return r.binary(ctx, renderer, "TRINGIFY_THEME_PREVIEW", "theme-preview-render")
		},
	}
}

func exe(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

func (r *Resolver) binary(ctx context.Context, explicit, env, name string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	if v := os.Getenv(env); v != "" {
		return v, nil
	}
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	dir, err := r.Dir(ctx)
	if err != nil {
		return "", fmt.Errorf("%w\nTo use a local %s instead, set %s", err, name, env)
	}
	return filepath.Join(dir, exe(name)), nil
}

func complete(dir string) bool {
	for _, name := range []string{"themecheck", "theme-preview-render"} {
		info, err := os.Stat(filepath.Join(dir, exe(name)))
		if err != nil || !info.Mode().IsRegular() {
			return false
		}
	}
	return true
}

var tagPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]*$`)

// Dir returns the directory holding both binaries, downloading them if
// they are not there yet.
func (r *Resolver) Dir(ctx context.Context) (string, error) {
	r.once.Lock()
	defer r.once.Unlock()
	if r.dir != "" {
		return r.dir, nil
	}
	if r.Version != "latest" && !tagPattern.MatchString(r.Version) {
		return "", fmt.Errorf("TRINGIFY_THEME_TOOLS_VERSION %q is not a release tag", r.Version)
	}
	if r.Home != "" {
		if !complete(r.Home) {
			tag := r.Version
			if tag == "latest" {
				if resolved, err := r.resolveLatest(ctx); err == nil {
					tag = resolved
				}
			}
			if err := r.install(ctx, tag, r.Home); err != nil {
				return "", err
			}
		}
		r.dir = r.Home
		return r.dir, nil
	}
	if r.Cache == "" {
		return "", fmt.Errorf("no cache directory for the theme tools (%v); set TRINGIFY_THEME_TOOLS_HOME to a directory to keep them in", r.CacheErr)
	}
	tag := r.Version
	if tag == "latest" {
		var err error
		if tag, err = r.latestTag(ctx); err != nil {
			return "", err
		}
	}
	dir := filepath.Join(r.Cache, tag)
	if !complete(dir) {
		if err := r.install(ctx, tag, dir); err != nil {
			return "", err
		}
		if r.Version == "latest" {
			r.prune(tag)
		}
	}
	r.dir = dir
	return dir, nil
}

type latestRecord struct {
	Tag     string    `json:"tag"`
	Checked time.Time `json:"checked"`
}

// latestTag resolves "latest" to a release tag, asking GitHub at most once
// a day and falling back to the last answer when offline.
func (r *Resolver) latestTag(ctx context.Context) (string, error) {
	file := filepath.Join(r.Cache, "latest.json")
	var record latestRecord
	if data, err := os.ReadFile(file); err == nil && json.Unmarshal(data, &record) == nil && tagPattern.MatchString(record.Tag) {
		if time.Since(record.Checked) < latestTTL && record.Checked.Before(time.Now().Add(time.Minute)) {
			return record.Tag, nil
		}
	} else {
		record = latestRecord{}
	}
	tag, err := r.resolveLatest(ctx)
	if err != nil {
		if record.Tag != "" && complete(filepath.Join(r.Cache, record.Tag)) {
			return record.Tag, nil
		}
		return "", fmt.Errorf("find the latest theme tools release: %w", err)
	}
	if err := os.MkdirAll(r.Cache, 0o755); err != nil {
		return "", r.cacheError(err)
	}
	data, _ := json.Marshal(latestRecord{Tag: tag, Checked: time.Now().UTC()})
	if tmp, err := os.CreateTemp(r.Cache, ".latest-*"); err == nil {
		_, werr := tmp.Write(data)
		cerr := tmp.Close()
		if werr != nil || cerr != nil || os.Rename(tmp.Name(), file) != nil {
			os.Remove(tmp.Name())
		}
	}
	return tag, nil
}

// resolveLatest reads the tag the releases/latest page redirects to.
func (r *Resolver) resolveLatest(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, Base+"/releases/latest", nil)
	if err != nil {
		return "", err
	}
	client := *r.HTTP
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	resp.Body.Close()
	location := resp.Header.Get("Location")
	if resp.StatusCode < 300 || resp.StatusCode > 399 || !strings.Contains(location, "/releases/tag/") {
		return "", fmt.Errorf("unexpected response from %s/releases/latest (HTTP %d)", Base, resp.StatusCode)
	}
	tag := path.Base(location)
	if !tagPattern.MatchString(tag) {
		return "", fmt.Errorf("unexpected release tag %q", tag)
	}
	return tag, nil
}

func (r *Resolver) cacheError(err error) error {
	return fmt.Errorf("%w\nSet TRINGIFY_THEME_TOOLS_HOME to a directory you can write to, to keep the theme tools there instead", err)
}

// prune removes other releases from the cache once a newer one is in place.
func (r *Resolver) prune(keep string) {
	entries, err := os.ReadDir(r.Cache)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() && e.Name() != keep && tagPattern.MatchString(e.Name()) {
			os.RemoveAll(filepath.Join(r.Cache, e.Name()))
		}
	}
}

func platform() (string, error) {
	switch runtime.GOOS + "-" + runtime.GOARCH {
	case "darwin-arm64", "darwin-amd64", "linux-amd64", "linux-arm64", "windows-amd64":
		return runtime.GOOS + "-" + runtime.GOARCH, nil
	}
	return "", fmt.Errorf("the theme tools are not published for %s/%s", runtime.GOOS, runtime.GOARCH)
}

func (r *Resolver) download(ctx context.Context, url string, w io.Writer) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := r.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %d", url, resp.StatusCode)
	}
	_, err = io.Copy(w, io.LimitReader(resp.Body, 512<<20))
	return err
}

// ExpectedSum finds the checksum for name in a SHA256SUMS file.
func ExpectedSum(sums, name string) (string, error) {
	scanner := bufio.NewScanner(strings.NewReader(sums))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 2 && (fields[1] == name || fields[1] == "*"+name) {
			if len(fields[0]) != 64 {
				return "", fmt.Errorf("malformed checksum for %s", name)
			}
			return strings.ToLower(fields[0]), nil
		}
	}
	return "", fmt.Errorf("no checksum published for %s", name)
}

// install downloads release tag and puts its binaries in dest.
func (r *Resolver) install(ctx context.Context, tag, dest string) error {
	plat, err := platform()
	if err != nil {
		return err
	}
	base := Base + "/releases/latest/download"
	if tag != "latest" {
		base = Base + "/releases/download/" + tag
	}
	archive := "tringify-theme-tools-" + plat + ".zip"
	fmt.Fprintf(r.Log, "Downloading the theme tools (%s, %s)…\n", tag, plat)
	parent := filepath.Dir(dest)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return r.cacheError(err)
	}
	work, err := os.MkdirTemp(parent, ".theme-tools-")
	if err != nil {
		return r.cacheError(err)
	}
	defer os.RemoveAll(work)
	var sums strings.Builder
	if err := r.download(ctx, base+"/SHA256SUMS", &sums); err != nil {
		return err
	}
	expected, err := ExpectedSum(sums.String(), archive)
	if err != nil {
		return err
	}
	zipPath := filepath.Join(work, archive)
	f, err := os.Create(zipPath)
	if err != nil {
		return err
	}
	hash := sha256.New()
	err = r.download(ctx, base+"/"+archive, io.MultiWriter(f, hash))
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if actual := hex.EncodeToString(hash.Sum(nil)); actual != expected {
		return fmt.Errorf("checksum mismatch for %s; refusing to install", archive)
	}
	staged := filepath.Join(work, "tools")
	names := []string{exe("themecheck"), exe("theme-preview-render"), "VERSION", "LICENSE"}
	if err := extract(zipPath, staged, "tringify-theme-tools", names); err != nil {
		return err
	}
	if !complete(staged) {
		return errors.New("unexpected theme tools archive layout")
	}
	os.Remove(zipPath)
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return r.cacheError(err)
	}
	// The renderer goes first and the validator last, so a directory with
	// both binaries is always a finished install.
	for _, name := range []string{"VERSION", "LICENSE", exe("theme-preview-render"), exe("themecheck")} {
		source := filepath.Join(staged, name)
		if _, err := os.Stat(source); err != nil {
			continue
		}
		if err := os.Rename(source, filepath.Join(dest, name)); err != nil {
			return r.cacheError(err)
		}
	}
	fmt.Fprintf(r.Log, "Installed the theme tools in %s\n", dest)
	return nil
}

// extract copies the named files from the archive's top directory into
// dest. Other entries, links and paths outside top are ignored.
func extract(zipPath, dest, top string, names []string) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer zr.Close()
	wanted := map[string]bool{}
	for _, n := range names {
		wanted[top+"/"+n] = true
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	for _, f := range zr.File {
		if !wanted[f.Name] || f.Mode()&os.ModeSymlink != 0 || f.FileInfo().IsDir() {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		perm := os.FileMode(0o644)
		base := path.Base(f.Name)
		if base != "VERSION" && base != "LICENSE" {
			perm = 0o755
		}
		out, err := os.OpenFile(filepath.Join(dest, base), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
		if err != nil {
			rc.Close()
			return err
		}
		_, err = io.Copy(out, io.LimitReader(rc, 512<<20))
		rc.Close()
		if closeErr := out.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return err
		}
		if err := os.Chmod(filepath.Join(dest, base), perm); err != nil {
			return err
		}
	}
	return nil
}
