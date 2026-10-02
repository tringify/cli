package themekit

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/tringify/cli/internal/pyjson"
)

// Modes accepted by themecheck.
var Modes = []string{"sealed", "development"}

// ValidMode reports whether mode is a themecheck mode.
func ValidMode(mode string) bool { return mode == "sealed" || mode == "development" }

type output struct {
	stdout, stderr string
	exitCode       int
}

func run(ctx context.Context, program string, args ...string) (output, error) {
	cmd := exec.CommandContext(ctx, program, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	out := output{
		stdout: pyjson.NormalizeNewlines(stdout.String()),
		stderr: pyjson.NormalizeNewlines(stderr.String()),
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && ctx.Err() == nil {
		out.exitCode = exit.ExitCode()
		if out.exitCode == 0 {
			out.exitCode = 1
		}
		return out, nil
	}
	if ctx.Err() != nil {
		return out, ctx.Err()
	}
	return out, err
}

// CheckArchive runs themecheck on a runtime archive.
func CheckArchive(ctx context.Context, archive, mode string, tools Tools) (string, error) {
	checker, err := tools.Checker(ctx)
	if err != nil {
		return "", err
	}
	out, err := run(ctx, checker, "-json", "-mode", mode, archive)
	if err != nil {
		return "", err
	}
	if out.exitCode != 0 {
		return "", errors.New(CheckerFailure(out.stdout, out.stderr))
	}
	return out.stdout, nil
}

func orFailed(text string) string {
	if text = pyStrip(text); text == "" {
		return "themecheck failed"
	}
	return text
}

// capitalize is Python's str.capitalize().
func capitalize(s string) string {
	r := []rune(s)
	for i := range r {
		if i == 0 {
			r[i] = unicode.ToTitle(r[i])
		} else {
			r[i] = unicode.ToLower(r[i])
		}
	}
	return string(r)
}

// CheckerFailure turns themecheck's JSON result into a readable message
// that names the file and the reason.
func CheckerFailure(stdout, stderr string) string {
	payload, err := pyjson.Loads(stdout)
	if err != nil {
		return orFailed(stdout + stderr)
	}
	obj, ok := payload.(*pyjson.Object)
	if !ok {
		return orFailed(stdout + stderr)
	}
	errValue, _ := obj.Get("error")
	message, ok := errValue.(pyjson.String)
	if !ok {
		return orFailed(stdout + stderr)
	}
	text := string(message)
	if code, _ := obj.Get("code"); pyjson.Truthy(code) {
		text += " (" + pyjson.Str(code) + ")"
	}
	details, _ := obj.Get("details")
	if d, ok := details.(*pyjson.Object); ok {
		for _, key := range d.Keys() {
			value, _ := d.Get(key)
			if value == nil || value == pyjson.String("") {
				continue
			}
			label := capitalize(strings.ReplaceAll(key, "_", " "))
			var shown string
			switch value.(type) {
			case *pyjson.Object, []pyjson.Value:
				shown = pyjson.DumpsUnicode(value)
			default:
				shown = pyjson.Str(value)
			}
			text += "\n  " + label + ": " + shown
		}
	} else if pyjson.Truthy(details) {
		text += "\n  " + pyjson.Str(details)
	}
	return text
}

// Validate checks the theme's compiled files as they are, without
// rebuilding, and returns the names of its compiled sections.
func Validate(ctx context.Context, root, mode string, tools Tools) ([]string, error) {
	root, err := resolvePath(root)
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "tringify-theme-check-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	archive := filepath.Join(dir, "theme.zip")
	if _, err := WriteArchive(root, archive); err != nil {
		return nil, err
	}
	if _, err := CheckArchive(ctx, archive, mode, tools); err != nil {
		return nil, err
	}
	matches, _ := filepath.Glob(filepath.Join(root, "sections", "*"+SourceExt))
	names := []string{}
	for _, m := range matches {
		names = append(names, strings.TrimSuffix(filepath.Base(m), SourceExt))
	}
	sortNames(names)
	return names, nil
}

// AuthorContract returns the theme author contract printed by themecheck.
func AuthorContract(ctx context.Context, tools Tools) (pyjson.Value, error) {
	checker, err := tools.Checker(ctx)
	if err != nil {
		return nil, err
	}
	out, err := run(ctx, checker, "-contract")
	if err != nil {
		return nil, err
	}
	if out.exitCode != 0 {
		return nil, errors.New(orFailed(out.stdout + out.stderr))
	}
	invalid := errors.New("themecheck returned an invalid author contract")
	value, err := pyjson.Loads(out.stdout)
	if err != nil {
		return nil, invalid
	}
	contract, ok := value.(*pyjson.Object)
	if !ok {
		return nil, invalid
	}
	get := func(o *pyjson.Object, key string) pyjson.Value { v, _ := o.Get(key); return v }
	if !pyjson.IsInt(get(contract, "schema_version"), 3) {
		return nil, invalid
	}
	for _, key := range []string{"ctx_needs", "setting_types", "hosted_actions"} {
		if _, ok := get(contract, key).([]pyjson.Value); !ok {
			return nil, invalid
		}
	}
	ctxObj, ok := get(contract, "ctx").(*pyjson.Object)
	if !ok || !pyjson.IsInt(get(ctxObj, "schema_version"), 1) {
		return nil, invalid
	}
	if _, ok := get(ctxObj, "definitions").(*pyjson.Object); !ok {
		return nil, invalid
	}
	roots, ok := get(ctxObj, "roots").([]pyjson.Value)
	if !ok {
		return nil, invalid
	}
	names := []pyjson.Value{}
	for _, r := range roots {
		root, ok := r.(*pyjson.Object)
		if !ok {
			return nil, invalid
		}
		names = append(names, get(root, "name"))
	}
	if !pyjson.Equal(names, get(contract, "ctx_needs")) {
		return nil, invalid
	}
	return contract, nil
}
