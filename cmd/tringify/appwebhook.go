package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/tringify/cli/internal/appconfig"
	"github.com/tringify/cli/internal/devapi"
)

func (a *app) appWebhookTest(ctx context.Context, args []string) error {
	fs := newFlags("app webhook test", "Send a signed app.test webhook to the app's webhook URL and report the answer.")
	appFlag := fs.String("app", "", "app ID (default: app_id in "+appconfig.FileName+")")
	file := fs.String("file", appconfig.FileName, "configuration file")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	appID, err := appTarget(*appFlag, *file)
	if err != nil {
		return err
	}
	api, _, err := a.devAPI()
	if err != nil {
		return err
	}
	if err := api.Do(ctx, http.MethodPost, "/apps/"+url.PathEscape(appID)+"/webhooks/test", nil, nil); err != nil {
		return appAccess(err)
	}
	a.println("Your webhook URL accepted the test webhook.")
	return nil
}

func (a *app) appDeliveries(ctx context.Context, args []string) error {
	fs := newFlags("app deliveries [--data] [--status failed] [--store STORE] [--delivery ID] [--event ID] [--cursor CURSOR]",
		"List webhook deliveries to the app, newest first: lifecycle deliveries, or with --data store data deliveries. If some stores can't be reached, a note on stderr says their deliveries are missing from the list.")
	appFlag := fs.String("app", "", "app ID (default: app_id in "+appconfig.FileName+")")
	file := fs.String("file", appconfig.FileName, "configuration file")
	data := fs.Bool("data", false, "list store data deliveries instead of lifecycle deliveries")
	status := fs.String("status", "", "only deliveries with this status, such as failed, retrying or delivered")
	event := fs.String("event-type", "", "only deliveries of this event type")
	store := fs.String("store", "", "only deliveries to this store: its ID, its domain, or part of its name")
	delivery := fs.String("delivery", "", "only the delivery with this ID")
	eventID := fs.String("event", "", "data deliveries: only deliveries of the event with this ID")
	cursor := fs.String("cursor", "", "continue a list where the previous page ended (printed after a page)")
	limit := fs.Int("limit", 20, "how many to list per page (at most 100)")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	if *eventID != "" && !*data {
		return errors.New("--event filters data deliveries; add --data")
	}
	appID, err := appTarget(*appFlag, *file)
	if err != nil {
		return err
	}
	api, _, err := a.devAPI()
	if err != nil {
		return err
	}
	query := url.Values{"limit": {strconv.Itoa(*limit)}}
	for key, value := range map[string]string{"status": *status, "event_type": *event, "store": *store, "delivery_id": *delivery, "event_id": *eventID, "cursor": *cursor} {
		if value != "" {
			query.Set(key, value)
		}
	}
	path := "/webhooks/deliveries"
	if *data {
		path = "/webhooks/data-deliveries"
	}
	var page struct {
		Items []struct {
			DeliveryID   string    `json:"delivery_id"`
			StoreID      string    `json:"store_id"`
			StoreName    string    `json:"store_name"`
			EventType    string    `json:"event_type"`
			StatusLabel  string    `json:"status_label"`
			Attempts     int       `json:"attempts"`
			ResponseCode *int      `json:"last_response_code"`
			LastError    *string   `json:"last_error"`
			CreatedAt    time.Time `json:"created_at"`
		} `json:"items"`
		NextCursor         string      `json:"next_cursor"`
		UnavailableRegions unavailable `json:"unavailable_regions"`
	}
	if err := api.Do(ctx, http.MethodGet, "/apps/"+url.PathEscape(appID)+path+"?"+query.Encode(), nil, &page); err != nil {
		return appAccess(err)
	}
	if len(page.UnavailableRegions) > 0 {
		fmt.Fprintln(a.stderr, "Some stores couldn't be reached right now; their deliveries are missing from this list. Try again in a few minutes.")
	}
	if len(page.Items) == 0 {
		a.println("No webhook deliveries.")
		return nil
	}
	for _, d := range page.Items {
		var answer bytes.Buffer
		if d.ResponseCode != nil {
			fmt.Fprintf(&answer, "HTTP %d", *d.ResponseCode)
		}
		if d.LastError != nil && *d.LastError != "" {
			if answer.Len() > 0 {
				answer.WriteString(", ")
			}
			answer.WriteString(*d.LastError)
		}
		store := d.StoreName
		if store == "" {
			store = d.StoreID
		}
		a.printf("%s  %s  %-28s %-10s %d attempt(s)  %s  %s\n", d.CreatedAt.UTC().Format("2006-01-02 15:04 UTC"), d.DeliveryID,
			d.EventType, d.StatusLabel, d.Attempts, store, answer.String())
	}
	if page.NextCursor != "" {
		a.printf("More deliveries: add --cursor %s\n", page.NextCursor)
	}
	return nil
}

