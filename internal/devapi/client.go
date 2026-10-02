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
}

func (e *Error) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("request failed (HTTP %d)", e.Status)
	}
	if e.Code != "" {
		return e.Message + " (" + e.Code + ")"
	}
	return e.Message
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
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", "tringify-cli/"+c.Version)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := c.HTTP.Do(req)
		if err != nil {
			return err
		}
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if err != nil {
			return err
		}
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			continue
		}
		var envelope struct {
			Success *bool           `json:"success"`
			Data    json.RawMessage `json:"data"`
			Code    string          `json:"code"`
			Message string          `json:"message"`
			Error   *struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(raw, &envelope)
		if resp.StatusCode >= 400 || (envelope.Success != nil && !*envelope.Success) {
			e := &Error{Status: resp.StatusCode, Code: envelope.Code, Message: envelope.Message}
			if envelope.Error != nil {
				e.Code, e.Message = envelope.Error.Code, envelope.Error.Message
			}
			if resp.StatusCode == http.StatusUnauthorized {
				return errors.Join(mcp.ErrUnauthorized, e)
			}
			return e
		}
		if out == nil || len(envelope.Data) == 0 {
			return nil
		}
		return json.Unmarshal(envelope.Data, out)
	}
	return mcp.ErrUnauthorized
}
