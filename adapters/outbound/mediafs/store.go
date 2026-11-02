// Package mediafs is the filesystem media store (ADR 0036): it downloads
// images that handlers want to attach to a message into one directory, names
// them after their content hash and deletes them once they are old enough.
package mediafs

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/publishing"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/netguard"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

const (
	// DefaultMaxBytes is the download cap when Config.MaxBytes is not set.
	DefaultMaxBytes int64 = 10 << 20
	// DefaultTimeout bounds one download when Config.Timeout is not set.
	DefaultTimeout = 20 * time.Second

	tempPrefix = ".tmp-"
)

var (
	// ErrHostNotAllowed reports a URL (or redirect target) whose host is not
	// in the allow-list.
	ErrHostNotAllowed = errors.New("mediafs: host not in the allow-list")
	// ErrNotImage reports a body whose content is not a supported image,
	// whatever the URL or the Content-Type header claim.
	ErrNotImage = errors.New("mediafs: content is not a supported image")
)

// imageTypes maps the sniffed media type to the stored file extension.
// Videos and everything else are rejected.
var imageTypes = map[string]string{
	"image/jpeg": ".jpg",
	"image/png":  ".png",
	"image/webp": ".webp",
}

// Config configures a Store.
type Config struct {
	// Dir is the directory files are written to; created on demand.
	Dir string
	// AllowedHosts are the only hosts images may come from (also checked on
	// every redirect hop). Defined by the host; it must not be empty.
	AllowedHosts []string
	// MaxBytes caps a download; zero means DefaultMaxBytes.
	MaxBytes int64
	// Timeout bounds one download; zero means DefaultTimeout.
	Timeout time.Duration
	// UserAgent is sent with every request; empty means netguard's default.
	UserAgent string
	// Policy is the SSRF address policy; the zero value is BlockPrivate.
	Policy netguard.HostPolicy
	// Transport replaces the netguard transport. For tests only: without the
	// dial-time address check, the URL checks are all that remain.
	Transport http.RoundTripper
	Logger    appout.Logger
}

// Stats are cumulative counters since the Store was created.
type Stats struct {
	Downloads    int64
	Reused       int64
	Rejects      int64
	BytesWritten int64
	Deleted      int64
}

// Store implements appout.EventMediaStore and appout.MediaSweeper.
type Store struct {
	dir     string
	hosts   map[string]struct{}
	max     int64
	policy  netguard.HostPolicy
	agent   string
	client  *http.Client
	logger  appout.Logger
	stats   struct{ downloads, reused, rejects, bytes, deleted atomic.Int64 }
	nowFunc func() time.Time
}

var (
	_ appout.EventMediaStore = (*Store)(nil)
	_ appout.MediaSweeper    = (*Store)(nil)
)

// New validates cfg and returns a Store.
func New(cfg Config) (*Store, error) {
	if strings.TrimSpace(cfg.Dir) == "" {
		return nil, errors.New("mediafs: dir is required")
	}
	hosts := make(map[string]struct{}, len(cfg.AllowedHosts))
	for _, h := range cfg.AllowedHosts {
		if h = normalizeHost(h); h != "" {
			hosts[h] = struct{}{}
		}
	}
	if len(hosts) == 0 {
		return nil, errors.New("mediafs: at least one allowed host is required")
	}
	if cfg.MaxBytes < 0 {
		return nil, errors.New("mediafs: max bytes must not be negative")
	}
	s := &Store{
		dir:     cfg.Dir,
		hosts:   hosts,
		max:     cfg.MaxBytes,
		policy:  cfg.Policy,
		agent:   cfg.UserAgent,
		logger:  cfg.Logger,
		nowFunc: time.Now,
	}
	if s.max == 0 {
		s.max = DefaultMaxBytes
	}
	if s.agent == "" {
		s.agent = netguard.DefaultUserAgent
	}
	rt := cfg.Transport
	if rt == nil {
		rt = netguard.NewTransport(cfg.Policy)
	}
	s.client = &http.Client{
		Timeout:       netguard.TimeoutOr(cfg.Timeout, DefaultTimeout),
		Transport:     rt,
		CheckRedirect: s.checkRedirect,
	}
	return s, nil
}

func normalizeHost(h string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(h)), ".")
}

// Stats returns the cumulative counters.
func (s *Store) Stats() Stats {
	return Stats{
		Downloads:    s.stats.downloads.Load(),
		Reused:       s.stats.reused.Load(),
		Rejects:      s.stats.rejects.Load(),
		BytesWritten: s.stats.bytes.Load(),
		Deleted:      s.stats.deleted.Load(),
	}
}

// validate applies the URL policy and the host allow-list.
func (s *Store) validate(raw string) (string, error) {
	u, err := netguard.ValidateURL(raw, s.policy)
	if err != nil {
		return "", err
	}
	if _, ok := s.hosts[normalizeHost(u.Hostname())]; !ok {
		return "", fmt.Errorf("%w: %s", ErrHostNotAllowed, normalizeHost(u.Hostname()))
	}
	return u.String(), nil
}

func (s *Store) checkRedirect(req *http.Request, via []*http.Request) error {
	if err := netguard.CheckRedirect(req, via); err != nil {
		return err
	}
	_, err := s.validate(req.URL.String())
	return err
}

