package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/tringify/cli/internal/devapi"
)

// Support cases from merchants who use the organization's apps and themes,
// answered as the signed-in member.

const supportUsage = `Usage:
  tringify support cases list      List cases (default: the ones that need a reply)
  tringify support cases get CASE  Show a case with its messages and what the merchant shared
  tringify support cases reply CASE --message TEXT [--solve]
  tringify support cases note CASE --message TEXT   Internal note, only your team sees it
  tringify support cases solve CASE
  tringify support cases reopen CASE
  tringify support cases assign CASE --to ACCOUNT_ID|me|none
  tringify support settings get    Show the organization's support settings

CASE is a case number such as C-20418, or a case ID.
`

func (a *app) support(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprint(a.stdout, supportUsage)
		return nil
	}
	switch args[0] {
	case "cases":
		if len(args) < 2 || args[1] == "help" || args[1] == "--help" || args[1] == "-h" {
			fmt.Fprint(a.stdout, supportUsage)
			return nil
		}
		rest := args[2:]
		switch args[1] {
		case "list":
			return a.supportList(ctx, rest)
		case "get":
			return a.supportGet(ctx, rest)
		case "reply":
			return a.supportWrite(ctx, "reply", rest)
		case "note":
			return a.supportWrite(ctx, "note", rest)
		case "solve":
			return a.supportStatus(ctx, "solve", "solved", rest)
		case "reopen":
			return a.supportStatus(ctx, "reopen", "open", rest)
		case "assign":
			return a.supportAssign(ctx, rest)
		}
		return fmt.Errorf("unknown support cases command %q", args[1])
	case "settings":
		if len(args) >= 2 && args[1] == "get" {
			return a.supportSettings(ctx, args[2:])
		}
		return errors.New("usage: tringify support settings get")
	}
	return fmt.Errorf("unknown support command %q", args[0])
}

