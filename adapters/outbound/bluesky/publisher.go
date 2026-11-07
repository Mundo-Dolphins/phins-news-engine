package bluesky

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/publishing"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/netguard"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

const (
	// DefaultHost is the production PDS entryway that accepts logins and
	// writes.
	DefaultHost = "https://bsky.social"

	maxPostGraphemes = 300
	maxPostBytes     = 3000
	postCollection   = "app.bsky.feed.post"
	postLang         = "es"
	// createdAtLayout is the datetime format of the lexicon, in UTC.
	createdAtLayout = "2006-01-02T15:04:05.000Z"
)

// Config configures a Publisher. The host supplies every value; the adapter
// reads nothing from the environment.
type Config struct {
	// Handle and AppPassword log in as the posting account. Use an app
	// password, never the account password. Neither is ever logged.
	Handle      string
	AppPassword string
	// Host is the PDS that takes logins and writes; empty means DefaultHost.
	Host string
	// AppViewHost serves the author feed used by reconciliation; empty means
	// the public AppView.
	AppViewHost string
	// MediaRoot is the media store directory that payload image paths are
	// resolved against. Without it a payload with images is a permanent
	// failure.
	MediaRoot string
	// Timeout bounds each HTTP call; non-positive means 30s.
	Timeout time.Duration
	// MaxResponseBytes caps every response body; non-positive means
	// netguard.JSONBodyLimit.
	MaxResponseBytes int64
	// Now overrides the clock; nil means time.Now. Tests use it.
	Now func() time.Time
}

// Publisher posts text and images to one Bluesky account (ADR 0035). It
// implements out.ThreadPublisher and out.DestinationReconciler, and is safe
// for concurrent use.
type Publisher struct {
	cfg  Config
	http *http.Client
	feed *Client

	mu   sync.Mutex
	sess *session
}

var (
	_ appout.PayloadPublisher      = (*Publisher)(nil)
	_ appout.ThreadPublisher       = (*Publisher)(nil)
	_ appout.DestinationReconciler = (*Publisher)(nil)
)

// NewPublisher validates cfg and builds a Publisher. It makes no network
// call; the first publish logs in.
func NewPublisher(cfg Config) (*Publisher, error) {
	if strings.TrimSpace(cfg.Handle) == "" || cfg.AppPassword == "" {
		return nil, errors.New("bluesky: publisher needs a handle and an app password")
	}
	if cfg.Host == "" {
		cfg.Host = DefaultHost
	}
	if cfg.AppViewHost == "" {
		cfg.AppViewHost = defaultHost
	}
	for _, h := range []string{cfg.Host, cfg.AppViewHost} {
		if _, err := netguard.ValidateURL(h, netguard.AllowPrivate); err != nil {
			return nil, fmt.Errorf("bluesky: host: %w", err)
		}
	}
	cfg.Host = strings.TrimRight(cfg.Host, "/")
	if cfg.MaxResponseBytes <= 0 {
		cfg.MaxResponseBytes = netguard.JSONBodyLimit
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}

	tr := netguard.NewTransport(netguard.AllowPrivate)
	tr.Proxy = nil
	tr.MaxIdleConnsPerHost = 2
	tr.IdleConnTimeout = 90 * time.Second
	timeout := netguard.TimeoutOr(cfg.Timeout, defaultTimeout)
	feed := NewClient(timeout, cfg.MaxResponseBytes)
	feed.Host = strings.TrimRight(cfg.AppViewHost, "/")
	return &Publisher{
		cfg: cfg,
		http: &http.Client{
			Timeout:       timeout,
			Transport:     tr,
			CheckRedirect: netguard.CheckRedirect,
		},
		feed: feed,
	}, nil
}

// Publish posts the article's title and canonical URL.
func (p *Publisher) Publish(ctx context.Context, a article.Article) (string, error) {
	return p.PublishPayload(ctx, a, publishing.MessagePayload{})
}

// PublishPayload posts payload's text and images (or, for an empty payload,
// the article's title and URL) and returns the post's at:// URI.
//
// Every check that can fail without a network call runs first, so a post that
// can never succeed fails as PermanentError before anything is uploaded. Only
// the final createRecord call can be ambiguous: a login or an image upload
// that fails leaves nothing posted and is an ordinary temporary error.
func (p *Publisher) PublishPayload(ctx context.Context, a article.Article, payload publishing.MessagePayload) (string, error) {
	ref, err := p.publishPost(ctx, a, payload)
	return ref.URI, err
}

