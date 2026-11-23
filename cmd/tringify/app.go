package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/tringify/cli/internal/appconfig"
	"github.com/tringify/cli/internal/devapi"
)

func (a *app) appCommand(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprint(a.stdout, usage)
		return nil
	}
	switch args[0] {
	case "list":
		return a.appList(ctx, args[1:])
	case "create":
		return a.appCreate(ctx, args[1:])
	case "categories":
		return a.appCategories(ctx, args[1:])
	case "init":
		return a.appInit(ctx, args[1:])
	case "config":
		if len(args) > 1 {
			switch args[1] {
			case "pull":
				return a.appConfigPull(ctx, args[2:])
			case "push":
				return a.appConfigPush(ctx, args[2:])
			case "check":
				return a.appConfigCheck(ctx, args[2:])
			}
		}
		return errors.New("usage: tringify app config pull|push|check")
	case "block":
		return a.appBlock(ctx, args[1:])
	case "release":
		return a.appRelease(ctx, args[1:])
	case "install-link":
		return a.appInstallLink(ctx, args[1:])
	case "listing":
		return a.appListing(ctx, args[1:])
	case "privacy", "terms", "docs", "api":
		return a.appSiteCommand(ctx, args[0], args[1:])
	case "versions":
		return a.appVersions(ctx, args[1:])
	case "publish":
		return a.appPublish(ctx, args[1:])
	case "webhook":
		if len(args) > 1 {
			switch args[1] {
			case "test":
				return a.appWebhookTest(ctx, args[2:])
			case "rotate-key":
				return a.appWebhookRotateKey(ctx, args[2:])
			case "finish-rotation":
				return a.appWebhookFinishRotation(ctx, args[2:])
			}
		}
		return errors.New("usage: tringify app webhook test|rotate-key|finish-rotation")
	case "secret":
		if len(args) > 1 {
			switch args[1] {
			case "rotate":
				return a.appSecretRotate(ctx, args[2:])
			case "finish-rotation":
				return a.appSecretFinishRotation(ctx, args[2:])
			}
		}
		return errors.New("usage: tringify app secret rotate|finish-rotation")
	case "deliveries":
		return a.appDeliveries(ctx, args[1:])
	case "retire":
		return a.appRetire(ctx, args[1:])
	case "delete":
		return a.appDelete(ctx, args[1:])
	}
	return fmt.Errorf("unknown app command %q", args[0])
}

// appAccess explains a refusal that a login without the command's access
// causes: a missing scope is refused as PERMISSION_DENIED, like a role
// without the permission.
func appAccess(err error) error {
	var apiErr *devapi.Error
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusForbidden && (apiErr.Code == "FORBIDDEN" || apiErr.Code == "PERMISSION_DENIED") {
		if publishingTokenInUse {
			// A publishing token has no more access than the member who
			// added its trusted publisher has now.
			return fmt.Errorf("%w. A trusted publisher acts with the access of the member who added it; that member needs this permission in the organization", err)
		}
		return fmt.Errorf("%w. If your role allows this, run `tringify login` again to grant the CLI its access", err)
	}
	return err
}

// appTarget is the app a command acts on: --app, or the app_id in the
// configuration file.
func appTarget(appFlag, file string) (string, error) {
	if appFlag != "" {
		return appFlag, nil
	}
	doc, _, err := appconfig.Read(file)
	if errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("no %s here. Pass --app ID (see `tringify app list`)", file)
	}
	if err != nil {
		return "", err
	}
	if id := doc.AppID(); id != "" {
		return id, nil
	}
	return "", fmt.Errorf("%s has no app_id. Pass --app ID", file)
}

func (a *app) appList(ctx context.Context, args []string) error {
	fs := newFlags("app list", "List your organization's apps.")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	api, session, err := a.devAPI()
	if err != nil {
		return err
	}
	// The API returns at most 50 apps a page.
	var data struct {
		Apps []map[string]any `json:"apps"`
	}
	for page := 1; ; page++ {
		var batch struct {
			Apps       []map[string]any `json:"apps"`
			TotalCount int              `json:"total_count"`
		}
		if err := api.Do(ctx, http.MethodGet, "/apps?limit=50&page="+strconv.Itoa(page), nil, &batch); err != nil {
			return appAccess(err)
		}
		data.Apps = append(data.Apps, batch.Apps...)
		if len(batch.Apps) == 0 || len(data.Apps) >= batch.TotalCount {
			break
		}
	}
	if len(data.Apps) == 0 {
		a.printf("No apps in %s. Create one with `tringify app create`.\n", session.Account.Target.Name)
		return nil
	}
	for _, item := range data.Apps {
		a.printf("%s  %s  %s  %s  %s\n", str(item["id"]), str(item["name"]), str(item["app_type"]), str(item["distribution"]), str(item["submission_status"]))
	}
	return nil
}

