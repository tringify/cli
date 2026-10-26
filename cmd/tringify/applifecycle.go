package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/tringify/cli/internal/appconfig"
	"github.com/tringify/cli/internal/browser"
)

// An app that was ever used can only be retired; only a never-used app can
// be deleted. Both need the developer's email code or passkey, which only
// the Developer Portal can ask for, so these commands show what the platform
// offers for the app and open its Danger Zone to confirm.

type lifecycleMessage struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type appLifecycle struct {
	AppID              string             `json:"app_id"`
	Name               string             `json:"name"`
	State              string             `json:"state"`
	RetiredAt          *time.Time         `json:"retired_at"`
	Offer              string             `json:"offer"`
	Summary            lifecycleMessage   `json:"summary"`
	Reasons            []lifecycleMessage `json:"reasons"`
	UnavailableRegions []struct {
		Stamp string `json:"stamp"`
		Name  string `json:"name"`
	} `json:"unavailable_regions"`
	DeleteRequest *struct {
		Status      string             `json:"status"`
		CreatedAt   time.Time          `json:"created_at"`
		CompletedAt *time.Time         `json:"completed_at"`
		Message     string             `json:"message"`
		Reasons     []lifecycleMessage `json:"reasons"`
	} `json:"delete_request"`
}

func (a *app) appRetire(ctx context.Context, args []string) error {
	return a.appLifecycleAction(ctx, args, "retire")
}

func (a *app) appDelete(ctx context.Context, args []string) error {
	return a.appLifecycleAction(ctx, args, "delete")
}

func (a *app) appLifecycleAction(ctx context.Context, args []string, action string) error {
	summaries := map[string]string{
		"retire": "Check whether the app can be retired and open the Developer Portal to retire it. A retired app leaves the App Store and accepts no new installs or versions; stores that installed it keep using it until they uninstall. Retiring cannot be undone. Any app that was ever used is retired, not deleted.",
		"delete": "Check whether the app can be deleted and open the Developer Portal to delete it. Only an app that was never used on any region can be deleted; any other app can only be retired (`tringify app retire`).",
	}
	fs := newFlags("app "+action+" [--app ID] [--json]", summaries[action]+" You confirm in the Developer Portal's Danger Zone because it needs your email code or passkey; set TRINGIFY_NO_BROWSER=1 to only print the link. Needs the Delete Apps permission; if the API refuses with a permission error, run `tringify login` again to grant it.")
	appFlag := fs.String("app", "", "app ID (default: app_id in "+appconfig.FileName+")")
	file := fs.String("file", appconfig.FileName, "configuration file")
	asJSON := fs.Bool("json", false, "print the platform's answer as JSON and open nothing")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	appID, err := appTarget(*appFlag, *file)
	if err != nil {
		return err
	}
	api, session, err := a.devAPI()
	if err != nil {
		return err
	}
	var raw json.RawMessage
	if err := api.Do(ctx, http.MethodGet, "/apps/"+url.PathEscape(appID)+"/lifecycle", nil, &raw); err != nil {
		return appAccess(err)
	}
	var lc appLifecycle
	if err := json.Unmarshal(raw, &lc); err != nil {
		return fmt.Errorf("the Developer API returned an unreadable reply: %w", err)
	}
	if *asJSON {
		var out bytes.Buffer
		if err := json.Indent(&out, raw, "", "  "); err != nil {
			return err
		}
		out.WriteByte('\n')
		if _, err := a.stdout.Write(out.Bytes()); err != nil {
			return err
		}
	} else {
		a.printLifecycle(lc)
	}

	switch lc.Offer {
	case action:
		if *asJSON {
			return nil
		}
		link := a.portalBase + "/dashboard/" + url.PathEscape(session.Account.Target.ID) + "/apps/" + url.PathEscape(lc.AppID) + "/danger"
		verb := map[string]string{"retire": "Retiring", "delete": "Deleting"}[action]
		a.printf("\n%s needs your email code or passkey, so you confirm it in the Developer Portal's Danger Zone:\n  %s\n", verb, link)
		if err := browser.Open(link); err != nil {
			a.printf("Could not open your browser (%v). Open the link above.\n", err)
		}
		return nil
	case "retire":
		return fmt.Errorf("%q cannot be deleted. Retire it instead: tringify app retire%s", lc.Name, appArg(*appFlag, appID))
	case "delete":
		return fmt.Errorf("%q was never used, so it is deleted rather than retired: tringify app delete%s", lc.Name, appArg(*appFlag, appID))
	}
	return fmt.Errorf("%q cannot be %s", lc.Name, pastParticiple(action))
}

// appArg repeats --app in a suggested command when it was given.
func appArg(appFlag, appID string) string {
	if appFlag == "" {
		return ""
	}
	return " --app " + appID
}

func pastParticiple(action string) string {
	if action == "retire" {
		return "retired"
	}
	return "deleted"
}

func (a *app) printLifecycle(lc appLifecycle) {
	a.printf("App:    %s (%s)\n", lc.Name, lc.AppID)
	state := lc.State
	if lc.RetiredAt != nil {
		state += ", retired " + formatExpiry(*lc.RetiredAt)
	}
	a.printf("State:  %s\n\n", state)
	a.println(lc.Summary.Message)
	if len(lc.Reasons) > 0 {
		a.println("")
		for _, r := range lc.Reasons {
			a.printf("  - %s\n", r.Message)
		}
	}
	if len(lc.UnavailableRegions) > 0 {
		a.println("\nRegions that could not be checked:")
		for _, r := range lc.UnavailableRegions {
			a.printf("  - %s (%s)\n", r.Name, r.Stamp)
		}
	}
	if dr := lc.DeleteRequest; dr != nil {
		a.printf("\nLast delete request: %s, requested %s", dr.Status, formatExpiry(dr.CreatedAt))
		if dr.CompletedAt != nil {
			a.printf(", ended %s", formatExpiry(*dr.CompletedAt))
		}
		a.printf("\n%s\n", dr.Message)
		for _, r := range dr.Reasons {
			a.printf("  - %s\n", r.Message)
		}
	}
}
