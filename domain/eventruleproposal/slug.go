// domain/eventruleproposal/slug.go
package eventruleproposal

import (
	"strings"
	"unicode"
)

// Slug normalizes an event type name to its canonical form: lowercase, every
// run of characters that are not a Unicode letter or digit collapsed to a
// single "-", with leading and trailing "-" trimmed. It returns "" when name
// has no letter or digit.
func Slug(name string) string {
	var b strings.Builder
	pendingDash := false
	for _, r := range strings.ToLower(name) {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			pendingDash = b.Len() > 0
			continue
		}
		if pendingDash {
			b.WriteByte('-')
			pendingDash = false
		}
		b.WriteRune(r)
	}
	return b.String()
}