type supportCase struct {
	ID            string `json:"id"`
	DisplayNumber string `json:"display_number"`
	Subject       string `json:"subject"`
	Status        string `json:"status"`
	DisplayStatus string `json:"display_status"`
	About         struct {
		Kind    string `json:"kind"`
		ID      string `json:"id"`
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"about"`
	Store struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"store"`
	Requester struct {
		Name string `json:"name"`
	} `json:"requester"`
	DevAssignee *struct {
		AccountID string `json:"account_id"`
		Name      string `json:"name"`
	} `json:"dev_assignee"`
	EscalatedAt *time.Time      `json:"escalated_at"`
	Shared      json.RawMessage `json:"shared"`
	EntryPoint  string          `json:"entry_point"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
	Rating      *struct {
		Rating  string `json:"rating"`
		Comment string `json:"comment"`
	} `json:"rating"`
}

var caseNumber = regexp.MustCompile(`^[Cc]-?(\d+)$`)

// supportAccess explains a refused call in terms of the member's access.
func supportAccess(err error) error {
	var apiErr *devapi.Error
	if errors.As(err, &apiErr) {
		switch {
		case apiErr.Status == http.StatusForbidden:
			return fmt.Errorf("%w. Answering cases needs the Support permission in your developer organization; if your role has it, run `tringify login` again to grant the CLI support access", err)
		case apiErr.Status == http.StatusNotFound || apiErr.Code == "SUPPORT_NOT_FOUND":
			return errors.New("no case with that number or ID belongs to your organization")
		}
	}
	return err
}

// caseID turns a case number (C-20418) into the case's ID.
func (a *app) caseID(ctx context.Context, api *devapi.Client, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	m := caseNumber.FindStringSubmatch(ref)
	if m == nil {
		if ref == "" {
			return "", errors.New("name the case: a number such as C-20418, or its ID")
		}
		return ref, nil
	}
	want := "C-" + m[1]
	var page struct {
		Items []supportCase `json:"items"`
	}
	query := url.Values{"status": {"all"}, "search": {want}, "limit": {"25"}}
	if err := api.Do(ctx, http.MethodGet, "/support/cases?"+query.Encode(), nil, &page); err != nil {
		return "", supportAccess(err)
	}
	for _, c := range page.Items {
		if strings.EqualFold(c.DisplayNumber, want) {
			return c.ID, nil
		}
	}
	return "", fmt.Errorf("no case %s belongs to your organization", want)
}

func (a *app) supportList(ctx context.Context, args []string) error {
	fs := newFlags("support cases list [--status STATUS] [--app ID | --theme ID] [--assignee me|unassigned|ID] [--store ID] [--search TEXT]",
		"List your organization's support cases, most recently updated first.")
	status := fs.String("status", "needs_reply", "needs_reply, waiting_for_merchant, with_tringify, solved, closed, active or all")
	appID := fs.String("app", "", "only cases about this app")
	themeID := fs.String("theme", "", "only cases about this theme (its Theme Store listing ID)")
	assignee := fs.String("assignee", "", "me, unassigned, or a member's account ID")
	store := fs.String("store", "", "only cases from this store ID")
	search := fs.String("search", "", "match the subject, store name or case number")
	page := fs.Int("page", 1, "page number")
	limit := fs.Int("limit", 25, "cases per page: 25, 50 or 100")
	asJSON := fs.Bool("json", false, "print the result as JSON")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	if *appID != "" && *themeID != "" {
		return errors.New("use --app or --theme, not both")
	}
	api, _, err := a.devAPI()
	if err != nil {
		return err
	}
	query := url.Values{"status": {*status}, "page": {strconv.Itoa(*page)}, "limit": {strconv.Itoa(*limit)}}
	if *appID != "" {
		query.Set("about_kind", "app")
		query.Set("about_id", *appID)
	}
	if *themeID != "" {
		query.Set("about_kind", "theme")
		query.Set("about_id", *themeID)
	}
	for key, value := range map[string]string{"assignee": *assignee, "store_id": *store, "search": *search} {
		if value != "" {
			query.Set(key, value)
		}
	}
	var raw json.RawMessage
	if err := api.Do(ctx, http.MethodGet, "/support/cases?"+query.Encode(), nil, &raw); err != nil {
		return supportAccess(err)
	}
	if *asJSON {
		return a.printJSON(raw)
	}
	var result struct {
		Items      []supportCase `json:"items"`
		Page       int           `json:"page"`
		TotalPages int           `json:"total_pages"`
		Counts     struct {
			NeedsReply         int `json:"needs_reply"`
			WaitingForMerchant int `json:"waiting_for_merchant"`
			WithTringify       int `json:"with_tringify"`
		} `json:"counts"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return err
	}
	a.printf("Needs reply: %d · Waiting for merchant: %d · With Tringify: %d\n\n", result.Counts.NeedsReply, result.Counts.WaitingForMerchant, result.Counts.WithTringify)
	if len(result.Items) == 0 {
		a.println("No cases.")
		return nil
	}
	for _, c := range result.Items {
		assigned := "unassigned"
		if c.DevAssignee != nil {
			assigned = c.DevAssignee.Name
		}
		a.printf("%-8s  %-20s  %s · %s · %s · %s · updated %s\n", c.DisplayNumber, c.DisplayStatus, c.Subject, c.About.Name, c.Store.Name, assigned, c.UpdatedAt.UTC().Format("2006-01-02 15:04 UTC"))
	}
	if result.Page < result.TotalPages {
		a.printf("\nPage %d of %d. Next: --page %d\n", result.Page, result.TotalPages, result.Page+1)
	}
	return nil
}

