package themebuild

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/tringify/cli/internal/themejson"
)

// Reference documentation: GenerateReference writes a theme's reference
// pages (docs/ref-*.md) from its built files and the hand-written
// descriptions in docs-src/descriptions.json, ready for `tringify theme docs
// push`. Every section, block, setting and style is listed, so the reference
// is complete by construction; a section, block or style without a
// description is reported, so nothing is published half-described. Every
// other .md file in docs/ is a hand-written guide and is left alone.

// ReferencePage is one generated page.
type ReferencePage struct {
	Slug  string
	Chars int // characters (code points) of the page body
}

// ReferenceResult is what GenerateReference wrote.
type ReferenceResult struct {
	Pages   []ReferencePage
	Missing []string // kind.name of every missing description, sorted
}

var schemaBlock = regexp.MustCompile(`(?s)\{%-?\s*schema\s*-?%\}(.*?)\{%-?\s*endschema`)

type refTheme struct {
	root      string
	manifest  *themejson.Object
	loc       *themejson.Object
	desc      *themejson.Object
	strings   *themejson.Object
	sections  *themejson.Object // name -> schema
	blocks    *themejson.Object // file name -> schema
	settings  []themejson.Value
	presets   *themejson.Object // file name -> preset
	templates *themejson.Object // file name -> template
	schemes   map[string]string
	missing   []string
}

func readRefJSON(path string) (themejson.Value, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	v, err := themejson.Loads(themejson.NormalizeNewlines(string(raw)))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return v, nil
}

func readRefObject(path string) (*themejson.Object, error) {
	v, err := readRefJSON(path)
	if err != nil {
		return nil, err
	}
	o, ok := v.(*themejson.Object)
	if !ok {
		return nil, fmt.Errorf("%s: expected a JSON object", path)
	}
	return o, nil
}

func refSchema(path string) (*themejson.Object, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	m := schemaBlock.FindStringSubmatch(themejson.NormalizeNewlines(string(raw)))
	if m == nil {
		return nil, nil
	}
	v, err := themejson.Loads(m[1])
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	o, _ := v.(*themejson.Object)
	return o, nil
}

func sortedGlob(pattern string) []string {
	files, _ := filepath.Glob(pattern)
	sort.Strings(files)
	return files
}

