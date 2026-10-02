// Package themetools runs the Tringify theme tools
// (https://github.com/tringify/theme-tools). An installed tringify-theme
// command is used when present; otherwise the CLI downloads the release for
// this platform, verifies it against the published SHA256SUMS, and keeps it
// in ~/.tringify/theme-tools, the same place the theme tools installer uses.
package themetools

import (
	"archive/zip"
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const repository = "tringify/theme-tools"

// Runner resolves and runs the theme tools.
type Runner struct {
	// Version is a release tag, or "latest".
	Version string
	Home    string
	HTTP    *http.Client
	Stdout  io.Writer
	Stderr  io.Writer
	Print   func(string)
}

func New(stdout, stderr io.Writer, print func(string)) (*Runner, error) {
	home := os.Getenv("TRINGIFY_THEME_TOOLS_HOME")
	if home == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		home = filepath.Join(userHome, ".tringify", "theme-tools")
	}
	version := os.Getenv("TRINGIFY_THEME_TOOLS_VERSION")
	if version == "" {
		version = "latest"
	}
	return &Runner{Version: version, Home: home, HTTP: &http.Client{Timeout: 5 * time.Minute}, Stdout: stdout, Stderr: stderr, Print: print}, nil
}

// command returns the program and leading arguments that run tringify-theme.
func (r *Runner) command(ctx context.Context) ([]string, error) {
	if path, err := exec.LookPath("tringify-theme"); err == nil {
		return []string{path}, nil
	}
	script := filepath.Join(r.Home, "theme.py")
	if _, err := os.Stat(script); err != nil {
		if err := r.install(ctx); err != nil {
			return nil, err
		}
	}
	python, err := findPython()
	if err != nil {
		return nil, err
	}
	return append(python, script), nil
}

// Run runs tringify-theme with args in dir, streaming its output.
func (r *Runner) Run(ctx context.Context, dir string, args ...string) error {
	command, err := r.command(ctx)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, command[0], append(command[1:], args...)...)
	cmd.Dir = dir
	cmd.Stdin = os.Stdin
	cmd.Stdout = r.Stdout
	cmd.Stderr = r.Stderr
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return fmt.Errorf("tringify-theme %s failed", args[0])
		}
		return err
	}
	return nil
}

func findPython() ([]string, error) {
	candidates := [][]string{{"python3"}, {"python3.13"}, {"python3.12"}, {"python3.11"}, {"python3.10"}, {"python"}}
	if runtime.GOOS == "windows" {
		candidates = append([][]string{{"py", "-3"}}, candidates...)
	}
	for _, c := range candidates {
		path, err := exec.LookPath(c[0])
		if err != nil {
			continue
		}
		args := append(append([]string{}, c[1:]...), "-c", "import sys; sys.exit(0 if sys.version_info >= (3, 10) else 1)")
		if exec.Command(path, args...).Run() == nil {
			return append([]string{path}, c[1:]...), nil
		}
	}
	return nil, errors.New("the theme tools need Python 3.10 or newer; install it and try again")
}

func platform() (string, error) {
	switch runtime.GOOS + "-" + runtime.GOARCH {
	case "darwin-arm64", "darwin-amd64", "linux-amd64", "linux-arm64", "windows-amd64":
		return runtime.GOOS + "-" + runtime.GOARCH, nil
	}
	return "", fmt.Errorf("the theme tools are not published for %s/%s", runtime.GOOS, runtime.GOARCH)
}

func (r *Runner) download(ctx context.Context, url string, w io.Writer) error {
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

func (r *Runner) install(ctx context.Context) error {
	plat, err := platform()
	if err != nil {
		return err
	}
	base := "https://github.com/" + repository + "/releases/latest/download"
	if r.Version != "latest" {
		base = "https://github.com/" + repository + "/releases/download/" + r.Version
	}
	archive := "tringify-theme-tools-" + plat + ".zip"
	r.Print(fmt.Sprintf("Downloading the theme tools (%s, %s)…", r.Version, plat))
	work, err := os.MkdirTemp("", "tringify-theme-tools-")
	if err != nil {
		return err
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
	extract := filepath.Join(work, "extract")
	if err := unzip(zipPath, extract); err != nil {
		return err
	}
	source := filepath.Join(extract, "tringify-theme-tools")
	if _, err := os.Stat(filepath.Join(source, "theme.py")); err != nil {
		return errors.New("unexpected theme tools archive layout")
	}
	if err := os.MkdirAll(filepath.Dir(r.Home), 0o755); err != nil {
		return err
	}
	staged := r.Home + ".new"
	_ = os.RemoveAll(staged)
	if err := os.Rename(source, staged); err != nil {
		return err
	}
	_ = os.RemoveAll(r.Home)
	if err := os.Rename(staged, r.Home); err != nil {
		return err
	}
	for _, name := range []string{"themecheck", "theme-preview-render"} {
		_ = os.Chmod(filepath.Join(r.Home, name), 0o755)
	}
	r.Print("Installed the theme tools in " + r.Home)
	return nil
}

// unzip extracts into dest, refusing paths that escape it and links.
func unzip(path, dest string) error {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return err
	}
	defer zr.Close()
	root, err := filepath.Abs(dest)
	if err != nil {
		return err
	}
	for _, f := range zr.File {
		target := filepath.Join(root, filepath.FromSlash(f.Name))
		if target != root && !strings.HasPrefix(target, root+string(os.PathSeparator)) {
			return fmt.Errorf("archive entry %q escapes the destination", f.Name)
		}
		mode := f.Mode()
		if mode&os.ModeSymlink != 0 {
			return fmt.Errorf("archive entry %q is a link", f.Name)
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		perm := os.FileMode(0o644)
		if mode.Perm()&0o111 != 0 {
			perm = 0o755
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
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
	}
	return nil
}
