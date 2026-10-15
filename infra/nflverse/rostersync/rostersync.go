// Package rostersync downloads the nflverse roster file. It lives apart from
// package nflverse (the pure parser the ingestion binary may import) so the
// network code stays offline-tooling-only.
package rostersync

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/infra/netguard"
)

// maxRosterBytes bounds a downloaded roster file (the real one is ~1 MB).
const maxRosterBytes = 16 << 20

// defaultTimeout applies when the caller's client has none.
const defaultTimeout = 60 * time.Second

// guardedClient returns a copy of client that always has a timeout and the
// netguard redirect policy (at most 5 redirects, no https->http). A nil
// client becomes a guarded default whose transport refuses link-local
// destinations.
func guardedClient(client *http.Client) *http.Client {
	if client == nil {
		return netguard.NewClient(defaultTimeout, defaultTimeout, netguard.AllowPrivate)
	}
	c := *client
	c.Timeout = netguard.TimeoutOr(c.Timeout, defaultTimeout)
	if c.CheckRedirect == nil {
		c.CheckRedirect = netguard.CheckRedirect
	}
	return &c
}

// Download fetches url into dest atomically (temp file + rename). Any
// failure leaves dest untouched. When validate is non-nil it runs on the
// temp file before the rename, so a 200 response with a garbage body (an
// HTML error page, say) can never replace a good file. url must be http or
// https without credentials and must not be link-local; the file is
// written 0600 and a missing parent directory is created 0700. Errors
// never carry URL query values.
func Download(ctx context.Context, client *http.Client, url, dest string, validate func(path string) error) error {
	if _, err := netguard.ValidateURL(url, netguard.AllowPrivate); err != nil {
		return fmt.Errorf("rostersync: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("rostersync: build request: %w", netguard.RedactError(err))
	}
	req.Header.Set("User-Agent", netguard.DefaultUserAgent)
	resp, err := guardedClient(client).Do(req)
	if err != nil {
		return fmt.Errorf("rostersync: download: %w", netguard.RedactError(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("rostersync: download returned HTTP %d", resp.StatusCode)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return fmt.Errorf("rostersync: create dir: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".roster-*.tmp")
	if err != nil {
		return fmt.Errorf("rostersync: temp file: %w", err)
	}
	defer os.Remove(tmp.Name())
	n, err := io.Copy(tmp, io.LimitReader(resp.Body, maxRosterBytes+1))
	closeErr := tmp.Close()
	if err != nil {
		return errors.Join(fmt.Errorf("rostersync: write: %w", err), closeErr)
	}
	if n > maxRosterBytes {
		return errors.Join(fmt.Errorf("rostersync: roster file exceeds %d bytes: %w", maxRosterBytes, netguard.ErrBodyTooLarge), closeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("rostersync: close: %w", closeErr)
	}
	if validate != nil {
		if err := validate(tmp.Name()); err != nil {
			return fmt.Errorf("rostersync: downloaded file rejected: %w", err)
		}
	}
	return os.Rename(tmp.Name(), dest)
}