func stem(path string) string {
	base := filepath.Base(path)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

func loadRefTheme(root string) (*refTheme, error) {
	t := &refTheme{root: root, sections: themejson.NewObject(), blocks: themejson.NewObject(),
		presets: themejson.NewObject(), templates: themejson.NewObject(), schemes: map[string]string{}}
	var err error
	if t.manifest, err = readRefObject(filepath.Join(root, "theme.json")); err != nil {
		return nil, err
	}
	if t.loc, err = readRefObject(filepath.Join(root, "locales", "en.schema.json")); err != nil {
		return nil, err
	}
	if t.desc, err = readRefObject(filepath.Join(root, "docs-src", "descriptions.json")); err != nil {
		return nil, err
	}
	for _, f := range sortedGlob(filepath.Join(root, "sections", "*.vasc")) {
		sc, err := refSchema(f)
		if err != nil {
			return nil, err
		}
		if sc != nil && sc.Len() > 0 {
			name := stem(f)
			if n, ok := sc.Get("name"); ok && themejson.Truthy(n) {
				name = themejson.Str(n)
			}
			t.sections.Set(name, sc)
		}
	}
	for _, f := range sortedGlob(filepath.Join(root, "blocks", "*.vasc")) {
		sc, err := refSchema(f)
		if err != nil {
			return nil, err
		}
		if sc != nil && sc.Len() > 0 {
			t.blocks.Set(stem(f), sc)
		}
	}
	schema, err := readRefObject(filepath.Join(root, "config", "settings_schema.json"))
	if err != nil {
		return nil, err
	}
	settings, _ := schema.Get("settings")
	t.settings = list(settings)
	for _, f := range sortedGlob(filepath.Join(root, "config", "presets", "*.json")) {
		v, err := readRefJSON(f)
		if err != nil {
			return nil, err
		}
		t.presets.Set(stem(f), v)
	}
	for _, f := range sortedGlob(filepath.Join(root, "templates", "*.json")) {
		v, err := readRefJSON(f)
		if err != nil {
			return nil, err
		}
		t.templates.Set(stem(f), v)
	}
	if s, err := readRefObject(filepath.Join(root, "locales", "en.json")); err == nil {
		t.strings = s
	} else if errors.Is(err, os.ErrNotExist) {
		t.strings = themejson.NewObject()
	} else {
		return nil, err
	}
	for _, st := range t.settings {
		if str(get(st, "type")) != "color_scheme_group" {
			continue
		}
		for _, sch := range list(get(st, "default")) {
			id := themejson.Str(get(sch, "id"))
			if l := get(sch, "label"); themejson.Truthy(l) {
				t.schemes[id] = t.label(l, "fieldLabel")
			} else {
				t.schemes[id] = id
			}
		}
	}
	return t, nil
}

// get is dict.get for a JSON object (nil when v is not an object).
func get(v themejson.Value, key string) themejson.Value {
	if o, ok := v.(*themejson.Object); ok {
		if x, ok := o.Get(key); ok {
			return x
		}
	}
	return nil
}

func has(v themejson.Value, key string) bool {
	if o, ok := v.(*themejson.Object); ok {
		_, ok := o.Get(key)
		return ok
	}
	return false
}

func list(v themejson.Value) []themejson.Value {
	l, _ := v.([]themejson.Value)
	return l
}

// getOr is dict.get(key, def): def only when the key is absent.
func getOr(v themejson.Value, key string, def themejson.Value) themejson.Value {
	if has(v, key) {
		return get(v, key)
	}
	return def
}

// str is the text of a JSON string ("" for anything else).
func str(v themejson.Value) string {
	s, _ := v.(themejson.String)
	return string(s)
}

// or is Python's `a or b`.
func or(a, b themejson.Value) themejson.Value {
	if themejson.Truthy(a) {
		return a
	}
	return b
}

func (t *refTheme) label(key themejson.Value, kind string) string {
	if key == nil {
		return ""
	}
	k := themejson.Str(key)
	if v, ok := t.loc.Get(kind + "." + k); ok && v != nil {
		return themejson.Str(v)
	}
	return capitalize(strings.ReplaceAll(strings.ReplaceAll(k, "opt_", ""), "_", " "))
}

func (t *refTheme) sectionName(name string) string {
	if v, ok := t.loc.Get("sectionName." + name); ok && themejson.Truthy(v) {
		return themejson.Str(v)
	}
	return capitalize(strings.ReplaceAll(name, "-", " "))
}

func (t *refTheme) description(kind, name string, optional bool) string {
	d := get(get(t.desc, kind), name)
	if !themejson.Truthy(d) {
		if !optional {
			t.missing = append(t.missing, kind+"."+name)
		}
		return ""
	}
	return themejson.Str(d)
}

func esc(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "|", "\\|"), "\n", " ")
}