func (a *app) supportGet(ctx context.Context, args []string) error {
	fs := newFlags("support cases get CASE [--json]", "Show a case: its messages, your team's notes, and what the merchant shared.")
	asJSON := fs.Bool("json", false, "print the result as JSON")
	rest, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return errors.New("usage: tringify support cases get CASE")
	}
	api, _, err := a.devAPI()
	if err != nil {
		return err
	}
	id, err := a.caseID(ctx, api, rest[0])
	if err != nil {
		return err
	}
	var raw json.RawMessage
	if err := api.Do(ctx, http.MethodGet, "/support/cases/"+url.PathEscape(id), nil, &raw); err != nil {
		return supportAccess(err)
	}
	if *asJSON {
		return a.printJSON(raw)
	}
	var view struct {
		Case     supportCase `json:"case"`
		Messages []struct {
			Author      string    `json:"author"`
			AuthorName  string    `json:"author_name"`
			Kind        string    `json:"kind"`
			BodyText    string    `json:"body_text"`
			CreatedAt   time.Time `json:"created_at"`
			Attachments []struct {
				Filename string `json:"filename"`
				Size     int64  `json:"size"`
			} `json:"attachments"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(raw, &view); err != nil {
		return err
	}
	c := view.Case
	a.printf("%s  %s\n%s\n", c.DisplayNumber, c.DisplayStatus, c.Subject)
	a.printf("About: %s %s", c.About.Name, c.About.Kind)
	if c.About.Version != "" {
		a.printf(" %s", c.About.Version)
	}
	a.printf("\nStore: %s · from %s (%s) · opened %s\n", c.Store.Name, c.Requester.Name, c.EntryPoint, c.CreatedAt.UTC().Format("2006-01-02 15:04 UTC"))
	if c.DevAssignee != nil {
		a.printf("Assigned: %s\n", c.DevAssignee.Name)
	}
	if c.EscalatedAt != nil {
		a.printf("With Tringify since %s\n", c.EscalatedAt.UTC().Format("2006-01-02 15:04 UTC"))
	}
	if c.Rating != nil && c.Rating.Rating != "" {
		a.printf("Rated: %s %s\n", c.Rating.Rating, c.Rating.Comment)
	}
	if len(c.Shared) > 0 && string(c.Shared) != "null" {
		var shared bytes.Buffer
		if err := json.Indent(&shared, c.Shared, "  ", "  "); err == nil {
			a.printf("Shared by the merchant:\n  %s\n", shared.String())
		}
	}
	for _, m := range view.Messages {
		who := m.AuthorName
		if m.Kind == "note" {
			who += " (internal note)"
		}
		a.printf("\n— %s · %s\n%s\n", who, m.CreatedAt.UTC().Format("2006-01-02 15:04 UTC"), m.BodyText)
		for _, f := range m.Attachments {
			a.printf("  [file] %s (%d bytes)\n", f.Filename, f.Size)
		}
	}
	return nil
}

func (a *app) supportWrite(ctx context.Context, kind string, args []string) error {
	summary := "Reply to the merchant. They get it by email and in the Help Center, signed with your name and your organization's."
	if kind == "note" {
		summary = "Add an internal note. Only your team and Tringify support see it."
	}
	fs := newFlags("support cases "+kind+" CASE --message TEXT | --message-file PATH", summary)
	message := fs.String("message", "", "the text")
	messageFile := fs.String("message-file", "", "read the text from this file (- for standard input)")
	var solve *bool
	if kind == "reply" {
		solve = fs.Bool("solve", false, "mark the case solved with this reply")
	}
	rest, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return fmt.Errorf("usage: tringify support cases %s CASE --message TEXT", kind)
	}
	text, err := a.messageText(*message, *messageFile)
	if err != nil {
		return err
	}
	api, _, err := a.devAPI()
	if err != nil {
		return err
	}
	id, err := a.caseID(ctx, api, rest[0])
	if err != nil {
		return err
	}
	body := map[string]any{"body_text": text}
	path := "/support/cases/" + url.PathEscape(id) + "/messages"
	if kind == "note" {
		path = "/support/cases/" + url.PathEscape(id) + "/notes"
	} else if *solve {
		body["status_after"] = "solved"
	}
	if err := api.Do(ctx, http.MethodPost, path, body, nil); err != nil {
		return supportAccess(err)
	}
	switch {
	case kind == "note":
		a.println("Note added.")
	case *solve:
		a.println("Reply sent and the case is marked solved.")
	default:
		a.println("Reply sent.")
	}
	return nil
}

func (a *app) messageText(message, file string) (string, error) {
	if message != "" && file != "" {
		return "", errors.New("use --message or --message-file, not both")
	}
	text := message
	if file != "" {
		var data []byte
		var err error
		if file == "-" {
			data, err = io.ReadAll(io.LimitReader(a.stdin, 1<<20))
		} else {
			data, err = os.ReadFile(file)
		}
		if err != nil {
			return "", err
		}
		text = string(data)
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return "", errors.New("write the text with --message or --message-file")
	}
	if len([]rune(text)) > 20000 {
		return "", errors.New("the text is longer than 20,000 characters")
	}
	return text, nil
}

func (a *app) supportStatus(ctx context.Context, name, status string, args []string) error {
	summary := "Mark the case solved. The merchant can open it again within 7 days."
	if status == "open" {
		summary = "Open a case solved in the last 7 days again."
	}
	fs := newFlags("support cases "+name+" CASE", summary)
	rest, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return fmt.Errorf("usage: tringify support cases %s CASE", name)
	}
	api, _, err := a.devAPI()
	if err != nil {
		return err
	}
	id, err := a.caseID(ctx, api, rest[0])
	if err != nil {
		return err
	}
	if err := api.Do(ctx, http.MethodPost, "/support/cases/"+url.PathEscape(id)+"/status", map[string]any{"status": status}, nil); err != nil {
		return supportAccess(err)
	}
	if status == "open" {
		a.println("The case is open again.")
	} else {
		a.println("The case is marked solved.")
	}
	return nil
}

func (a *app) supportAssign(ctx context.Context, args []string) error {
	fs := newFlags("support cases assign CASE --to ACCOUNT_ID|me|none", "Assign the case to a member with Support access, to yourself, or to no one.")
	to := fs.String("to", "", "a member's account ID, me, or none")
	rest, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(rest) != 1 || *to == "" {
		return errors.New("usage: tringify support cases assign CASE --to ACCOUNT_ID|me|none")
	}
	api, _, err := a.devAPI()
	if err != nil {
		return err
	}
	id, err := a.caseID(ctx, api, rest[0])
	if err != nil {
		return err
	}
	var account any = *to
	if *to == "none" {
		account = nil
	}
	if err := api.Do(ctx, http.MethodPost, "/support/cases/"+url.PathEscape(id)+"/assign", map[string]any{"account_id": account}, nil); err != nil {
		return supportAccess(err)
	}
	if account == nil {
		a.println("The case is unassigned.")
	} else {
		a.println("The case is assigned.")
	}
	return nil
}

func (a *app) supportSettings(ctx context.Context, args []string) error {
	fs := newFlags("support settings get [--json]", "Show your organization's support settings: response target, support hours, auto-reply and where cases can start.")
	asJSON := fs.Bool("json", false, "print the result as JSON")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	api, _, err := a.devAPI()
	if err != nil {
		return err
	}
	var raw json.RawMessage
	if err := api.Do(ctx, http.MethodGet, "/support/settings", nil, &raw); err != nil {
		return supportAccess(err)
	}
	if *asJSON {
		return a.printJSON(raw)
	}
	var data struct {
		Settings struct {
			ResponseTargetHours int    `json:"response_target_hours"`
			Timezone            string `json:"timezone"`
			Hours               []struct {
				Day   int    `json:"day"`
				Start string `json:"start"`
				End   string `json:"end"`
			} `json:"hours"`
			AutoReplyEnabled   bool `json:"auto_reply_enabled"`
			AllowAppBridge     bool `json:"allow_app_bridge"`
			AllowMCP           bool `json:"allow_mcp"`
			ShareErrorsDefault bool `json:"share_errors_default"`
		} `json:"settings"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return err
	}
	s := data.Settings
	onOff := func(on bool) string {
		if on {
			return "on"
		}
		return "off"
	}
	a.printf("Response target: %d business hours\nTime zone: %s\n", s.ResponseTargetHours, s.Timezone)
	if len(s.Hours) == 0 {
		a.println("Support hours: always")
	} else {
		days := []string{"", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"}
		var parts []string
		for _, h := range s.Hours {
			day := strconv.Itoa(h.Day)
			if h.Day >= 1 && h.Day <= 7 {
				day = days[h.Day]
			}
			parts = append(parts, day+" "+h.Start+"–"+h.End)
		}
		a.printf("Support hours: %s\n", strings.Join(parts, ", "))
	}
	a.printf("Auto-reply outside hours: %s\nCases from inside your app: %s\nCases from AI assistants: %s\nShare errors by default: %s\n",
		onOff(s.AutoReplyEnabled), onOff(s.AllowAppBridge), onOff(s.AllowMCP), onOff(s.ShareErrorsDefault))
	return nil
}

func (a *app) printJSON(raw json.RawMessage) error {
	var out bytes.Buffer
	if err := json.Indent(&out, raw, "", "  "); err != nil {
		return err
	}
	a.println(out.String())
	return nil
}
