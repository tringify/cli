// Package devapi calls the Tringify Developer API with a developer
// organization login.
package devapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/tringify/cli/internal/mcp"
)

// Client calls the Developer API for one organization.
type Client struct {
	Base           string
	OrganizationID string
	Tokens         mcp.TokenSource
	HTTP           *http.Client
	Version        string
}

func New(base, organizationID string, tokens mcp.TokenSource, version string) *Client {
	return &Client{Base: base, OrganizationID: organizationID, Tokens: tokens, HTTP: &http.Client{Timeout: 2 * time.Minute}, Version: version}
}

// Error is an error returned by the API.
type Error struct {
	Status  int
	Code    string
	Message string
	// Detail is the API's explanation of a validation failure, when it gives one.
	Detail string
}

func (e *Error) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("request failed (HTTP %d)", e.Status)
	}
	message := e.Message
	if e.Detail != "" && e.Detail != e.Message {
		message += " " + e.Detail
	}
	if e.Code != "" {
		return message + " (" + e.Code + ")"
	}
	return message
}

// Do sends a request and decodes the "data" field of a successful reply.
func (c *Client) Do(ctx context.Context, method, path string, body any, out any) error {
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return err
		}
	}
	return c.send(ctx, method, path, payload, "application/json", func(resp *http.Response) error {
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		if err != nil || len(raw) == 0 {
			return err
		}
		var envelope struct {
			Success *bool           `json:"success"`
			Data    json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil {
			return fmt.Errorf("the Developer API returned an unreadable reply: %w", err)
		}
		if envelope.Success != nil && !*envelope.Success {
			return apiError(resp.StatusCode, raw)
		}
		if out == nil || len(envelope.Data) == 0 {
			return nil
		}
		return json.Unmarshal(envelope.Data, out)
	})
}

// Download fetches a binary reply, such as a theme package, with its headers.
// Errors are reported the same way as Do's.
func (c *Client) Download(ctx context.Context, path string, limit int64) ([]byte, http.Header, error) {
	var raw []byte
	var header http.Header
	err := c.send(ctx, http.MethodGet, path, nil, "application/zip", func(resp *http.Response) error {
		var err error
		if raw, err = io.ReadAll(io.LimitReader(resp.Body, limit+1)); err != nil {
			return err
		}
		if int64(len(raw)) > limit {
			return fmt.Errorf("the download is larger than %d MB", limit>>20)
		}
		header = resp.Header
		return nil
	})
	return raw, header, err
}

// send makes the request with a fresh access token, retrying once with a
// refreshed token on 401, and hands a successful (2xx) response to read.
func (c *Client) send(ctx context.Context, method, path string, payload []byte, accept string, read func(*http.Response) error) error {
	for attempt := 0; attempt < 2; attempt++ {
		var token string
		var err error
		if attempt == 0 {
			token, err = c.Tokens.AccessToken(ctx)
		} else {
			token, err = c.Tokens.ForceRefresh(ctx)
		}
		if err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, method, c.Base+"/api/v1"+path, bytes.NewReader(payload))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-Organization-ID", c.OrganizationID)
		req.Header.Set("Accept", accept)
		req.Header.Set("User-Agent", "tringify-cli/"+c.Version)
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := c.HTTP.Do(req)
		if err != nil {
			return err
		}
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			resp.Body.Close()
			continue
		}
		if resp.StatusCode >= 400 {
			raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			resp.Body.Close()
			e := apiError(resp.StatusCode, raw)
			if resp.StatusCode == http.StatusUnauthorized {
				return errors.Join(mcp.ErrUnauthorized, e)
			}
			return e
		}
		err = read(resp)
		resp.Body.Close()
		return err
	}
	return mcp.ErrUnauthorized
}

// apiError reads the API's error reply: {"error":{"code","message"}} or a
// top-level code and message.
func apiError(status int, raw []byte) *Error {
	var envelope struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Details any    `json:"details"`
		Error   *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Details any    `json:"details"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &envelope)
	e := &Error{Status: status, Code: envelope.Code, Message: envelope.Message}
	details := envelope.Details
	if envelope.Error != nil {
		e.Code, e.Message, details = envelope.Error.Code, envelope.Error.Message, envelope.Error.Details
	}
	// Only a text explanation is shown; structured details stay out of the
	// one-line error.
	if text, ok := details.(string); ok {
		e.Detail = text
	}
	return e
}