// publishPost is PublishPayload returning the post's full ref.
func (p *Publisher) publishPost(ctx context.Context, a article.Article, payload publishing.MessagePayload) (publishing.PostRef, error) {
	text, err := postText(a, payload)
	if err != nil {
		return publishing.PostRef{}, &appout.PermanentError{Err: fmt.Errorf("bluesky: publish %s: %w", a.ID, err)}
	}
	if len(payload.Images) > maxImages {
		return publishing.PostRef{}, &appout.PermanentError{Err: fmt.Errorf("bluesky: publish %s: %d images, the limit is %d", a.ID, len(payload.Images), maxImages)}
	}
	images := make([]preparedImage, 0, len(payload.Images))
	for _, ref := range payload.Images {
		img, err := loadImage(p.cfg.MediaRoot, ref)
		if err != nil {
			return publishing.PostRef{}, fmt.Errorf("bluesky: publish %s: %w", a.ID, err)
		}
		images = append(images, img)
	}

	embeds := make([]embedImage, 0, len(images))
	for _, img := range images {
		blob, err := p.uploadBlob(ctx, img)
		if err != nil {
			return publishing.PostRef{}, fmt.Errorf("bluesky: publish %s: %w", a.ID, err)
		}
		e := embedImage{Alt: img.alt, Image: blob}
		if img.width > 0 && img.height > 0 {
			e.AspectRatio = &aspectRatio{Width: img.width, Height: img.height}
		}
		embeds = append(embeds, e)
	}

	rec := p.newRecord(text)
	if len(embeds) > 0 {
		rec.Embed = &embedImages{Type: "app.bsky.embed.images", Images: embeds}
	}
	ref, err := p.createRecord(ctx, rec)
	if err != nil {
		return publishing.PostRef{}, fmt.Errorf("bluesky: publish %s: %w", a.ID, err)
	}
	return ref, nil
}

// PublishThread posts payload.Parts() as a reply chain: every part after the
// first replies to the previous one, and all share the first as root. done
// holds the parts already posted, which are skipped; record is called after
// each newly posted part and its error stops the thread. It returns the first
// part's URI.
//
// Every part is checked before anything is posted, so a part that can never
// be posted fails as PermanentError with nothing sent. Each createRecord
// keeps the single-post ambiguity rules, and only confirmed posts are
// recorded.
func (p *Publisher) PublishThread(ctx context.Context, a article.Article, payload publishing.MessagePayload,
	done []publishing.PostRef, record func(publishing.PostRef) error) (string, error) {
	parts := payload.Parts()
	if len(done) > len(parts) {
		return "", &appout.PermanentError{Err: fmt.Errorf("bluesky: publish %s: %d parts already posted, the thread has %d", a.ID, len(done), len(parts))}
	}
	if len(done) == len(parts) {
		return done[0].URI, nil
	}
	if len(parts) > 1 && len(payload.Images) > 0 {
		return "", &appout.PermanentError{Err: fmt.Errorf("bluesky: publish %s: a thread cannot carry images", a.ID)}
	}
	if len(parts) == 1 {
		// A single post: images and all, exactly like PublishPayload.
		ref, err := p.publishPost(ctx, a, payload)
		if err != nil {
			return "", err
		}
		if err := record(ref); err != nil {
			return "", fmt.Errorf("bluesky: publish %s: %w", a.ID, err)
		}
		return ref.URI, nil
	}
	texts, err := threadTexts(a, payload)
	if err != nil {
		return "", &appout.PermanentError{Err: fmt.Errorf("bluesky: publish %s: %w", a.ID, err)}
	}

	posted := append([]publishing.PostRef(nil), done...)
	for i := len(done); i < len(texts); i++ {
		rec := p.newRecord(texts[i])
		if i > 0 {
			rec.Reply = &replyRef{Root: strongRef(posted[0]), Parent: strongRef(posted[i-1])}
		}
		ref, err := p.createRecord(ctx, rec)
		if err != nil {
			return "", fmt.Errorf("bluesky: publish %s: thread part %d: %w", a.ID, i, err)
		}
		if err := record(ref); err != nil {
			return "", fmt.Errorf("bluesky: publish %s: thread part %d: %w", a.ID, i, err)
		}
		posted = append(posted, ref)
	}
	return posted[0].URI, nil
}

// newRecord builds a text post with the account's language and the text's
// facets.
func (p *Publisher) newRecord(text string) postRecordOut {
	return postRecordOut{
		Type:      postCollection,
		Text:      text,
		CreatedAt: p.cfg.Now().UTC().Format(createdAtLayout),
		Langs:     []string{postLang},
		Facets:    buildFacets(text),
	}
}

// threadTexts returns the text of every part: the first is built like a
// single post (link included), the continuations are posted as they are.
// Each must fit the post limits.
func threadTexts(a article.Article, payload publishing.MessagePayload) ([]string, error) {
	first, err := postText(a, payload)
	if err != nil {
		return nil, err
	}
	texts := []string{first}
	for i, c := range payload.Continuations {
		if strings.TrimSpace(c) == "" {
			return nil, fmt.Errorf("thread part %d: nothing to post", i+1)
		}
		if err := checkLimits(c); err != nil {
			return nil, fmt.Errorf("thread part %d: %w", i+1, err)
		}
		texts = append(texts, c)
	}
	return texts, nil
}