// Fetch implements appout.EventMediaStore. The returned path is relative to
// Dir and stable across restarts; fetching the same bytes again returns the
// same path and renews the file's age.
func (s *Store) Fetch(ctx context.Context, rawURL string) (publishing.ImageRef, error) {
	ref, err := s.fetch(ctx, rawURL)
	if err != nil {
		s.stats.rejects.Add(1)
		if s.logger != nil {
			s.logger.Warn("media fetch rejected", "url", netguard.RedactURL(rawURL), "error", err.Error(),
				"rejects", s.stats.rejects.Load())
		}
		return publishing.ImageRef{}, err
	}
	return ref, nil
}

func (s *Store) fetch(ctx context.Context, rawURL string) (publishing.ImageRef, error) {
	target, err := s.validate(rawURL)
	if err != nil {
		return publishing.ImageRef{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return publishing.ImageRef{}, netguard.RedactError(err)
	}
	req.Header.Set("User-Agent", s.agent)
	req.Header.Set("Accept", "image/jpeg,image/png,image/webp")
	resp, err := s.client.Do(req)
	if err != nil {
		return publishing.ImageRef{}, fmt.Errorf("mediafs: download: %w", netguard.RedactError(err))
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return publishing.ImageRef{}, fmt.Errorf("mediafs: download: status %d", resp.StatusCode)
	}
	if resp.ContentLength > s.max {
		return publishing.ImageRef{}, netguard.BodyTooLarge(s.max)
	}
	data, err := netguard.ReadBody(resp.Body, s.max)
	if err != nil {
		return publishing.ImageRef{}, fmt.Errorf("mediafs: download: %w", netguard.RedactError(err))
	}
	// The real type comes from the bytes, not from the URL or the header.
	mime := strings.SplitN(http.DetectContentType(data), ";", 2)[0]
	ext, ok := imageTypes[mime]
	if !ok {
		return publishing.ImageRef{}, fmt.Errorf("%w: detected %s", ErrNotImage, mime)
	}
	sum := sha256.Sum256(data)
	name := hex.EncodeToString(sum[:]) + ext
	written, err := s.store(name, data)
	if err != nil {
		return publishing.ImageRef{}, err
	}
	if written {
		s.stats.downloads.Add(1)
		s.stats.bytes.Add(int64(len(data)))
	} else {
		s.stats.reused.Add(1)
	}
	if s.logger != nil {
		s.logger.Info("media fetched", "path", name, "bytes", len(data), "new", written,
			"downloads", s.stats.downloads.Load(), "bytes_total", s.stats.bytes.Load())
	}
	return publishing.ImageRef{Path: name, MIME: mime}, nil
}

// store writes data as name unless the file exists, in which case it only
// renews the file's age. It reports whether bytes were written.
func (s *Store) store(name string, data []byte) (bool, error) {
	if err := os.MkdirAll(s.dir, 0o750); err != nil {
		return false, fmt.Errorf("mediafs: create dir: %w", err)
	}
	root, err := os.OpenRoot(s.dir)
	if err != nil {
		return false, fmt.Errorf("mediafs: open dir: %w", err)
	}
	defer func() { _ = root.Close() }()

	if info, err := root.Stat(name); err == nil && info.Mode().IsRegular() && info.Size() == int64(len(data)) {
		now := s.nowFunc()
		if err := root.Chtimes(name, now, now); err != nil {
			return false, fmt.Errorf("mediafs: renew %s: %w", name, err)
		}
		return false, nil
	}
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return false, fmt.Errorf("mediafs: temp name: %w", err)
	}
	tmp := tempPrefix + hex.EncodeToString(nonce[:])
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
	if err != nil {
		return false, fmt.Errorf("mediafs: create temp file: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = root.Remove(tmp)
		return false, fmt.Errorf("mediafs: write: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = root.Remove(tmp)
		return false, fmt.Errorf("mediafs: write: %w", err)
	}
	if err := root.Rename(tmp, name); err != nil {
		_ = root.Remove(tmp)
		return false, fmt.Errorf("mediafs: publish file: %w", err)
	}
	return true, nil
}

// DeleteOlderThan implements appout.MediaSweeper. Leftover temp files from an
// interrupted download are removed under the same age rule; they are never
// referenced.
func (s *Store) DeleteOlderThan(ctx context.Context, cutoff time.Time, keep map[string]struct{}) (appout.MediaSweepResult, error) {
	var res appout.MediaSweepResult
	entries, err := os.ReadDir(s.dir)
	if errors.Is(err, fs.ErrNotExist) {
		return res, nil
	}
	if err != nil {
		return res, fmt.Errorf("mediafs: list dir: %w", err)
	}
	root, err := os.OpenRoot(s.dir)
	if err != nil {
		return res, fmt.Errorf("mediafs: open dir: %w", err)
	}
	defer func() { _ = root.Close() }()

	kept := make(map[string]struct{}, len(keep))
	for k := range keep {
		kept[path.Clean(filepath.ToSlash(k))] = struct{}{}
	}
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		if !e.Type().IsRegular() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue // vanished between ReadDir and Info
		}
		if !info.ModTime().Before(cutoff) {
			continue
		}
		if _, ok := kept[e.Name()]; ok && !strings.HasPrefix(e.Name(), tempPrefix) {
			res.Protected++
			continue
		}
		if err := root.Remove(e.Name()); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return res, fmt.Errorf("mediafs: delete %s: %w", e.Name(), err)
		}
		res.Deleted++
		res.BytesDeleted += info.Size()
	}
	s.stats.deleted.Add(int64(res.Deleted))
	if s.logger != nil {
		s.logger.Info("media swept", "deleted", res.Deleted, "bytes_deleted", res.BytesDeleted,
			"protected", res.Protected, "deleted_total", s.stats.deleted.Load())
	}
	return res, nil
}
