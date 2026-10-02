package themekit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/tringify/cli/internal/pyjson"
)

// renderTimeout bounds one run of the preview renderer.
const renderTimeout = 90 * time.Second

func renderFailure(out output, fallback string) error {
	text := out.stderr
	if text == "" {
		text = out.stdout
	}
	if text = pyStrip(text); text == "" {
		text = fallback
	}
	return errors.New(text)
}

// InspectContext packages the theme and returns the exact sample CTX the
// preview renderer gives one page.
func InspectContext(ctx context.Context, root, page, entity, preset string, tools Tools) (pyjson.Value, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	if !isDir(resolved) {
		return nil, errors.New("theme root must be a directory")
	}
	dir, err := os.MkdirTemp("", "tringify-theme-context-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	bundle := filepath.Join(dir, "theme.zip")
	if _, err := Package(ctx, resolved, bundle, "sealed", tools); err != nil {
		return nil, err
	}
	renderer, err := tools.Renderer(ctx)
	if err != nil {
		return nil, err
	}
	args := []string{"--bundle", bundle, "--context", "--page", page, "--base", "/preview"}
	if entity != "" {
		args = append(args, "--entity", entity)
	}
	if preset != "" {
		args = append(args, "--preset", preset)
	}
	runCtx, cancel := context.WithTimeout(ctx, renderTimeout)
	defer cancel()
	out, err := run(runCtx, renderer, args...)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, errors.New("theme context inspection timed out after 90 seconds")
		}
		return nil, err
	}
	if out.exitCode != 0 {
		return nil, renderFailure(out, "theme context inspection failed")
	}
	value, err := pyjson.Loads(out.stdout)
	if err != nil {
		return nil, errors.New("theme preview renderer returned invalid context JSON")
	}
	payload, ok := value.(*pyjson.Object)
	invalid := errors.New("theme preview renderer returned an invalid context result")
	if !ok {
		return nil, invalid
	}
	version, _ := payload.Get("schema_version")
	gotPage, _ := payload.Get("page")
	ctxValue, _ := payload.Get("context")
	if _, isObj := ctxValue.(*pyjson.Object); !pyjson.IsInt(version, 1) || gotPage != pyjson.String(page) || !isObj {
		return nil, invalid
	}
	return payload, nil
}
