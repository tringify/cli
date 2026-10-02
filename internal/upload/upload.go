// Package upload sends a bundle to the short-lived URL returned by an upload
// intent and waits for it to be validated.
package upload

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// Grant is what an upload intent returns.
type Grant struct {
	IntentID string            `json:"intent_id"`
	Status   string            `json:"status"`
	URL      string            `json:"url"`
	Headers  map[string]string `json:"headers"`
}

// Status is an upload's validation state.
type Status struct {
	IntentID string `json:"intent_id"`
	Status   string `json:"status"`
	Artifact *struct {
		UploadID  string `json:"upload_id"`
		SizeBytes int64  `json:"size_bytes"`
	} `json:"artifact"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// Put sends the file to the grant URL with the headers it requires. The URL
// is signed; no Tringify credential is attached.
func Put(ctx context.Context, client *http.Client, grant Grant, path string) error {
	if !strings.HasPrefix(grant.URL, "https://") {
		return errors.New("the upload URL is not HTTPS")
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, grant.URL, f)
	if err != nil {
		return err
	}
	req.ContentLength = info.Size()
	for k, v := range grant.Headers {
		req.Header.Set(k, v)
	}
	if req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/zip")
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("upload failed (HTTP %d)", resp.StatusCode)
	}
	return nil
}

// Wait polls until the upload is ready or rejected.
func Wait(ctx context.Context, poll func(context.Context) (*Status, error)) (string, error) {
	deadline := time.Now().Add(5 * time.Minute)
	delay := time.Second
	for {
		s, err := poll(ctx)
		if err != nil {
			return "", err
		}
		switch s.Status {
		case "ready":
			if s.Artifact == nil || s.Artifact.UploadID == "" {
				return "", errors.New("the upload is ready but has no upload ID")
			}
			return s.Artifact.UploadID, nil
		case "rejected", "expired":
			if s.Error != nil && s.Error.Message != "" {
				return "", fmt.Errorf("the bundle was rejected: %s", s.Error.Message)
			}
			return "", fmt.Errorf("the upload was %s", s.Status)
		}
		if time.Now().After(deadline) {
			return "", errors.New("timed out waiting for the bundle to be validated")
		}
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return "", ctx.Err()
		}
		if delay < 5*time.Second {
			delay += time.Second
		}
	}
}