// postText builds the post body and enforces Bluesky's limits. It never
// shortens text: over-limit text is an error.
func postText(a article.Article, payload publishing.MessagePayload) (string, error) {
	var text string
	if payload.IsEmpty() {
		text = strings.TrimSpace(a.Title)
		if a.CanonicalURL != "" {
			if text != "" {
				text += "\n\n"
			}
			text += a.CanonicalURL
		}
	} else {
		text = payload.Text
		if payload.Link != "" && !strings.Contains(text, payload.Link) {
			if strings.TrimSpace(text) != "" {
				text = strings.TrimRight(text, " \t\r\n") + "\n\n"
			}
			text += payload.Link
		}
	}
	if strings.TrimSpace(text) == "" && len(payload.Images) == 0 {
		return "", errors.New("nothing to post: no text and no images")
	}
	if err := checkLimits(text); err != nil {
		return "", err
	}
	return text, nil
}

// checkLimits enforces Bluesky's per-post grapheme and byte limits.
func checkLimits(text string) error {
	if n := countGraphemes(text); n > maxPostGraphemes {
		return fmt.Errorf("text is %d characters, the limit is %d", n, maxPostGraphemes)
	}
	if len(text) > maxPostBytes {
		return fmt.Errorf("text is %d bytes, the limit is %d", len(text), maxPostBytes)
	}
	return nil
}

// Wire types of the records written to the repo.
type postRecordOut struct {
	Type      string       `json:"$type"`
	Text      string       `json:"text"`
	CreatedAt string       `json:"createdAt"`
	Langs     []string     `json:"langs"`
	Facets    []postFacet  `json:"facets,omitempty"`
	Embed     *embedImages `json:"embed,omitempty"`
	Reply     *replyRef    `json:"reply,omitempty"`
}

// replyRef makes a post a reply: Root is the thread's first post, Parent the
// post it answers.
type replyRef struct {
	Root   postStrongRef `json:"root"`
	Parent postStrongRef `json:"parent"`
}

type postStrongRef struct {
	URI string `json:"uri"`
	CID string `json:"cid"`
}

func strongRef(r publishing.PostRef) postStrongRef { return postStrongRef{URI: r.URI, CID: r.CID} }

type embedImages struct {
	Type   string       `json:"$type"`
	Images []embedImage `json:"images"`
}

type embedImage struct {
	Alt         string          `json:"alt"`
	Image       json.RawMessage `json:"image"`
	AspectRatio *aspectRatio    `json:"aspectRatio,omitempty"`
}

type aspectRatio struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

// uploadBlob uploads one image and returns the blob reference to embed. A
// failure here posted nothing, so it is never ambiguous.
func (p *Publisher) uploadBlob(ctx context.Context, img preparedImage) (json.RawMessage, error) {
	r, _, err := p.authedPost(ctx, "com.atproto.repo.uploadBlob", img.mime, img.data)
	if err != nil {
		return nil, err
	}
	if r.status != http.StatusOK {
		return nil, statusError("com.atproto.repo.uploadBlob", r, p.cfg.Now())
	}
	var body struct {
		Blob json.RawMessage `json:"blob"`
	}
	if err := json.Unmarshal(r.body, &body); err != nil || len(body.Blob) == 0 {
		return nil, errors.New("bluesky: uploadBlob: unreadable response")
	}
	return body.Blob, nil
}

// createRecord writes the post. Once the request has been sent, a transport
// failure, an unreadable reply, or a gateway error with no API body leaves
// it unknown whether the post exists, so those are AmbiguousError (ADR 0030).
// A reply that parses as an API answer is a confirmed outcome. The returned
// ref carries the post's CID, which a reply to it needs.
func (p *Publisher) createRecord(ctx context.Context, rec postRecordOut) (publishing.PostRef, error) {
	const endpoint = "com.atproto.repo.createRecord"
	// The DID is part of the body, so resolve the session before marshalling.
	_, did, err := p.accessToken(ctx)
	if err != nil {
		return publishing.PostRef{}, err
	}
	body, err := json.Marshal(map[string]any{"repo": did, "collection": postCollection, "record": rec})
	if err != nil {
		return publishing.PostRef{}, fmt.Errorf("bluesky: marshal post: %w", err)
	}
	r, _, err := p.authedPost(ctx, endpoint, "application/json", body)
	if err != nil {
		var sent *transportError
		if errors.As(err, &sent) {
			return publishing.PostRef{}, &appout.AmbiguousError{Err: err}
		}
		return publishing.PostRef{}, err
	}
	if r.status != http.StatusOK {
		if r.status >= 500 && !isAPIError(r.body) {
			return publishing.PostRef{}, &appout.AmbiguousError{Err: fmt.Errorf("bluesky: HTTP %d for %s with no API error body", r.status, endpoint)}
		}
		return publishing.PostRef{}, statusError(endpoint, r, p.cfg.Now())
	}
	var created struct {
		URI string `json:"uri"`
		CID string `json:"cid"`
	}
	if err := json.Unmarshal(r.body, &created); err != nil || created.URI == "" || created.CID == "" {
		return publishing.PostRef{}, &appout.AmbiguousError{Err: fmt.Errorf("bluesky: unreadable %s response", endpoint)}
	}
	return publishing.PostRef{URI: created.URI, CID: created.CID}, nil
}

func isAPIError(body []byte) bool {
	var e apiErrorResponse
	return json.Unmarshal(body, &e) == nil && (e.Error != "" || e.Message != "")
}
