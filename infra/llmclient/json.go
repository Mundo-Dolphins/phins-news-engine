// infra/llmclient/json.go
package llmclient

import (
	"context"
	"encoding/json"
	"errors"
)

// CompleteJSON sends system+user and returns the model's reply with any
// code fence removed, after checking it is valid JSON. An unusable reply is
// retried once (see runStructured). The caller owns interpreting the JSON.
func (c *Client) CompleteJSON(ctx context.Context, system, user string) (string, error) {
	return runStructured(ctx, c, system, user, func(content string) (string, error) {
		s := stripCodeFence(content)
		if !json.Valid([]byte(s)) {
			return "", errors.New("response is not valid JSON")
		}
		return s, nil
	})
}

// StatusCode returns the HTTP status code carried by an error returned from
// the client, when the failure was a non-2xx provider response.
func StatusCode(err error) (int, bool) {
	var statusErr *httpStatusError
	if errors.As(err, &statusErr) {
		return statusErr.StatusCode, true
	}
	return 0, false
}
