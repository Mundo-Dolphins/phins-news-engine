package dictionary_test

import (
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/dictionary"
)

func TestBumpPatchVersion_IncrementsPatch(t *testing.T) {
	got, err := dictionary.BumpPatchVersion("1.0.0")
	if err != nil {
		t.Fatalf("BumpPatchVersion() error = %v", err)
	}
	if got != "1.0.1" {
		t.Errorf("BumpPatchVersion(%q) = %q, want %q", "1.0.0", got, "1.0.1")
	}
}

func TestBumpPatchVersion_HandlesMultiDigitPatch(t *testing.T) {
	got, err := dictionary.BumpPatchVersion("2.3.9")
	if err != nil {
		t.Fatalf("BumpPatchVersion() error = %v", err)
	}
	if got != "2.3.10" {
		t.Errorf("BumpPatchVersion(%q) = %q, want %q", "2.3.9", got, "2.3.10")
	}
}

func TestBumpPatchVersion_RejectsNonSemver(t *testing.T) {
	tests := []string{"", "1.0", "1.0.0.0", "v1.0.0", "1.0.x"}
	for _, v := range tests {
		t.Run(v, func(t *testing.T) {
			if _, err := dictionary.BumpPatchVersion(v); err == nil {
				t.Errorf("BumpPatchVersion(%q) error = nil, want an error for non-semver input", v)
			}
		})
	}
}
