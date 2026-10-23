// Package download retrieves theme packages from short-lived R2 links.
package download

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"time"
)

const maxPackageBytes = 256 << 20

// Link is the gateway's theme export response. Authentication is in the URL;
// account credentials must never be sent to the object store.
type Link struct {
	DownloadURL string `json:"download_url"`
	Filename    string `json:"filename"`
	ExpiresIn   int64  `json:"expires_in"`
	SizeBytes   int64  `json:"size_bytes"`
	SHA256      string `json:"sha256"`
}

func Get(ctx context.Context, client *http.Client, link Link) ([]byte, string, error) {
	u, err := url.Parse(link.DownloadURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || link.Filename == "" {
		return nil, "", errors.New("the server returned an invalid theme download link")
	}
	if link.SizeBytes <= 0 || link.SizeBytes > maxPackageBytes {
		return nil, "", errors.New("the theme package size must be between 1 byte and 256 MiB")
	}
	// R2's signature timestamp accounts for time already spent since issuance.
	signedAt, err := time.Parse("20060102T150405Z", u.Query().Get("X-Amz-Date"))
	ttl, ttlErr := strconv.ParseInt(u.Query().Get("X-Amz-Expires"), 10, 64)
	if err != nil || ttlErr != nil || ttl <= 0 || ttl > 604800 || link.ExpiresIn <= 0 || link.ExpiresIn > 604800 {
		return nil, "", errors.New("the server returned an invalid theme download expiry")
	}
	deadline := signedAt.Add(time.Duration(ttl) * time.Second)
	if !time.Now().Before(deadline) {
		return nil, "", errors.New("the theme download link has expired; run the command again")
	}
	if relative := time.Now().Add(time.Duration(link.ExpiresIn) * time.Second); relative.Before(deadline) {
		deadline = relative
	}
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, link.DownloadURL, nil)
	if err != nil {
		return nil, "", errors.New("could not create the theme download request")
	}
	// Do not forward a signed URL or any credentials through redirects.
	c := *client
	c.Jar = nil
	c.Timeout = 10 * time.Minute
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := c.Do(req)
	if err != nil {
		return nil, "", errors.New("the theme package download failed; run the command again for a fresh link")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("the theme package download returned HTTP %d; run the command again for a fresh link", resp.StatusCode)
	}
	if resp.ContentLength > maxPackageBytes {
		return nil, "", errors.New("the theme package is too large")
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, link.SizeBytes+1))
	if err != nil {
		return nil, "", errors.New("could not read the theme package")
	}
	if int64(len(raw)) != link.SizeBytes {
		return nil, "", errors.New("the theme package size does not match the download link")
	}
	if link.SHA256 != "" {
		digest := sha256.Sum256(raw)
		if hex.EncodeToString(digest[:]) != link.SHA256 {
			return nil, "", errors.New("the theme package failed its integrity check")
		}
	}
	return raw, filepath.Base(link.Filename), nil
}
