// Package clock provides the real, system-wall-clock implementation of the
// application's out.Clock port.
package clock

import (
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// Real reports the actual system time.
type Real struct{}

// Now returns time.Now().
func (Real) Now() time.Time { return time.Now() }

var _ out.Clock = Real{}
