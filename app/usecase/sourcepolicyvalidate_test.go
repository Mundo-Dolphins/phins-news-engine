package usecase_test

import (
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/app/usecase"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/sourcepolicyfile"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/sourcesfile"
)

func TestValidateSourcePolicies_OK(t *testing.T) {
	sources := &sourcesfile.Sources{
		Twitter: sourcesfile.TwitterConfig{Accounts: []sourcesfile.TwitterAccountConfig{
			{Handle: "SprIsotopes", Enabled: true, IntervalMinutes: 1, MaxItemsPerCycle: 1, MaxAgeHours: 1},
		}},
	}
	policies := &sourcepolicyfile.File{
		Version: 1, Mode: "shadow",
		Policies: []sourcepolicyfile.PolicyEntry{
			{SourceID: mustID(t, source.TypeTwitter, "SprIsotopes"), Authority: "official_team"},
		},
	}
	result := usecase.ValidateSourcePolicies(sources, policies)
	if len(result.Issues) != 0 {
		t.Errorf("Issues = %v, want none", result.Issues)
	}
}

func TestValidateSourcePolicies_OrphanedPolicy(t *testing.T) {
	sources := &sourcesfile.Sources{} // no sources configured at all
	policies := &sourcepolicyfile.File{
		Version: 1, Mode: "shadow",
		Policies: []sourcepolicyfile.PolicyEntry{
			{SourceID: mustID(t, source.TypeTwitter, "SprIsotopes"), Authority: "official_team"},
		},
	}
	result := usecase.ValidateSourcePolicies(sources, policies)
	if len(result.Issues) != 1 {
		t.Fatalf("Issues = %v, want exactly one orphaned-policy issue", result.Issues)
	}
}

func TestValidateSourcePolicies_TwitterPolicyDoesNotCoverInstagramOrBluesky(t *testing.T) {
	// The exact production shape: both configured, same bare handle,
	// only a Twitter policy exists. It must NOT be treated as covering
	// Instagram or Bluesky too.
	sources := &sourcesfile.Sources{
		Twitter: sourcesfile.TwitterConfig{Accounts: []sourcesfile.TwitterAccountConfig{
			{Handle: "SprIsotopes", Enabled: false},
		}},
		Instagram: sourcesfile.InstagramConfig{Accounts: []sourcesfile.InstagramAccountConfig{
			{Handle: "sprisotopes", Enabled: false},
		}},
		Bluesky: sourcesfile.BlueskyConfig{Accounts: []sourcesfile.BlueskyAccountConfig{
			{Handle: "isotopes.bsky.social", Enabled: false},
		}},
	}
	policies := &sourcepolicyfile.File{
		Version: 1, Mode: "shadow",
		Policies: []sourcepolicyfile.PolicyEntry{
			{SourceID: mustID(t, source.TypeTwitter, "SprIsotopes"), Authority: "official_team"},
		},
	}
	result := usecase.ValidateSourcePolicies(sources, policies)
	if len(result.Issues) != 0 {
		t.Errorf("Issues = %v, want none — the Twitter policy legitimately matches only the Twitter source", result.Issues)
	}
}
