package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/tringify/cli/internal/appconfig"
	"github.com/tringify/cli/internal/devapi"
)

// appSecretRotate issues a new client secret. For 72 hours the previous one
// keeps working, so the app keeps exchanging codes while the new one is
// deployed.
func (a *app) appSecretRotate(ctx context.Context, args []string) error {
	fs := newFlags("app secret rotate [--pipe-to COMMAND | --json]", "Issue a new client secret. For 72 hours the previous secret keeps working too, so your app keeps exchanging codes while you deploy the new one; the output says exactly when the previous secret stops working. Rotating again during that time stops the oldest secret at once. Run `tringify app secret finish-rotation` once the new secret is deployed, or right away if the previous secret was exposed. The new secret is shown only in this command's output, or, with --pipe-to, handed to your secret store without being shown.")
	appFlag := fs.String("app", "", "app ID (default: app_id in "+appconfig.FileName+")")
	file := fs.String("file", appconfig.FileName, "configuration file")
	asJSON := fs.Bool("json", false, "print the result as JSON, for scripts and piping into a secret store")
	pipeTo := fs.String("pipe-to", "", "command that stores the new secret, given on its standard input; the secret is not printed")
	code := fs.String("code", "", "verification code from your authenticator app (asked for when omitted)")
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
	return a.rotateClientSecret(ctx, api, appID, *asJSON, *pipeTo, *code)
}

func (a *app) rotateClientSecret(ctx context.Context, api *devapi.Client, appID string, asJSON bool, pipeTo, code string) error {
	code, err := a.identityCode(ctx, api, "rotate_app_secret", "app", appID, code)
	if err != nil {
		return err
	}
	var rotated struct {
		ClientSecret string `json:"client_secret"`
		// Absent when the app had no secret before, so there is no overlap.
		PreviousSecretExpiresAt *time.Time `json:"previous_secret_expires_at"`
	}
	if err := api.Do(ctx, http.MethodPost, "/apps/"+url.PathEscape(appID)+"/regenerate-secret", map[string]string{"otp_code": code}, &rotated); err != nil {
		return appAccess(err)
	}
	secret := rotated.ClientSecret
	if secret == "" {
		return errors.New("the API did not return the new client secret")
	}
	expires := rotated.PreviousSecretExpiresAt
	if pipeTo != "" {
		afterFailure := "The new one was not shown; run this command again to issue another"
		if expires != nil {
			afterFailure = fmt.Sprintf("The new one was not shown. Until %s, the client secret that was current before this command ran keeps working; if that secret came from an earlier rotation you never deployed, your app is not using it. Running this command again stops it at once, so do it only when you can deploy the next secret right away", formatExpiry(*expires))
		}
		if err := a.storeSecret(ctx, "client secret", secret, pipeTo, afterFailure); err != nil {
			return err
		}
		a.printSecretOverlap(expires)
		return nil
	}
	if asJSON {
		return json.NewEncoder(a.stdout).Encode(map[string]any{"app_id": appID, "client_secret": secret, "previous_secret_expires_at": expires})
	}
	a.printf("Client secret:  %s\n\n", secret)
	a.println("Store it now; it is shown only once.")
	a.printSecretOverlap(expires)
	return nil
}

// printSecretOverlap says until when the previous client secret still works.
func (a *app) printSecretOverlap(expires *time.Time) {
	if expires == nil {
		return
	}
	a.printf("Until %s, the previous client secret keeps working too. Once the new secret is deployed, run `tringify app secret finish-rotation` to stop it sooner.\n", formatExpiry(*expires))
}

// appSecretFinishRotation revokes the previous client secret before its 72
// hours are up.
func (a *app) appSecretFinishRotation(ctx context.Context, args []string) error {
	fs := newFlags("app secret finish-rotation [--app ID] [--yes]", "Stop accepting the previous client secret now instead of when its 72 hours end. Run this once the new secret is deployed everywhere, or right away if the previous secret was exposed.")
	appFlag := fs.String("app", "", "app ID (default: app_id in "+appconfig.FileName+")")
	file := fs.String("file", appconfig.FileName, "configuration file")
	yes := fs.Bool("yes", false, "revoke without asking")
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
	var details struct {
		Rotation struct {
			InProgress              bool       `json:"in_progress"`
			PreviousSecretExpiresAt *time.Time `json:"previous_secret_expires_at"`
		} `json:"client_secret_rotation"`
	}
	if err := api.Do(ctx, http.MethodGet, "/apps/"+url.PathEscape(appID), nil, &details); err != nil {
		return appAccess(err)
	}
	if !details.Rotation.InProgress {
		a.println("No client secret rotation is in progress; only the current secret works.")
		return nil
	}
	if !*yes {
		question := "Stop accepting the previous client secret now?"
		if details.Rotation.PreviousSecretExpiresAt != nil {
			question = fmt.Sprintf("The previous client secret keeps working until %s. Stop accepting it now?", formatExpiry(*details.Rotation.PreviousSecretExpiresAt))
		}
		ok, err := confirmWith(a.stdin, a.stdout, question, "Pass --yes to revoke without confirming")
		if err != nil || !ok {
			if err == nil {
				a.println("Previous secret not revoked.")
			}
			return err
		}
	}
	if err := api.Do(ctx, http.MethodPost, "/apps/"+url.PathEscape(appID)+"/finish-secret-rotation", nil, nil); err != nil {
		return appAccess(err)
	}
	a.println("Revoked the previous client secret. Only the current secret works now.")
	return nil
}

// storeSecret hands a newly issued secret to the developer's own secret store
// command on its standard input, so it never appears on screen. Any host
// works: wrangler, gcloud, fly, vercel, a password manager. The command runs
// in the system shell so it can be written as it would be typed. afterFailure
// tells the developer where they stand if the command fails.
//
// The command's output is never forwarded as is, since a command that echoes
// its input would print the secret. Its standard output is discarded; if it
// fails, its standard error is shown with the secret redacted.
func (a *app) storeSecret(ctx context.Context, what, secret, command, afterFailure string) error {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/C", command)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", command)
	}
	var childErr bytes.Buffer
	cmd.Stdin = strings.NewReader(secret)
	cmd.Stderr = &childErr
	if err := cmd.Run(); err != nil {
		if out := strings.TrimSpace(strings.ReplaceAll(childErr.String(), secret, "[redacted]")); out != "" {
			fmt.Fprintf(a.stderr, "%s\n", out)
		}
		return fmt.Errorf("a new %s was issued, but %q failed (%v). %s", what, command, err, afterFailure)
	}
	a.printf("Issued a new %s and stored it with: %s\n", what, command)
	return nil
}