func (t *refTheme) fmtDefault(st themejson.Value) string {
	if !has(st, "default") {
		return ""
	}
	d := get(st, "default")
	typ := str(get(st, "type"))
	if (typ == "select" || typ == "radio") && themejson.Truthy(get(st, "options")) {
		for _, o := range list(get(st, "options")) {
			if themejson.Equal(get(o, "value"), d) {
				return t.label(get(o, "label"), "fieldLabel")
			}
		}
	}
	if b, ok := d.(bool); ok {
		if b {
			return "On"
		}
		return "Off"
	}
	if s, ok := d.(themejson.String); ok && strings.HasPrefix(string(s), "@t:") {
		key := string(s)[3:]
		var val themejson.Value
		if x, ok := t.strings.Get(key); ok && x != nil {
			val = x
		} else {
			var node themejson.Value = t.strings
			for _, part := range strings.Split(key, ".") {
				node = get(node, part)
			}
			if ns, ok := node.(themejson.String); ok {
				val = ns
			}
		}
		if themejson.Truthy(val) {
			return "“" + themejson.Str(val) + "”"
		}
		return "the theme's standard text"
	}
	if s, ok := d.(themejson.String); ok && typ == "font_picker" {
		words := strings.Split(string(s), "-")
		for i, w := range words {
			words[i] = capitalize(w)
		}
		return strings.Join(words, " ")
	}
	if s, ok := d.(themejson.String); ok && typ == "color_scheme" {
		if name, ok := t.schemes[string(s)]; ok {
			return name
		}
		return string(s)
	}
	switch x := d.(type) {
	case nil:
		return ""
	case themejson.String:
		if x == "" {
			return ""
		}
	case []themejson.Value, *themejson.Object:
		return ""
	}
	return themejson.Str(d) + themejson.Str(getOr(st, "unit", themejson.String("")))
}

var typeNames = map[string]string{
	"select": "Choice", "radio": "Choice", "text": "Text", "textarea": "Text (several lines)",
	"richtext": "Formatted text", "checkbox": "On/off", "range": "Slider", "color_scheme": "Colour scheme",
	"color": "Colour", "image": "Image", "video": "Video", "url": "Link", "menu": "Menu", "page": "Page",
	"product": "Product", "product_list": "Products", "collection": "Collection", "collection_list": "Collections",
	"font_picker": "Font", "number": "Number", "color_scheme_group": "Colour schemes", "blog": "Blog",
	"article": "Blog post", "html": "Code", "header": "Heading", "paragraph": "Note",
}

// settingCells are the four cells of a settings table row (label, type,
// default, details), already escaped.
func (t *refTheme) settingCells(st themejson.Value, byID *themejson.Object) [4]string {
	label := t.label(or(get(get(st, "editor"), "label"), get(st, "id")), "fieldLabel")
	typ := str(get(st, "type"))
	kind, ok := typeNames[typ]
	if !ok {
		kind = capitalize(strings.ReplaceAll(typ, "_", " "))
	}
	var detail []string
	if opts := get(st, "options"); themejson.Truthy(opts) {
		names := []string{}
		for _, o := range list(opts) {
			names = append(names, t.label(get(o, "label"), "fieldLabel"))
		}
		detail = append(detail, "Options: "+strings.Join(names, ", "))
	}
	if typ == "range" && has(st, "min") {
		unit := themejson.Str(getOr(st, "unit", themejson.String("")))
		step := getOr(st, "step", themejson.Int("1"))
		detail = append(detail, fmt.Sprintf("%s–%s%s, steps of %s", themejson.Str(get(st, "min")), themejson.Str(get(st, "max")), unit, themejson.Str(step)))
	}
	if typ == "color_scheme_group" {
		names := []string{}
		for _, f := range list(get(st, "fields")) {
			names = append(names, t.label(get(f, "label"), "fieldLabel"))
		}
		detail = append(detail, "Each scheme sets: "+strings.Join(names, ", "))
	}
	if vi := get(st, "visible_if"); themejson.Truthy(vi) {
		setting := get(vi, "setting")
		var other themejson.Value
		if setting != nil {
			other = get(byID, themejson.Str(setting))
		}
		var olabel string
		if themejson.Truthy(other) {
			olabel = t.label(get(get(other, "editor"), "label"), "fieldLabel")
		} else {
			olabel = themejson.Str(setting)
		}
		vals := get(vi, "values")
		if themejson.Truthy(other) && themejson.Truthy(get(other, "options")) && themejson.Truthy(vals) {
			mapped := []themejson.Value{}
			for _, v := range list(vals) {
				name := themejson.Value(nil)
				found := false
				for _, o := range list(get(other, "options")) {
					if themejson.Equal(get(o, "value"), v) {
						name, found = themejson.String(t.label(get(o, "label"), "fieldLabel")), true
					}
				}
				if found {
					mapped = append(mapped, name)
				} else {
					mapped = append(mapped, v)
				}
			}
			vals = mapped
		}
		if vals == nil || themejson.Equal(vals, []themejson.Value{true}) {
			detail = append(detail, "Shown when "+olabel+" is on")
		} else {
			parts := []string{}
			for _, v := range list(vals) {
				parts = append(parts, themejson.Str(v))
			}
			detail = append(detail, "Shown when "+olabel+" is "+strings.Join(parts, " or "))
		}
	}
	return [4]string{esc(label), kind, esc(t.fmtDefault(st)), esc(strings.Join(detail, ". "))}
}

