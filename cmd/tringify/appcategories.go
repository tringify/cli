package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"

	"github.com/tringify/cli/internal/devapi"
)

// appCategories lists the categories and subcategories an app can be created
// in, from the same API the Developer Portal's create page reads. Tringify's
// own channels (channel_is_internal) are left out: the API refuses them. The
// list is public, so this works without signing in.
func (a *app) appCategories(ctx context.Context, args []string) error {
	fs := newFlags("app categories [--type standard|sales_channel] [--json]",
		"List the category and subcategory IDs to pass to `tringify app create`. A sales channel app's subcategory sets its channel type.")
	appType := fs.String("type", "", "only subcategories for this app type: standard or sales_channel")
	asJSON := fs.Bool("json", false, "print the result as JSON")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	api := devapi.Public(a.devAPIBase, version)
	type category struct {
		ID           string `json:"id"`
		Name         string `json:"name"`
		DisplayOrder int    `json:"display_order"`
	}
	type subcategory struct {
		ID                string `json:"id"`
		CategoryID        string `json:"category_id"`
		Name              string `json:"name"`
		DisplayOrder      int    `json:"display_order"`
		ChannelType       string `json:"channel_type,omitempty"`
		AppType           string `json:"app_type"`
		ChannelIsInternal bool   `json:"channel_is_internal,omitempty"`
	}
	var data struct {
		Categories    []category    `json:"categories"`
		Subcategories []subcategory `json:"subcategories"`
	}
	if err := api.Do(ctx, http.MethodGet, "/apps/categories", nil, &data); err != nil {
		return err
	}
	subs := map[string][]subcategory{}
	for _, s := range data.Subcategories {
		if s.ChannelIsInternal || (*appType != "" && s.AppType != *appType) {
			continue
		}
		subs[s.CategoryID] = append(subs[s.CategoryID], s)
	}
	sort.SliceStable(data.Categories, func(i, j int) bool { return data.Categories[i].DisplayOrder < data.Categories[j].DisplayOrder })
	type out struct {
		ID            string        `json:"id"`
		Name          string        `json:"name"`
		Subcategories []subcategory `json:"subcategories"`
	}
	var list []out
	for _, c := range data.Categories {
		s := subs[c.ID]
		if len(s) == 0 {
			continue
		}
		sort.SliceStable(s, func(i, j int) bool { return s[i].DisplayOrder < s[j].DisplayOrder })
		list = append(list, out{c.ID, c.Name, s})
	}
	if *asJSON {
		if list == nil {
			list = []out{}
		}
		return json.NewEncoder(a.stdout).Encode(list)
	}
	if len(list) == 0 {
		a.printf("No categories for app type %q.\n", *appType)
		return nil
	}
	for i, c := range list {
		if i > 0 {
			a.printf("\n")
		}
		a.printf("%s  (--category %s)\n", c.Name, c.ID)
		for _, s := range c.Subcategories {
			line := fmt.Sprintf("  %-28s --subcategory %s  [%s", s.Name, s.ID, s.AppType)
			if s.ChannelType != "" {
				line += ", channel " + s.ChannelType
			}
			a.printf("%s]\n", line)
		}
	}
	return nil
}
