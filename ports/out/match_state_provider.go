package out

import (
	"context"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/matchstate"
)

// MatchStateProvider reports the currently live game for whatever team
// the implementation is configured for, if any. Implementations must
// fail closed: any doubt about identity or phase returns ok=false, never
// a guessed State. now is passed through for implementations that need
// it for logging/caching; it is not used to compute the state itself
// (the live provider's own clock — ESPN's — is authoritative for phase).
type MatchStateProvider interface {
	Current(ctx context.Context, now time.Time) (state matchstate.State, ok bool, err error)
}