func (t *refTheme) settingRow(st themejson.Value, byID *themejson.Object) string {
	c := t.settingCells(st, byID)
	return "| " + c[0] + " | " + c[1] + " | " + c[2] + " | " + c[3] + " |"
}

func byIDOf(settings []themejson.Value) *themejson.Object {
	o := themejson.NewObject()
	for _, s := range settings {
		if has(s, "id") {
			o.Set(themejson.Str(get(s, "id")), s)
		}
	}
	return o
}

func (t *refTheme) settingsTable(settings []themejson.Value, grouped bool) string {
	if len(settings) == 0 {
		return "_No settings._\n"
	}
	byID := byIDOf(settings)
	var order []string
	groups := map[string][]themejson.Value{}
	for _, st := range settings {
		if !has(st, "id") {
			continue
		}
		g := themejson.Str(or(get(get(st, "editor"), "group"), themejson.String("")))
		if !has(get(st, "editor"), "group") {
			g = ""
		}
		if _, ok := groups[g]; !ok {
			order = append(order, g)
		}
		groups[g] = append(groups[g], st)
	}
	var out []string
	for _, g := range order {
		if grouped && g != "" {
			out = append(out, "**"+t.label(themejson.String(g), "fieldGroup")+"**\n")
		}
		out = append(out, "| Setting | Type | Default | Details |\n|---|---|---|---|")
		for _, st := range groups[g] {
			out = append(out, t.settingRow(st, byID))
		}
		out = append(out, "")
	}
	return strings.Join(out, "\n") + "\n"
}

func front(title string, order int) string {
	return fmt.Sprintf("---\ntitle: %s\norder: %d\n---\n\n", title, order)
}

func templateSections(tpl themejson.Value) []themejson.Value {
	secs := or(get(tpl, "sections"), []themejson.Value{})
	if o, ok := secs.(*themejson.Object); ok {
		var order []string
		if ord := get(tpl, "order"); themejson.Truthy(ord) {
			for _, k := range list(ord) {
				order = append(order, themejson.Str(k))
			}
		} else {
			order = o.Keys()
		}
		var out []themejson.Value
		for _, k := range order {
			if v, ok := o.Get(k); ok {
				out = append(out, v)
			}
		}
		return out
	}
	out := append([]themejson.Value(nil), list(secs)...)
	sort.SliceStable(out, func(i, j int) bool {
		return refNumber(or(get(out[i], "position"), themejson.Int("0"))) < refNumber(or(get(out[j], "position"), themejson.Int("0")))
	})
	return out
}

func refNumber(v themejson.Value) float64 {
	var f float64
	switch x := v.(type) {
	case themejson.Int:
		fmt.Sscan(string(x), &f)
	case themejson.Float:
		f = float64(x)
	case bool:
		if x {
			f = 1
		}
	}
	return f
}

func (t *refTheme) whereUsed() map[string]map[string]bool {
	used := map[string]map[string]bool{}
	for _, name := range t.templates.Keys() {
		tpl, _ := t.templates.Get(name)
		for _, s := range templateSections(tpl) {
			typ := themejson.Str(get(s, "type"))
			if used[typ] == nil {
				used[typ] = map[string]bool{}
			}
			used[typ][name] = true
		}
	}
	return used
}

