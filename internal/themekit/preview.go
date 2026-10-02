package themekit

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tringify/cli/internal/pyjson"
)

const (
	maxPreviewFiles = 2000
	maxPreviewBytes = 100 * 1024 * 1024
	maxRendered     = 128 * 1024 * 1024
)

// FrameCSP applies to rendered pages: scripts run, but nothing they do can
// reach the network, submit a form or touch the studio.
const FrameCSP = "default-src 'none'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline' https:; " +
	"img-src 'self' data: https:; font-src 'self' https: data:; media-src 'self' https:; " +
	"connect-src 'none'; form-action 'none'; base-uri 'none'; object-src 'none'; " +
	"frame-src 'none'; sandbox allow-scripts"

const studioCSP = "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; frame-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'"

var pageBridge = []byte("<script>parent.postMessage({type:'tringify-preview-page',path:location.pathname},'*')</script>")

// SourceSignature fingerprints the authoring inputs (bundle directories,
// src and the manifest files) without following links or reading anything
// else in the theme directory.
func SourceSignature(root string) (string, error) {
	digest := sha256.New()
	count, size := 0, int64(0)
	var stack []string
	for _, name := range BundleDirectories {
		stack = append(stack, filepath.Join(root, name))
	}
	stack = append(stack, filepath.Join(root, "src"))
	for _, name := range BundleFiles {
		stack = append(stack, filepath.Join(root, name))
	}
	for len(stack) > 0 {
		path := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		info, err := os.Lstat(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			return "", fmt.Errorf("theme source symlinks are not supported: %s", rel(root, path))
		case info.IsDir():
			entries, err := os.ReadDir(path)
			if err != nil {
				return "", err
			}
			names := make([]string, len(entries))
			for i, e := range entries {
				names[i] = e.Name()
			}
			sort.Sort(sort.Reverse(sort.StringSlice(names)))
			for _, n := range names {
				stack = append(stack, filepath.Join(path, n))
			}
			continue
		case !info.Mode().IsRegular():
			return "", fmt.Errorf("unsupported theme source file: %s", rel(root, path))
		}
		if info.Name() == dsStore {
			continue
		}
		count++
		size += info.Size()
		if count > maxPreviewFiles || size > maxPreviewBytes {
			return "", errors.New("preview source exceeds 2,000 files or 100 MiB")
		}
		fmt.Fprintf(digest, "%s\x00%d:%d:%d\x00", filepath.ToSlash(rel(root, path)), info.Size(), info.ModTime().UnixNano(), changeTime(info))
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

type asset struct {
	data []byte
	mime string
}

// Preview holds the latest successfully rendered snapshot of a theme.
type Preview struct {
	Root     string
	Temp     string
	Base     string
	Preset   string
	Tools    Tools
	Out      io.Writer
	Renderer string

	mu        sync.Mutex
	snapshot  map[string]asset
	name      pyjson.Value
	pages     pyjson.Value
	revision  int
	errText   string
	signature string
}

// NewPreview prepares a preview of root. Builds happen in temp.
func NewPreview(root, temp, base, renderer, preset string, tools Tools, out io.Writer) (*Preview, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, err
	}
	if !isDir(resolved) {
		return nil, errors.New("theme root must be a directory")
	}
	return &Preview{
		Root: resolved, Temp: temp, Base: base, Renderer: renderer, Preset: preset, Tools: tools, Out: out,
		snapshot: map[string]asset{}, name: pyjson.String(filepath.Base(resolved)), pages: []pyjson.Value{},
	}, nil
}

// Rebuild renders the theme again if its source changed. A failed build
// keeps the previous snapshot and is retried only when the source changes.
func (p *Preview) Rebuild(ctx context.Context) bool {
	changed, err := p.rebuild(ctx)
	if err == nil {
		return changed
	}
	if ctx.Err() != nil {
		return false
	}
	message := []rune(err.Error())
	if len(message) > 8000 {
		message = message[:8000]
	}
	p.mu.Lock()
	differs := p.errText != string(message)
	p.errText = string(message)
	p.mu.Unlock()
	if differs {
		fmt.Fprintf(p.Out, "Preview needs attention: %s\n", string(message))
	}
	return false
}

func (p *Preview) rebuild(ctx context.Context) (bool, error) {
	signature, err := SourceSignature(p.Root)
	if err != nil {
		return false, err
	}
	if signature == p.signature {
		return false, nil
	}
	p.signature = signature
	work, err := os.MkdirTemp(p.Temp, "build-")
	if err != nil {
		return false, err
	}
	defer os.RemoveAll(work)
	bundle, output := filepath.Join(work, "theme.zip"), filepath.Join(work, "rendered")
	if _, err := Package(ctx, p.Root, bundle, "sealed", p.Tools); err != nil {
		return false, err
	}
	runCtx, cancel := context.WithTimeout(ctx, renderTimeout)
	defer cancel()
	out, err := run(runCtx, p.Renderer, "--bundle", bundle, "--output", output, "--base", p.Base, "--preset", p.Preset)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return false, errors.New("theme rendering timed out after 90 seconds")
		}
		return false, err
	}
	if out.exitCode != 0 {
		return false, renderFailure(out, "theme rendering failed")
	}
	text, err := readText(output, filepath.Join(output, "manifest.json"))
	if err != nil {
		return false, err
	}
	value, err := pyjson.Loads(text)
	if err != nil {
		return false, err
	}
	manifest, ok := value.(*pyjson.Object)
	if !ok {
		return false, errors.New("renderer returned an invalid manifest")
	}
	pagesValue, _ := manifest.Get("pages")
	pages, ok := pagesValue.([]pyjson.Value)
	if !ok || len(pages) == 0 {
		return false, errors.New("renderer returned no preview pages")
	}
	snapshot := map[string]asset{}
	total := 0
	resolvedOutput, err := filepath.EvalSymlinks(output)
	if err != nil {
		return false, err
	}
	for _, pageValue := range pages {
		page, ok := pageValue.(*pyjson.Object)
		if !ok {
			return false, errors.New("renderer returned an invalid preview page")
		}
		routeValue, hasPath := page.Get("path")
		fileValue, hasFile := page.Get("file")
		if !hasPath {
			return false, errors.New("'path'")
		}
		if !hasFile {
			return false, errors.New("'file'")
		}
		route, ok := routeValue.(pyjson.String)
		if !ok || !strings.HasPrefix(string(route), p.Base+"/") || strings.ContainsAny(string(route), "?#") {
			return false, errors.New("renderer returned an invalid preview route")
		}
		filename, ok := fileValue.(pyjson.String)
		if !ok {
			return false, errors.New("renderer returned an invalid page file")
		}
		file := string(filename)
		if !filepath.IsAbs(file) {
			file = filepath.Join(output, file)
		}
		resolved, resolveErr := resolvePath(file)
		if isSymlink(file) || resolveErr != nil || !within(resolved, resolvedOutput) {
			return false, errors.New("renderer returned an invalid page file")
		}
		data, err := os.ReadFile(file)
		if err != nil {
			return false, err
		}
		data = append(data, pageBridge...)
		total += len(data)
		snapshot[string(route)] = asset{data, "text/html; charset=utf-8"}
	}
	for _, folder := range []string{"__asset", "__demo-image"} {
		base := filepath.Join(output, folder)
		if _, err := os.Lstat(base); err != nil {
			continue
		}
		err := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.Type()&fs.ModeSymlink != 0 {
				return errors.New("renderer returned a symlink asset")
			}
			if !d.Type().IsRegular() {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			total += len(data)
			snapshot[p.Base+"/"+filepath.ToSlash(rel(output, path))] = asset{data, mimeType(path)}
			return nil
		})
		if err != nil {
			return false, err
		}
	}
	if total > maxRendered {
		return false, errors.New("rendered preview exceeds 128 MiB")
	}
	name, hasName := manifest.Get("name")
	p.mu.Lock()
	p.snapshot, p.name, p.pages = snapshot, name, pagesValue
	p.revision++
	p.errText = ""
	p.mu.Unlock()
	if !hasName {
		return true, errors.New("'name'")
	}
	fmt.Fprintf(p.Out, "Preview updated: %s · %d pages\n", pyjson.Str(name), len(pages))
	return true, nil
}

