package publishing

import (
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"strings"
)

// ImageRef points at one image of a MessagePayload.
type ImageRef struct {
	// Path locates the image inside the media store. It must be non-empty and
	// relative; the publisher resolves it against the store's root.
	Path string `json:"path"`
	// MIME is the image media type, e.g. "image/jpeg".
	MIME string `json:"mime"`
	// Alt is the accessibility text. Whether it is mandatory is up to the
	// destination.
	Alt string `json:"alt,omitempty"`
}

// MessagePayload is the destination-agnostic content of one outbox row, for
// messages whose text and images do not derive from the article fields (ADR
// 0032). It lives in the publication_outbox.payload JSON column.
//
// A row whose payload carries neither Text nor Images (an old '{}' row, or the
// legacy Telegram {"title","canonical_url"} shape) is "empty": the publisher
// derives the message from the article, as before.
type MessagePayload struct {
	// Text is the message body. The destination validates its length.
	Text string `json:"text,omitempty"`
	// Images are attached to the message, in order.
	Images []ImageRef `json:"images,omitempty"`
	// Link is an optional absolute http(s) URL the message points to.
	Link string `json:"link,omitempty"`
	// Kind is a free-form label for the host's handler (e.g. "injury_report").
	Kind string `json:"kind,omitempty"`
	// Continuations are the texts that follow Text when the message does not
	// fit one post; a destination that supports threads posts Text and then
	// each continuation as a reply to the previous part. Text-only: a payload
	// with continuations carries no images.
	Continuations []string `json:"continuations,omitempty"`
}

// Parts returns Text followed by the continuations, in posting order.
func (p MessagePayload) Parts() []string {
	return append([]string{p.Text}, p.Continuations...)
}

// PostRef identifies one posted part of a thread.
type PostRef struct {
	URI string `json:"uri"`
	CID string `json:"cid"`
}

// WebURL is the bsky.app link of a Bluesky post URI
// (at://<did>/app.bsky.feed.post/<rkey>), or "" for any other URI.
func (r PostRef) WebURL() string {
	rest, ok := strings.CutPrefix(r.URI, "at://")
	if !ok {
		return ""
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] != "app.bsky.feed.post" || parts[2] == "" {
		return ""
	}
	return "https://bsky.app/profile/" + parts[0] + "/post/" + parts[2]
}

// ParseThreadProgress decodes the posted parts of a thread, in order. An
// empty string yields no parts.
func ParseThreadProgress(raw string) ([]PostRef, error) {
	if strings.TrimSpace(raw) == "" {
		return []PostRef{}, nil
	}
	var refs []PostRef
	if err := json.Unmarshal([]byte(raw), &refs); err != nil {
		return nil, fmt.Errorf("publishing: decode thread progress: %w", err)
	}
	if refs == nil {
		refs = []PostRef{}
	}
	return refs, nil
}

// MarshalThreadProgress encodes the posted parts of a thread. No parts
// encode as '[]'.
func MarshalThreadProgress(refs []PostRef) (string, error) {
	if refs == nil {
		refs = []PostRef{}
	}
	b, err := json.Marshal(refs)
	if err != nil {
		return "", fmt.Errorf("publishing: encode thread progress: %w", err)
	}
	return string(b), nil
}

// IsEmpty reports whether the payload carries no content of its own, so the
// publisher must derive the message from the article.
func (p MessagePayload) IsEmpty() bool {
	return strings.TrimSpace(p.Text) == "" && len(p.Images) == 0
}

// ParseMessagePayload decodes the outbox payload column. An empty string and
// '{}' yield the empty payload, and unknown fields (such as the legacy
// Telegram title/canonical_url) are ignored, so old rows keep decoding.
func ParseMessagePayload(raw string) (MessagePayload, error) {
	if strings.TrimSpace(raw) == "" {
		return MessagePayload{}, nil
	}
	var p MessagePayload
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return MessagePayload{}, fmt.Errorf("publishing: decode message payload: %w", err)
	}
	return p, nil
}

// Marshal encodes the payload for the outbox payload column. The empty
// payload encodes as '{}'.
func (p MessagePayload) Marshal() (string, error) {
	b, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("publishing: encode message payload: %w", err)
	}
	return string(b), nil
}

// Validate checks that a non-empty payload carries enough for a destination
// to publish it: every image has a relative media-store path and an image
// MIME type, and Link, when set, is an absolute http(s) URL. The empty
// payload is always valid. Text length and image limits are the
// destination's to enforce.
func (p MessagePayload) Validate() error {
	for i, img := range p.Images {
		if err := img.validate(); err != nil {
			return fmt.Errorf("publishing: payload image %d: %w", i, err)
		}
	}
	if p.Link != "" {
		u, err := url.Parse(p.Link)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("publishing: payload link %q must be an absolute http(s) URL", p.Link)
		}
	}
	if len(p.Continuations) > 0 {
		if strings.TrimSpace(p.Text) == "" {
			return fmt.Errorf("publishing: payload has continuations but no text")
		}
		if len(p.Images) > 0 {
			return fmt.Errorf("publishing: payload with continuations must not carry images")
		}
		for i, c := range p.Continuations {
			if strings.TrimSpace(c) == "" {
				return fmt.Errorf("publishing: payload continuation %d must not be empty", i)
			}
		}
	}
	if p.IsEmpty() && (p.Link != "" || p.Kind != "") {
		return fmt.Errorf("publishing: payload has a link or kind but no text or images")
	}
	return nil
}

func (i ImageRef) validate() error {
	if strings.TrimSpace(i.Path) == "" {
		return fmt.Errorf("path must not be empty")
	}
	clean := path.Clean(strings.ReplaceAll(i.Path, `\`, "/"))
	if path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(i.Path, ":") {
		return fmt.Errorf("path %q must be relative to the media store", i.Path)
	}
	if !strings.HasPrefix(i.MIME, "image/") {
		return fmt.Errorf("mime %q must be an image/* type", i.MIME)
	}
	return nil
}

// ValidatePayload decodes and validates a raw outbox payload column value.
func ValidatePayload(raw string) error {
	p, err := ParseMessagePayload(raw)
	if err != nil {
		return err
	}
	return p.Validate()
}
