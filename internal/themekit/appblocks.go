package themekit

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/tringify/cli/internal/pyjson"
)

// App blocks are checked, inspected and previewed by the theme tools
// renderer, which carries the app platform's own block rules: the CLI hands
// it the app configuration with each block's source inline.

// BlockProblem is one reason the app platform would refuse a block. Part is
// "declaration" (the block's entry in the configuration) or "source" (its
// .vasc file); Line is 1-based in the source, or 0.
type BlockProblem struct {
	Block   string `json:"block"`
	Part    string `json:"part"`
	Line    int    `json:"line"`
	Message string `json:"message"`
}

// appBlocksRun writes config to a temporary file and runs the renderer on
// it with args.
func appBlocksRun(ctx context.Context, config []byte, tools Tools, what string, args ...string) (string, error) {
	renderer, err := tools.Renderer(ctx)
	if err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp("", "tringify-app-blocks-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	file := filepath.Join(dir, "app.json")
	if err := os.WriteFile(file, config, 0o600); err != nil {
		return "", err
	}
	runCtx, cancel := context.WithTimeout(ctx, renderTimeout)
	defer cancel()
	out, err := run(runCtx, renderer, append([]string{"--app-blocks", file}, args...)...)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return "", errors.New(what + " timed out after 90 seconds")
		}
		return "", err
	}
	if out.exitCode != 0 {
		if strings.Contains(out.stderr, "flag provided but not defined: -app-blocks") {
			return "", errors.New("these theme tools predate app blocks. Unset TRINGIFY_THEME_TOOLS_VERSION or TRINGIFY_THEME_PREVIEW to use the latest release")
		}
		return "", renderFailure(out, what+" failed")
	}
	return out.stdout, nil
}

// CheckAppBlocks checks the blocks in an app configuration (sources
// inline) with the rules the app platform applies when it is saved. It
// returns the number of blocks and the problems found.
func CheckAppBlocks(ctx context.Context, config []byte, tools Tools) (int, []BlockProblem, error) {
	stdout, err := appBlocksRun(ctx, config, tools, "app block check", "--check")
	if err != nil {
		return 0, nil, err
	}
	var result struct {
		SchemaVersion int            `json:"schema_version"`
		Blocks        int            `json:"blocks"`
		Problems      []BlockProblem `json:"problems"`
	}
	if json.Unmarshal([]byte(stdout), &result) != nil || result.SchemaVersion != 1 || result.Problems == nil {
		return 0, nil, errors.New("theme preview renderer returned an invalid app block check")
	}
	return result.Blocks, result.Problems, nil
}

// AppBlockContext returns the sample data each block that lists page
// renders with. With a theme root, the theme's demo content is used;
// otherwise the built-in sample catalog.
func AppBlockContext(ctx context.Context, config []byte, root, page, entity, preset string, tools Tools) (pyjson.Value, error) {
	args := []string{"--context", "--page", page, "--base", "/preview"}
	if entity != "" {
		args = append(args, "--entity", entity)
	}
	if preset != "" {
		args = append(args, "--preset", preset)
	}
	if root != "" {
		dir, err := os.MkdirTemp("", "tringify-app-blocks-theme-")
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(dir)
		bundle := filepath.Join(dir, "theme.zip")
		if _, err := Package(ctx, root, bundle, "sealed", tools); err != nil {
			return nil, err
		}
		args = append(args, "--bundle", bundle)
	}
	stdout, err := appBlocksRun(ctx, config, tools, "app block context", args...)
	if err != nil {
		return nil, err
	}
	value, err := pyjson.Loads(stdout)
	invalid := errors.New("theme preview renderer returned an invalid app block context")
	if err != nil {
		return nil, invalid
	}
	payload, ok := value.(*pyjson.Object)
	if !ok {
		return nil, invalid
	}
	version, _ := payload.Get("schema_version")
	blocks, _ := payload.Get("blocks")
	if _, isList := blocks.([]pyjson.Value); !pyjson.IsInt(version, 1) || !isList {
		return nil, invalid
	}
	return payload, nil
}
