// Package mcp calls tools on Tringify's MCP endpoints over streamable HTTP
// (stateless, JSON responses).
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

// TokenSource supplies a current access token and can force a refresh.
type TokenSource interface {
	AccessToken(ctx context.Context) (string, error)
	ForceRefresh(ctx context.Context) (string, error)
}

// Client calls tools on one MCP endpoint.
type Client struct {
	Endpoint string
	Tokens   TokenSource
	HTTP     *http.Client
	Version  string
	id       atomic.Int64
}

func New(endpoint string, tokens TokenSource, version string) *Client {
	return &Client{Endpoint: endpoint, Tokens: tokens, HTTP: &http.Client{Timeout: 2 * time.Minute}, Version: version}
}

// ToolError is a failure reported by a tool.
type ToolError struct {
	Code    string
	Message string
}

func (e *ToolError) Error() string {
	if e.Code != "" {
		return e.Message + " (" + e.Code + ")"
	}
	return e.Message
}

// ErrInsufficientScope means the login lacks the access the tool needs.
var ErrInsufficientScope = errors.New("this login does not include the access needed; sign in again to grant it")

// ErrUnauthorized means the token was refused.
var ErrUnauthorized = errors.New("the login is no longer valid; sign in again")

func (c *Client) post(ctx context.Context, body any) (map[string]any, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	for attempt := 0; attempt < 2; attempt++ {
		var token string
		if attempt == 0 {
			token, err = c.Tokens.AccessToken(ctx)
		} else {
			token, err = c.Tokens.ForceRefresh(ctx)
		}
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Endpoint, bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("MCP-Protocol-Version", "2025-11-25")
		req.Header.Set("User-Agent", "tringify-cli/"+c.Version)
		resp, err := c.HTTP.Do(req)
		if err != nil {
			return nil, err
		}
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		switch {
		case resp.StatusCode == http.StatusUnauthorized && attempt == 0:
			continue
		case resp.StatusCode == http.StatusUnauthorized:
			return nil, ErrUnauthorized
		case resp.StatusCode == http.StatusForbidden && strings.Contains(resp.Header.Get("WWW-Authenticate"), "insufficient_scope"):
			return nil, ErrInsufficientScope
		case resp.StatusCode == http.StatusTooManyRequests:
			return nil, errors.New("rate limited; wait a moment and try again")
		case resp.StatusCode != http.StatusOK:
			return nil, fmt.Errorf("MCP request failed (HTTP %d)", resp.StatusCode)
		}
		var out map[string]any
		if err := json.Unmarshal(raw, &out); err != nil {
			return nil, fmt.Errorf("unexpected MCP response")
		}
		if e, ok := out["error"].(map[string]any); ok {
			return nil, &ToolError{Message: fmt.Sprint(e["message"])}
		}
		return out, nil
	}
	return nil, ErrUnauthorized
}

// Call invokes a tool and returns its "data" on success.
func (c *Client) Call(ctx context.Context, name string, args map[string]any) (any, error) {
	if args == nil {
		args = map[string]any{}
	}
	out, err := c.post(ctx, map[string]any{"jsonrpc": "2.0", "id": c.id.Add(1), "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
	if err != nil {
		return nil, err
	}
	result, _ := out["result"].(map[string]any)
	if result == nil {
		return nil, fmt.Errorf("tool %s returned no result", name)
	}
	structured, _ := result["structuredContent"].(map[string]any)
	if structured == nil {
		// Fall back to the text content, which carries the same JSON.
		if content, ok := result["content"].([]any); ok && len(content) > 0 {
			if first, ok := content[0].(map[string]any); ok {
				_ = json.Unmarshal([]byte(fmt.Sprint(first["text"])), &structured)
			}
		}
	}
	if structured == nil {
		return nil, fmt.Errorf("tool %s returned an unexpected result", name)
	}
	if isErr, _ := result["isError"].(bool); isErr || structured["success"] == false {
		e, _ := structured["error"].(map[string]any)
		if e == nil {
			return nil, &ToolError{Message: "the request failed"}
		}
		message, _ := e["message"].(string)
		code, _ := e["code"].(string)
		if message == "" {
			message = "the request failed"
		}
		return nil, &ToolError{Code: code, Message: message}
	}
	if data, ok := structured["data"]; ok {
		return data, nil
	}
	return structured, nil
}