var templateNames = map[string]string{
	"home": "Home page", "product": "Product page", "collection": "Collection page", "collection_list": "All collections",
	"cart": "Cart page", "search": "Search results", "page": "Page", "blog": "Blog post", "blog_list": "Blog",
	"blog_category": "Blog category", "account": "Customer account", "login": "Sign in", "register": "Create account",
	"not_found": "Page not found (404)", "password": "Password page", "policy": "Policy pages", "shop": "Shop (all products)",
	"brand": "Brand page", "brand_list": "All brands", "vendor": "Vendor page", "vendor_list": "All vendors",
	"category": "Category page", "category_list": "All categories", "gift_card": "Gift card", "wishlist": "Wishlist",
	"track_order": "Track an order", "coming_soon": "Coming soon", "maintenance": "Maintenance", "unavailable": "Store unavailable",
	"global_body_start": "Header group (top of every page)", "global_body_end": "Footer group (bottom of every page)",
	"global_overlay": "Overlays (popups, drawers and banners on every page)",
}

func templateTitle(name string) string {
	if v, ok := templateNames[name]; ok {
		return v
	}
	if base, alt, ok := strings.Cut(name, "__"); ok {
		b := base
		if v, ok := templateNames[base]; ok {
			b = v
		}
		alt = strings.ReplaceAll(strings.ReplaceAll(alt, "-", " "), "_", " ")
		return b + ": " + alt + " (alternate template)"
	}
	return capitalize(strings.ReplaceAll(name, "_", " "))
}

func (t *refTheme) blocksText(sc themejson.Value) string {
	b := get(sc, "blocks")
	if !themejson.Truthy(b) {
		return ""
	}
	var out []string
	var types []themejson.Value
	if _, ok := b.(*themejson.Object); ok {
		types = list(or(get(b, "types"), []themejson.Value{}))
		if !has(b, "types") {
			types = nil
		}
	} else {
		types = list(b)
	}
	if len(types) > 0 {
		out = append(out, "**Blocks**\n")
		for _, bt := range types {
			bl := t.label(or(or(get(get(bt, "editor"), "label"), get(bt, "name")), get(bt, "type")), "fieldLabel")
			lim := ""
			if l := get(bt, "limit"); themejson.Truthy(l) {
				lim = " (at most " + themejson.Str(l) + ")"
			}
			line := "- **" + bl + "**" + lim
			if themejson.Truthy(get(bt, "settings")) {
				line += ":"
			}
			out = append(out, line)
			if settings := list(get(bt, "settings")); len(settings) > 0 {
				byID := byIDOf(settings)
				for _, st := range settings {
					if !has(st, "id") {
						continue
					}
					c := t.settingCells(st, byID)
					var extra []string
					for _, x := range []string{c[1], func() string {
						if c[2] != "" {
							return "default " + c[2]
						}
						return ""
					}(), c[3]} {
						if x != "" {
							extra = append(extra, x)
						}
					}
					out = append(out, "  - "+c[0]+" ("+strings.Join(extra, ", ")+")")
				}
			}
		}
		out = append(out, "")
	}
	if _, ok := b.(*themejson.Object); ok {
		var notes []string
		if m := get(b, "max"); themejson.Truthy(m) {
			notes = append(notes, "Up to "+themejson.Str(m)+" blocks.")
		}
		if themejson.Truthy(get(b, "accept_theme_blocks")) || themejson.Truthy(get(b, "theme_blocks")) {
			var names []string
			for _, tb := range list(or(get(b, "theme_blocks"), []themejson.Value{})) {
				name := themejson.Str(tb)
				if blk, ok := t.blocks.Get(name); ok {
					names = append(names, t.label(or(get(get(blk, "editor"), "label"), tb), "fieldLabel"))
				} else {
					names = append(names, name)
				}
			}
			note := "Also takes the shared theme blocks"
			if len(names) > 0 {
				note += ": " + strings.Join(names, ", ")
			}
			notes = append(notes, note+" (see Blocks).")
		}
		if themejson.Truthy(get(b, "accept_app_blocks")) {
			notes = append(notes, "Takes app blocks from installed apps.")
		}
		if defaults := get(b, "default"); themejson.Truthy(defaults) {
			var names []string
			for _, d := range list(defaults) {
				var chosen themejson.Value = d
				for _, ty := range types {
					if themejson.Equal(get(ty, "type"), d) {
						chosen = get(get(ty, "editor"), "label")
						break
					}
				}
				names = append(names, t.label(chosen, "fieldLabel"))
			}
			notes = append(notes, "Starts with: "+strings.Join(names, ", ")+".")
		}
		if len(notes) > 0 {
			out = append(out, strings.Join(notes, " ")+"\n")
		}
	}
	return strings.Join(out, "\n") + "\n"
}

