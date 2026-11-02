package bluesky

import "time"

// ClientTimeoutForTest exposes the HTTP client timeout.
func ClientTimeoutForTest(c *Client) time.Duration { return c.httpClient.Timeout }

// CountGraphemesForTest exposes the grapheme counter.
func CountGraphemesForTest(s string) int { return countGraphemes(s) }
