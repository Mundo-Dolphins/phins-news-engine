// app/eventrulecycle/known_test.go
package eventrulecycle_test

import (
	"sort"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/app/eventrulecycle"
)

func TestKnownTypeNames(t *testing.T) {
	got := eventrulecycle.KnownTypeNames()
	if !sort.StringsAreSorted(got) {
		t.Errorf("not sorted: %v", got)
	}
	seen := map[string]bool{}
	for _, n := range got {
		if seen[n] {
			t.Errorf("duplicate %q", n)
		}
		seen[n] = true
	}
	for _, want := range []string{"game", "injury", "injured-reserve", "generic", "quote"} {
		if !seen[want] {
			t.Errorf("missing %q", want)
		}
	}
}
