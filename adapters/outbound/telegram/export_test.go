package telegram

import "time"

// ClientTimeoutForTest exposes the publisher's HTTP client timeout.
func ClientTimeoutForTest(p *Publisher) time.Duration { return p.client.Timeout }
