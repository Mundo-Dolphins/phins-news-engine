package espnscoreboard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/matchstate"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/netguard"
)

const defaultHost = "https://site.api.espn.com/apis/site/v2/sports/football/nfl/scoreboard"

// DefaultUserAgent is sent when the client is built with an empty user agent.
const DefaultUserAgent = netguard.DefaultUserAgent

const (
	// maxResponseBytes caps the scoreboard document. It is larger than
	// netguard.JSONBodyLimit because a full league week of events with
	// competitor detail can exceed 1 MiB.
	maxResponseBytes int64 = 4 << 20
	defaultTimeout         = 10 * time.Second
)

// Client polls ESPN's public NFL scoreboard endpoint for the configured
// team's currently live game. It implements out.MatchStateProvider.
type Client struct {
	Host             string // exported so tests can point it at an httptest.Server
	TeamAbbreviation string
	UserAgent        string
	httpClient       *http.Client
}

// NewClient builds a Client pointed at ESPN's real public scoreboard
// endpoint for the given team abbreviation. An empty userAgent means
// DefaultUserAgent and a non-positive timeout means 10s. Redirects follow
// the netguard policy and link-local destinations are refused.
func NewClient(teamAbbreviation, userAgent string, timeout time.Duration) *Client {
	if userAgent == "" {
		userAgent = DefaultUserAgent
	}
	return &Client{
		Host:             defaultHost,
		TeamAbbreviation: teamAbbreviation,
		UserAgent:        userAgent,
		httpClient:       netguard.NewClient(timeout, defaultTimeout, netguard.AllowPrivate),
	}
}

// Current implements out.MatchStateProvider.
func (c *Client) Current(ctx context.Context, now time.Time) (matchstate.State, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Host, nil)
	if err != nil {
		return matchstate.State{}, false, fmt.Errorf("espn scoreboard: build request: %w", netguard.RedactError(err))
	}
	ua := c.UserAgent
	if ua == "" {
		ua = DefaultUserAgent
	}
	req.Header.Set("User-Agent", ua)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return matchstate.State{}, false, fmt.Errorf("espn scoreboard: request: %w", netguard.RedactError(err))
	}
	defer resp.Body.Close()

	body, err := netguard.ReadBody(resp.Body, maxResponseBytes)
	if errors.Is(err, netguard.ErrBodyTooLarge) {
		return matchstate.State{}, false, fmt.Errorf("espn scoreboard: %w", err)
	}
	if err != nil {
		return matchstate.State{}, false, fmt.Errorf("espn scoreboard: read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return matchstate.State{}, false, fmt.Errorf("espn scoreboard: unexpected status %d", resp.StatusCode)
	}

	var parsed scoreboardResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return matchstate.State{}, false, fmt.Errorf("espn scoreboard: decode response: %w", err)
	}

	event, ok := findTeamEvent(parsed, c.TeamAbbreviation)
	if !ok {
		return matchstate.State{}, false, nil
	}
	state, ok := mapEvent(event, now)
	if !ok {
		return matchstate.State{}, false, nil
	}
	return state, true, nil
}

var _ interface {
	Current(ctx context.Context, now time.Time) (matchstate.State, bool, error)
} = (*Client)(nil)
