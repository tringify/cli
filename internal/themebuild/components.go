package themebuild

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Shared storefront components that themes embed in their section sources.
// A section marks the component's place once:
//
//	{%- comment -%}tf:shared NAME{%- endcomment -%}{%- comment -%}/tf:shared NAME{%- endcomment -%}
//
// and SyncComponents replaces whatever sits between the markers with
// components/NAME.html. Edit a component here, never in a theme.
//
//go:embed components/*.html
var components embed.FS

var componentStart = regexp.MustCompile(`\{%- comment -%\}tf:shared ([a-z0-9-]+)\{%- endcomment -%\}`)

// SyncComponents brings every src/sections/*/body.html in root up to date
// with the shared components and returns the sections it changed (or, with
// check, would change). An unknown component name is an error and nothing is
// written.
func SyncComponents(root string, check bool) ([]string, error) {
	files, err := filepath.Glob(filepath.Join(root, "src", "sections", "*", "body.html"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	type update struct{ path, text string }
	var updates []update
	var changed []string
	missing := map[string]bool{}
	for _, path := range files {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		old := string(raw)
		next := replaceComponents(old, missing)
		if next != old {
			changed = append(changed, filepath.Base(filepath.Dir(path)))
			updates = append(updates, update{path, next})
		}
	}
	if len(missing) > 0 {
		names := make([]string, 0, len(missing))
		for name := range missing {
			names = append(names, name)
		}
		sort.Strings(names)
		return nil, fmt.Errorf("unknown component(s): %s", strings.Join(names, ", "))
	}
	if !check {
		for _, u := range updates {
			if err := os.WriteFile(u.path, []byte(u.text), 0o666); err != nil {
				return nil, err
			}
		}
	}
	return changed, nil
}

// replaceComponents fills each marked component with its current source.
func replaceComponents(text string, missing map[string]bool) string {
	var b strings.Builder
	rest := text
	for {
		loc := componentStart.FindStringSubmatchIndex(rest)
		if loc == nil {
			b.WriteString(rest)
			return b.String()
		}
		name := rest[loc[2]:loc[3]]
		end := "{%- comment -%}/tf:shared " + name + "{%- endcomment -%}"
		closeAt := strings.Index(rest[loc[1]:], end)
		if closeAt < 0 {
			// An unclosed marker is left as it is, like any other text.
			b.WriteString(rest[:loc[1]])
			rest = rest[loc[1]:]
			continue
		}
		source, err := components.ReadFile("components/" + name + ".html")
		b.WriteString(rest[:loc[1]])
		if err != nil {
			missing[name] = true
			b.WriteString(rest[loc[1] : loc[1]+closeAt+len(end)])
		} else {
			b.WriteString("\n" + strings.Trim(string(source), "\n") + "\n" + end)
		}
		rest = rest[loc[1]+closeAt+len(end):]
	}
}