// State is what the studio polls: the revision, the last error and the
// pages of the current snapshot.
func (p *Preview) State() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	state := pyjson.NewObject()
	state.Set("revision", pyjson.Int(strconv.Itoa(p.revision)))
	state.Set("error", pyjson.String(p.errText))
	state.Set("name", p.name)
	state.Set("pages", p.pages)
	return pyjson.Compact(state)
}

// Snapshot returns the current snapshot for tests.
func (p *Preview) Snapshot() map[string][]byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := map[string][]byte{}
	for k, v := range p.snapshot {
		out[k] = v.data
	}
	return out
}

// Error returns the current build error, if any.
func (p *Preview) Error() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.errText
}

// Revision returns the number of successful builds.
func (p *Preview) Revision() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.revision
}

var mimeTypes = map[string]string{
	".avif": "image/avif", ".css": "text/css", ".gif": "image/gif", ".htm": "text/html", ".html": "text/html",
	".ico": "image/vnd.microsoft.icon", ".jpeg": "image/jpeg", ".jpg": "image/jpeg", ".js": "text/javascript",
	".json": "application/json", ".mjs": "text/javascript", ".mp3": "audio/mpeg", ".mp4": "video/mp4",
	".otf": "font/otf", ".pdf": "application/pdf", ".png": "image/png", ".svg": "image/svg+xml",
	".ttf": "font/ttf", ".txt": "text/plain", ".wasm": "application/wasm", ".webm": "video/webm",
	".webp": "image/webp", ".woff": "font/woff", ".woff2": "font/woff2", ".xml": "text/xml",
}

