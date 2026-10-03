package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/tringify/cli/internal/devapi"
)

func (a *app) store(ctx context.Context, args []string) error {
	if len(args) == 0 {
		fmt.Fprint(a.stdout, usage)
		return nil
	}
	switch args[0] {
	case "list":
		return a.storeList(ctx, args[1:])
	case "create":
		return a.storeCreate(ctx, args[1:])
	}
	return fmt.Errorf("unknown store command %q", args[0])
}

// devStoreAccess explains a refusal from a login made before development
// store access was part of the sign-in.
func devStoreAccess(err error) error {
	var apiErr *devapi.Error
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusForbidden {
		return fmt.Errorf("%w. Run `tringify login` again to allow development store access", err)
	}
	return err
}

func (a *app) storeList(ctx context.Context, args []string) error {
	fs := newFlags("store list", "List your organization's development stores.")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	api, session, err := a.devAPI()
	if err != nil {
		return err
	}
	var data any
	if err := api.Do(ctx, http.MethodGet, "/stores?limit=100", nil, &data); err != nil {
		return devStoreAccess(err)
	}
	list := items(data)
	if m, ok := data.(map[string]any); ok && len(list) == 0 {
		if l, ok := m["stores"].([]any); ok {
			list = items(l)
		}
	}
	if len(list) == 0 {
		a.printf("No development stores in %s. Create one with `tringify store create --name NAME`.\n", session.Account.Target.Name)
		return nil
	}
	for _, s := range list {
		status := str(s["status"])
		if setup := str(s["setup_status"]); setup != "" && setup != "completed" {
			status = "setup " + setup
		}
		a.printf("%s  %s  %s.mytringify.com  %s\n", str(s["id"]), str(s["name"]), str(s["subdomain"]), status)
	}
	return nil
}

var subdomainCleaner = regexp.MustCompile(`[^a-z0-9]+`)

func subdomainFor(name string) string {
	return strings.Trim(subdomainCleaner.ReplaceAllString(strings.ToLower(name), "-"), "-")
}

// localTimezone names this computer's time zone, or "" when it is unknown.
func localTimezone() string {
	if tz := os.Getenv("TZ"); tz != "" {
		return strings.TrimPrefix(tz, ":")
	}
	if target, err := os.Readlink("/etc/localtime"); err == nil {
		if i := strings.Index(target, "zoneinfo/"); i >= 0 {
			return target[i+len("zoneinfo/"):]
		}
	}
	return ""
}

func (a *app) storeCreate(ctx context.Context, args []string) error {
	fs := newFlags("store create --name NAME --country CC --currency CUR [--subdomain SUB] [--timezone ZONE] [--billing-currency CUR]", "Create a development store for your organization and wait until it is ready.")
	name := fs.String("name", "", "store name (required)")
	subdomain := fs.String("subdomain", "", "store address, SUB.mytringify.com (default: from the name)")
	country := fs.String("country", "", "store country, for example IN or GB (required)")
	currency := fs.String("currency", "", "store currency, for example INR (required)")
	timezone := fs.String("timezone", localTimezone(), "store time zone, for example Asia/Kolkata")
	billing := fs.String("billing-currency", "", "organization billing currency (default: the only one offered)")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	if strings.TrimSpace(*name) == "" || *country == "" || *currency == "" {
		return errors.New("--name, --country and --currency are required")
	}
	if *timezone == "" {
		return errors.New("--timezone is required, for example Asia/Kolkata")
	}
	if *subdomain == "" {
		*subdomain = subdomainFor(*name)
	}
	api, session, err := a.devAPI()
	if err != nil {
		return err
	}
	if *billing == "" {
		var limits struct {
			BillingCurrencies []string `json:"billing_currencies"`
		}
		if err := api.Do(ctx, http.MethodGet, "/stores/limits", nil, &limits); err != nil {
			return devStoreAccess(err)
		}
		if len(limits.BillingCurrencies) != 1 {
			return fmt.Errorf("pass --billing-currency, one of %s", strings.Join(limits.BillingCurrencies, ", "))
		}
		*billing = limits.BillingCurrencies[0]
	}
	var created map[string]any
	body := map[string]any{"name": strings.TrimSpace(*name), "subdomain": *subdomain, "country": strings.ToUpper(*country), "currency": strings.ToUpper(*currency), "timezone": *timezone, "billing_currency": strings.ToUpper(*billing)}
	if err := api.Do(ctx, http.MethodPost, "/stores", body, &created); err != nil {
		return devStoreAccess(err)
	}
	if s, ok := created["store"].(map[string]any); ok {
		created = s
	}
	id := str(created["id"])
	if id == "" {
		return errors.New("the store was created, but the reply had no store ID; see `tringify store list`")
	}
	a.printf("Creating %s in %s…\n", strings.TrimSpace(*name), session.Account.Target.Name)
	deadline := time.Now().Add(5 * time.Minute)
	for {
		var status map[string]any
		if err := api.Do(ctx, http.MethodGet, "/stores/"+url.PathEscape(id)+"/setup/status", nil, &status); err != nil {
			return err
		}
		switch firstNonEmpty(str(status["setup_status"]), str(status["status"])) {
		case "completed":
			a.printf("Ready: %s (store %s, %s.mytringify.com).\n", strings.TrimSpace(*name), id, *subdomain)
			a.printf("\nNext:\n  tringify login --store\n  tringify theme dev --store %s\n", id)
			return nil
		case "failed":
			return fmt.Errorf("store setup failed; retry it from the Developer Portal (store %s)", id)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("store %s is still being set up; check `tringify store list`", id)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}
