// app/leaguegazetteer/noise_test.go
package leaguegazetteer_test

import (
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/app/leaguegazetteer"
)

func TestIsNoise(t *testing.T) {
	for mention, want := range map[string]bool{
		"1examplefan": true, "555sportsfan": true, "33": true, "AJ": true, "Ca": true, "@AdamQuorvale": true,
		"Bigtime_XY": true, "https://x.com/a": true, "a/b": true, "aaaaab": true, "": true, "  ": true, "!!!": true,
		"Burns": false, "Dorian Burns": false, "Q.T. Brindlewood": false, "Ja'Ron Quillfeather": false, "Topes": false, "88ers": false,
	} {
		got, why := leaguegazetteer.IsNoise(mention, 3)
		if got != want {
			t.Errorf("IsNoise(%q) = %v (%s), want %v", mention, got, why, want)
		}
		if got && why == "" {
			t.Errorf("IsNoise(%q) gave no reason", mention)
		}
	}
	if noisy, _ := leaguegazetteer.IsNoise("Ab", 0); !noisy {
		t.Error("minLetters 0 must default to 3")
	}
}