func mimeType(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	if t, ok := mimeTypes[ext]; ok {
		return t
	}
	if t := mime.TypeByExtension(ext); t != "" {
		return t
	}
	return "application/octet-stream"
}

// Handler serves the studio, its state and the current snapshot, and
// nothing else. Requests must name an allowed Host, which stops DNS
// rebinding from reading the preview.
func (p *Preview) Handler(allowed func(host string) bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		send := func(status int, data []byte, mimeType string, frame bool) {
			h := w.Header()
			h.Set("Content-Type", mimeType)
			h.Set("Content-Length", strconv.Itoa(len(data)))
			h.Set("Cache-Control", "no-store")
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("Referrer-Policy", "no-referrer")
			if frame {
				h.Set("Content-Security-Policy", FrameCSP)
			} else {
				h.Set("Content-Security-Policy", studioCSP)
			}
			w.WriteHeader(status)
			w.Write(data)
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Connection", "close")
			send(http.StatusNotImplemented, []byte(fmt.Sprintf("Unsupported method (%q)", r.Method)), "text/plain", false)
			return
		}
		if !allowed(r.Host) {
			send(http.StatusForbidden, []byte("Host not allowed"), "text/plain", false)
			return
		}
		switch route := r.URL.Path; route {
		case p.Base + "/studio":
			send(http.StatusOK, []byte(strings.ReplaceAll(studioHTML, "__BASE__", p.Base)), "text/html; charset=utf-8", false)
		case p.Base + "/state":
			send(http.StatusOK, []byte(p.State()), "application/json", false)
		default:
			p.mu.Lock()
			item, ok := p.snapshot[route]
			p.mu.Unlock()
			if !ok {
				send(http.StatusNotFound, []byte("This page is not part of the local preview."), "text/plain", false)
				return
			}
			send(http.StatusOK, item.data, item.mime, true)
		}
	})
}

// CheckHost validates the preview address: a specific IPv4 address.
func CheckHost(host string, port int) error {
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("'%s' does not appear to be an IPv4 or IPv6 address", host)
	}
	if ip.To4() == nil || strings.Contains(host, ":") {
		return errors.New("preview currently requires an IPv4 address")
	}
	if port < 0 || port > 65535 {
		return errors.New("port must be between 0 and 65535")
	}
	if ip.Equal(net.IPv4zero) {
		return errors.New("use the specific LAN IP for remote preview, or 127.0.0.1 for local preview")
	}
	return nil
}

// Serve watches root and serves the preview on host:port until ctx ends.
func Serve(ctx context.Context, root, host string, port int, preset string, tools Tools, out io.Writer) error {
	if err := CheckHost(host, port); err != nil {
		return err
	}
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	renderer, err := tools.Renderer(ctx)
	if err != nil {
		return err
	}
	token := make([]byte, 24)
	if _, err := rand.Read(token); err != nil {
		return err
	}
	base := "/s/" + base64.RawURLEncoding.EncodeToString(token)
	temp, err := os.MkdirTemp("", "tringify-theme-preview-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temp)
	preview, err := NewPreview(root, temp, base, renderer, preset, tools, out)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host).To4()
	listener, err := net.Listen("tcp4", net.JoinHostPort(ip.String(), strconv.Itoa(port)))
	if err != nil {
		return err
	}
	actualPort := listener.Addr().(*net.TCPAddr).Port
	allowedHosts := map[string]bool{fmt.Sprintf("%s:%d", ip, actualPort): true}
	if ip.IsLoopback() {
		allowedHosts[fmt.Sprintf("localhost:%d", actualPort)] = true
	}
	server := &http.Server{
		Handler:           preview.Handler(func(h string) bool { return allowedHosts[h] }),
		ReadHeaderTimeout: 10 * time.Second,
	}
	preview.Rebuild(ctx)
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
			preview.Rebuild(ctx)
		}
	}()
	fmt.Fprintf(out, "Open http://%s:%d%s/studio\n", ip, actualPort, base)
	fmt.Fprintln(out, "Local sample preview. Shopper actions are disabled. Press Ctrl+C to stop.")
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()
	select {
	case <-ctx.Done():
		err = nil
	case err = <-serveErr:
	}
	stop()
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	server.Shutdown(shutdown)
	select {
	case <-watchDone:
	case <-time.After(95 * time.Second):
	}
	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	}
	return err
}