func (t *refTheme) sectionDoc(name string, sc themejson.Value, used map[string]map[string]bool) string {
	out := []string{"## " + t.sectionName(name) + "\n"}
	if d := t.description("sections", name, false); d != "" {
		out = append(out, d+"\n")
	}
	var places []string
	for p := range used[name] {
		places = append(places, p)
	}
	sort.Strings(places)
	if len(places) > 0 {
		titles := make([]string, len(places))
		for i, p := range places {
			titles[i] = templateTitle(p)
		}
		out = append(out, "Used on: "+strings.Join(titles, ", ")+".\n")
	}
	out = append(out, t.settingsTable(list(get(sc, "settings")), true))
	out = append(out, t.blocksText(sc))
	return strings.Join(out, "\n")
}

func groupOf(name string, sc themejson.Value, chrome map[string]bool) string {
	kind := str(get(sc, "kind"))
	switch {
	case kind == "embed" || strings.HasSuffix(name, "cart-drawer"):
		return "overlay"
	case kind == "layout" || chrome[name]:
		return "layout"
	case strings.HasPrefix(name, "main-") || strings.Contains(name, "-main-") || strings.HasPrefix(name, "main_"):
		return "page"
	case !themejson.Truthy(get(sc, "presets")):
		// a section merchants cannot add (no presets) is a page type's own section
		return "page"
	}
	return "content"
}

