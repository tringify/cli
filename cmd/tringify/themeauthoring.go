package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/tringify/cli/internal/themebuild"
)

func (a *app) themeComponents(_ context.Context, args []string) error {
	fs := newFlags("theme components [DIR] [--check]", "Copy the shared storefront components (date picker, account tabs) into the section sources that mark them with {%- comment -%}tf:shared NAME{%- endcomment -%}{%- comment -%}/tf:shared NAME{%- endcomment -%}. Run theme build afterwards. --check changes nothing and fails when a section is out of date.")
	check := fs.Bool("check", false, "change nothing; fail when a section is out of date")
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	root, err := rootArg(positional)
	if err != nil {
		return err
	}
	changed, err := themebuild.SyncComponents(root, *check)
	if err != nil {
		return fmt.Errorf("theme components failed: %w", err)
	}
	if *check {
		if len(changed) > 0 {
			return fmt.Errorf("out of date: %s (run tringify theme components)", strings.Join(changed, ", "))
		}
		a.println("components are up to date")
		return nil
	}
	if len(changed) == 0 {
		a.println("components are up to date")
		return nil
	}
	a.printf("updated %d sections: %s\n", len(changed), strings.Join(changed, ", "))
	return nil
}

func (a *app) themeReference(_ context.Context, args []string) error {
	fs := newFlags("theme reference [DIR]", "Write the theme's reference pages (docs/ref-*.md: theme settings, styles, page templates, sections and blocks) from the built theme and docs-src/descriptions.json, for tringify theme docs push. Run theme build first. Every other .md file in docs/ is left alone. Fails when a section, block or style has no description.")
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	root, err := rootArg(positional)
	if err != nil {
		return err
	}
	result, err := themebuild.GenerateReference(root)
	if err != nil {
		return fmt.Errorf("theme reference failed: %w", err)
	}
	total := 0
	for _, p := range result.Pages {
		a.printf("%s: %d chars\n", p.Slug, p.Chars)
		total += p.Chars
	}
	a.printf("total %d chars\n", total)
	if len(result.Missing) > 0 {
		return fmt.Errorf("missing descriptions in docs-src/descriptions.json: %s", strings.Join(result.Missing, ", "))
	}
	return nil
}
