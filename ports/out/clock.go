package out

import "time"

// Clock abstracts wall-clock time so domain and application logic stay
// testable.
type Clock interface {
	Now() time.Time
}