// GenerateReference writes the reference pages into root/docs and returns
// them with any missing descriptions. The pages are written even when
// descriptions are missing, so the author can see where they go.
func GenerateReference(root string) (*ReferenceResult, error) {
	t, err := loadRefTheme(root)
	if err != nil {
		return nil, err
	}
	docs := filepath.Join(root, "docs")
	if err := os.MkdirAll(docs, 0o777); err != nil {
		return nil, err
	}
	for _, f := range sortedGlob(filepath.Join(docs, "ref-*.md")) {
		if err := os.Remove(f); err != nil {
			return nil, err
		}
	}
	used := t.whereUsed()
	name := themejson.Str(getOr(t.manifest, "name", themejson.String("")))
	type page struct{ slug, body string }
	var pages []page

	// Theme settings
	out := []string{front("Theme settings", 60)}
	out = append(out, "Theme settings apply to the whole store. Open them in the theme editor under **Theme settings**. "+
		"Every setting "+name+" has is listed here, grouped as in the editor.\n")
	out = append(out, t.settingsTable(t.settings, true))
	pages = append(pages, page{"ref-theme-settings", strings.Join(out, "\n")})

	// Styles
	out = []string{front("Styles", 61)}
	out = append(out, "A style is a complete starting point: colour schemes, fonts, buttons, cards and the demo content "+
		"of its home page. Pick one when you add "+name+"; you can change every setting afterwards.\n")
	base := themejson.NewObject()
	for _, s := range t.settings {
		if has(s, "id") {
			base.Set(themejson.Str(get(s, "id")), get(s, "default"))
		}
	}
	byID := byIDOf(t.settings)
	if t.presets.Len() == 0 {
		out = append(out, name+" comes as one style: its look comes from the theme settings. "+
			"Change colours, fonts and cards in Theme settings.\n")
	} else {
		out = append(out, "## Default\n")
		d := t.description("styles", "Default", true)
		if d == "" {
			d = name + " as it comes: every setting at its default. It is the **Default** choice when you add " + name + "."
		}
		out = append(out, d, "")
	}
	for _, pname := range t.presets.Keys() {
		p, _ := t.presets.Get(pname)
		title := themejson.Str(getOr(p, "name", themejson.String(pname)))
		out = append(out, "## "+title+"\n")
		if d := t.description("styles", title, false); d != "" {
			out = append(out, d+"\n")
		}
		var rows []string
		if ps, ok := or(get(p, "settings"), themejson.NewObject()).(*themejson.Object); ok {
			for _, k := range ps.Keys() {
				v, _ := ps.Get(k)
				st, ok := byID.Get(k)
				if !ok || !themejson.Truthy(st) || str(get(st, "type")) == "color_scheme_group" {
					continue
				}
				bv, _ := base.Get(k)
				if themejson.Equal(v, bv) {
					continue
				}
				stObj := st.(*themejson.Object).Copy()
				stObj.Set("default", v)
				value := t.fmtDefault(stObj)
				if value == "" {
					value = themejson.Str(v)
				}
				rows = append(rows, "| "+esc(t.label(or(get(get(st, "editor"), "label"), themejson.String(k)), "fieldLabel"))+" | "+esc(value)+" |")
			}
		}
		if len(rows) > 0 {
			out = append(out, "Differs from the theme defaults in:\n\n| Setting | Value |\n|---|---|\n"+strings.Join(rows, "\n")+"\n")
		}
	}
	pages = append(pages, page{"ref-styles", strings.Join(out, "\n")})

	// Templates
	out = []string{front("Page templates", 62)}
	out = append(out, "Each page type has a template: the sections it shows, in order. Templates whose name has a second part "+
		"(for example *Product page: grid*) are alternate templates you can assign to individual products, "+
		"collections or pages. Every section listed can be moved, hidden, removed or added to.\n")
	for _, tname := range t.templates.Keys() {
		tpl, _ := t.templates.Get(tname)
		var names []string
		for _, s := range templateSections(tpl) {
			n := t.sectionName(themejson.Str(get(s, "type")))
			if themejson.Truthy(get(s, "disabled")) {
				n += " (hidden)"
			}
			names = append(names, n)
		}
		listed := "no sections"
		if len(names) > 0 {
			listed = strings.Join(names, ", ")
		}
		out = append(out, "- **"+templateTitle(tname)+"**: "+listed+".")
	}
	pages = append(pages, page{"ref-templates", strings.Join(out, "\n") + "\n"})

	// Sections in three pages; sections placed above or below every page
	// (announcement bars) sit with the header and footer.
	chrome := map[string]bool{}
	for _, sf := range list(get(t.manifest, "surfaces")) {
		key := str(get(sf, "surface_key"))
		if key != "global_body_start" && key != "global_body_end" {
			continue
		}
		for _, n := range list(get(sf, "allowed_sections")) {
			chrome[themejson.Str(n)] = true
		}
		for _, n := range list(get(sf, "required_sections")) {
			chrome[themejson.Str(n)] = true
		}
		for _, grp := range list(get(sf, "required_one_of")) {
			for _, n := range list(grp) {
				chrome[themejson.Str(n)] = true
			}
		}
	}
	type entry struct {
		name string
		sc   themejson.Value
	}
	groups := map[string][]entry{}
	for _, n := range t.sections.Keys() {
		sc, _ := t.sections.Get(n)
		g := groupOf(n, sc, chrome)
		groups[g] = append(groups[g], entry{n, sc})
	}
	spec := []struct {
		slug, title string
		order       int
		group, lead string
	}{
		{"ref-sections-pages", "Sections: page sections", 63, "page",
			"These sections show the main content of a page type (the product, the collection grid, the cart, the account). " +
				"Each page template has its own; settings and blocks are listed for each."},
		{"ref-sections-content", "Sections: content sections", 64, "content",
			"Content sections can be added to any page template, in any order and as many times as you like: open a template in " +
				"the theme editor and choose **Add section**."},
		{"ref-sections-header-footer", "Sections: header, footer and overlays", 65, "",
			"The header group sits at the top of every page and the footer group at the bottom; overlays are popups, drawers and " +
				"banners that open over any page. Change them in the theme editor from any page."},
	}
	for _, sp := range spec {
		out = []string{front(sp.title, sp.order), sp.lead + "\n"}
		var items []entry
		if sp.group != "" {
			items = append(items, groups[sp.group]...)
		} else {
			items = append(append(items, groups["layout"]...), groups["overlay"]...)
		}
		sort.SliceStable(items, func(i, j int) bool { return t.sectionName(items[i].name) < t.sectionName(items[j].name) })
		for _, it := range items {
			out = append(out, t.sectionDoc(it.name, it.sc, used))
		}
		pages = append(pages, page{sp.slug, strings.Join(out, "\n")})
	}

	// Blocks
	out = []string{front("Blocks", 66)}
	if t.blocks.Len() > 0 {
		out = append(out, "Theme blocks are shared building blocks you can add inside the sections that accept them (listed under "+
			"each section as *Also takes the shared theme blocks*). Some blocks hold other blocks.\n")
		accepts := map[string][]string{}
		for _, n := range t.sections.Keys() {
			sc, _ := t.sections.Get(n)
			if b, ok := get(sc, "blocks").(*themejson.Object); ok {
				for _, tb := range list(or(get(b, "theme_blocks"), []themejson.Value{})) {
					accepts[themejson.Str(tb)] = append(accepts[themejson.Str(tb)], t.sectionName(n))
				}
			}
		}
		names := t.blocks.Keys()
		sort.Strings(names)
		for _, n := range names {
			sc, _ := t.blocks.Get(n)
			var heading string
			if themejson.Truthy(get(sc, "editor")) {
				heading = t.label(or(get(get(sc, "editor"), "label"), themejson.String(n)), "fieldLabel")
			} else {
				heading = t.sectionName(themejson.Str(getOr(sc, "name", themejson.String(n))))
			}
			out = append(out, "## "+heading+"\n")
			if d := t.description("blocks", n, false); d != "" {
				out = append(out, d+"\n")
			}
			if a := accepts[n]; len(a) > 0 {
				set := map[string]bool{}
				var uniq []string
				for _, x := range a {
					if !set[x] {
						set[x] = true
						uniq = append(uniq, x)
					}
				}
				sort.Strings(uniq)
				out = append(out, "Can be added to: "+strings.Join(uniq, ", ")+".\n")
			}
			out = append(out, t.settingsTable(list(get(sc, "settings")), true))
			out = append(out, t.blocksText(sc))
		}
	} else {
		out = append(out, name+" defines its blocks inside each section; they are listed with the section that has them.\n")
	}
	pages = append(pages, page{"ref-blocks", strings.Join(out, "\n")})

	result := &ReferenceResult{}
	for _, p := range pages {
		body := strings.TrimRightFunc(p.body, unicode.IsSpace) + "\n"
		if err := os.WriteFile(filepath.Join(docs, p.slug+".md"), []byte(body), 0o666); err != nil {
			return nil, err
		}
		result.Pages = append(result.Pages, ReferencePage{Slug: p.slug, Chars: utf8.RuneCountInString(p.body)})
	}
	set := map[string]bool{}
	for _, m := range t.missing {
		if !set[m] {
			set[m] = true
			result.Missing = append(result.Missing, m)
		}
	}
	sort.Strings(result.Missing)
	return result, nil
}