// appWebhookRotateKey issues a new webhook signing secret. For 72 hours every
// request also carries a signature made with the previous secret, so the app
// keeps verifying while the new one is deployed.
func (a *app) appWebhookRotateKey(ctx context.Context, args []string) error {
	fs := newFlags("app webhook rotate-key [--pipe-to COMMAND | --json]", "Issue a new webhook signing secret. For 72 hours, webhooks and other signed requests also carry a second signature made with the previous secret, so your app keeps verifying while you deploy the new one; the output says exactly when signatures made with the previous secret stop. Rotating again during that time stops signatures made with the oldest secret at once. Run `tringify app webhook finish-rotation` once the new secret is deployed. The new secret is shown only in this command's output, or, with --pipe-to, handed to your secret store without being shown.")
	appFlag := fs.String("app", "", "app ID (default: app_id in "+appconfig.FileName+")")
	file := fs.String("file", appconfig.FileName, "configuration file")
	asJSON := fs.Bool("json", false, "print the result as JSON, for scripts and piping into a secret store")
	pipeTo := fs.String("pipe-to", "", "command that stores the new secret, given on its standard input; the secret is not printed")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	if *pipeTo != "" && *asJSON {
		return errors.New("use --pipe-to or --json, not both")
	}
	appID, err := appTarget(*appFlag, *file)
	if err != nil {
		return err
	}
	api, _, err := a.devAPI()
	if err != nil {
		return err
	}
	return a.rotateWebhookKey(ctx, api, appID, *asJSON, *pipeTo)
}

func (a *app) rotateWebhookKey(ctx context.Context, api *devapi.Client, appID string, asJSON bool, pipeTo string) error {
	var rotated struct {
		SigningKey string `json:"signing_key"`
		// Absent when the app had no secret before, so there is no overlap.
		PreviousKeyExpiresAt *time.Time `json:"previous_key_expires_at"`
	}
	if err := api.Do(ctx, http.MethodPost, "/apps/"+url.PathEscape(appID)+"/webhooks/rotate-signing-key", nil, &rotated); err != nil {
		return appAccess(err)
	}
	secret := rotated.SigningKey
	if secret == "" {
		return errors.New("the API did not return the new signing secret")
	}
	expires := rotated.PreviousKeyExpiresAt
	if pipeTo != "" {
		afterFailure := "The new one was not shown; run this command again to issue another"
		if expires != nil {
			afterFailure = fmt.Sprintf("The new one was not shown. Until %s, requests also carry a signature made with the signing secret that was current before this command ran; if that secret came from an earlier rotation you never deployed, your app is not verifying with it. Running this command again stops those signatures at once, so do it only when you can deploy the next secret right away", formatExpiry(*expires))
		}
		if err := a.storeSecret(ctx, "webhook signing secret", secret, pipeTo, afterFailure); err != nil {
			return err
		}
		a.printOverlap(expires)
		return nil
	}
	if asJSON {
		return json.NewEncoder(a.stdout).Encode(map[string]any{"app_id": appID, "webhook_signing_secret": secret, "previous_key_expires_at": expires})
	}
	a.printf("Webhook signing secret:  %s\n\n", secret)
	a.println("Store it now; it is shown only once.")
	a.printOverlap(expires)
	return nil
}

// printOverlap says until when requests still carry the previous secret's
// signature.
func (a *app) printOverlap(expires *time.Time) {
	if expires == nil {
		return
	}
	a.printf("Until %s, requests also carry a signature made with the previous secret. Once the new secret is deployed, run `tringify app webhook finish-rotation` to stop those signatures sooner.\n", formatExpiry(*expires))
}

func formatExpiry(t time.Time) string {
	return t.UTC().Format("2006-01-02 15:04 UTC")
}

// appWebhookFinishRotation stops signatures made with the previous webhook
// signing secret before its 72 hours are up.
func (a *app) appWebhookFinishRotation(ctx context.Context, args []string) error {
	fs := newFlags("app webhook finish-rotation [--app ID] [--yes]", "Stop signing requests with the previous webhook signing secret now instead of when its 72 hours end. Afterwards requests carry only the new secret's signature, so run this once the new secret is deployed everywhere you verify signatures.")
	appFlag := fs.String("app", "", "app ID (default: app_id in "+appconfig.FileName+")")
	file := fs.String("file", appconfig.FileName, "configuration file")
	yes := fs.Bool("yes", false, "finish without asking")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	appID, err := appTarget(*appFlag, *file)
	if err != nil {
		return err
	}
	api, _, err := a.devAPI()
	if err != nil {
		return err
	}
	return a.finishWebhookKeyRotation(ctx, api, appID, *yes)
}

func (a *app) finishWebhookKeyRotation(ctx context.Context, api *devapi.Client, appID string, yes bool) error {
	var details struct {
		Rotation struct {
			InProgress           bool       `json:"in_progress"`
			PreviousKeyExpiresAt *time.Time `json:"previous_key_expires_at"`
		} `json:"signing_key_rotation"`
	}
	if err := api.Do(ctx, http.MethodGet, "/apps/"+url.PathEscape(appID), nil, &details); err != nil {
		return appAccess(err)
	}
	if !details.Rotation.InProgress {
		a.println("No signing secret rotation is in progress; requests carry only the current secret's signature.")
		return nil
	}
	if !yes {
		question := "Stop signing requests with the previous signing secret now?"
		if details.Rotation.PreviousKeyExpiresAt != nil {
			question = fmt.Sprintf("Requests carry a signature made with the previous signing secret until %s. Stop those signatures now?", formatExpiry(*details.Rotation.PreviousKeyExpiresAt))
		}
		ok, err := confirmWith(a.stdin, a.stdout, question, "Pass --yes to finish without confirming")
		if err != nil || !ok {
			if err == nil {
				a.println("Rotation not finished.")
			}
			return err
		}
	}
	if err := api.Do(ctx, http.MethodPost, "/apps/"+url.PathEscape(appID)+"/webhooks/finish-signing-key-rotation", nil, nil); err != nil {
		return appAccess(err)
	}
	a.println("Finished the rotation. Requests now carry only the new signing secret's signature.")
	return nil
}