// stringList is a flag that can be given more than once.
type stringList []string

func (l *stringList) String() string { return strings.Join(*l, ",") }

func (l *stringList) Set(v string) error { *l = append(*l, v); return nil }

// appCreate creates an app. Its client secret is issued once, in this
// command's output; nothing can read it again later.
func (a *app) appCreate(ctx context.Context, args []string) error {
	fs := newFlags("app create --name NAME [--handle HANDLE] --type standard|sales_channel --distribution private|marketplace [--category ID --subcategory ID] [--json]",
		"Create an app in your organization. Its handle, the unique name in its App Store address and its project directory, is made from the name unless you choose one with --handle; it cannot change later. Sales channel apps need a category and subcategory (list them with `tringify app categories`); the subcategory sets the channel type. Run `tringify app init --app ID` next to start a project for it.")
	name := fs.String("name", "", "app name")
	handle := fs.String("handle", "", "the app's handle: lowercase letters, numbers and hyphens (default: made from the name)")
	appType := fs.String("type", "", "standard or sales_channel")
	distribution := fs.String("distribution", "", "private (install links) or marketplace (App Store listing)")
	category := fs.String("category", "", "category ID (see `tringify app categories`)")
	subcategory := fs.String("subcategory", "", "subcategory ID (see `tringify app categories`)")
	description := fs.String("description", "", "short description")
	asJSON := fs.Bool("json", false, "print the result as JSON, for scripts and piping into a secret store")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	body := map[string]any{"name": *name, "app_type": *appType, "distribution": *distribution}
	for key, value := range map[string]string{"slug": *handle, "category_id": *category, "subcategory_id": *subcategory, "description": *description} {
		if value != "" {
			body[key] = value
		}
	}
	api, _, err := a.devAPI()
	if err != nil {
		return err
	}
	// Check the handle first, so a taken or malformed one is reported with
	// the way out instead of a refused create.
	var check struct {
		Handle    string `json:"handle"`
		Available bool   `json:"available"`
		Message   string `json:"message"`
	}
	query := url.Values{"name": {*name}, "handle": {*handle}}
	if err := api.Do(ctx, http.MethodGet, "/apps/handle-check?"+query.Encode(), nil, &check); err != nil {
		return appAccess(err)
	}
	if !check.Available {
		return fmt.Errorf("%s Set the handle with --handle", check.Message)
	}
	return a.createApp(ctx, api, body, *asJSON)
}

func (a *app) createApp(ctx context.Context, api *devapi.Client, body map[string]any, asJSON bool) error {
	var created struct {
		App struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			Slug string `json:"slug"`
		} `json:"app"`
		Secrets struct {
			ClientID     string `json:"client_id"`
			ClientSecret string `json:"client_secret"`
		} `json:"secrets"`
	}
	if err := api.Do(ctx, http.MethodPost, "/apps", body, &created); err != nil {
		return appAccess(err)
	}
	if asJSON {
		return json.NewEncoder(a.stdout).Encode(map[string]string{
			"app_id": created.App.ID, "name": created.App.Name, "slug": created.App.Slug,
			"client_id": created.Secrets.ClientID, "client_secret": created.Secrets.ClientSecret,
		})
	}
	a.printf("Created %s\n\n", created.App.Name)
	a.printf("App ID:         %s\n", created.App.ID)
	a.printf("Handle:         %s\n", created.App.Slug)
	a.printf("Client ID:      %s\n", created.Secrets.ClientID)
	a.printf("Client secret:  %s\n\n", created.Secrets.ClientSecret)
	a.println("Store the client secret now. It is shown only once; to issue a new one, run tringify app secret rotate.")
	a.printf("\nNext: tringify app init --app %s [DIR]   (a project from the app starter)\n", created.App.ID)
	return nil
}
