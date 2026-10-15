// infra/sourcepolicyfile/resolver_test.go
package sourcepolicyfile_test

import (
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/sourcepolicy"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/sourcepolicyfile"
)

func mustID(t *testing.T, platform source.Type, rawIdentifier string) source.ID {
	t.Helper()
	id, err := source.NewID(platform, rawIdentifier)
	if err != nil {
		t.Fatalf("source.NewID(%q, %q) = %v, want no error", platform, rawIdentifier, err)
	}
	return id
}

func TestResolver_ResolvesConfiguredSource(t *testing.T) {
	sourceID := mustID(t, source.TypeRSS, "sprisotopes")
	f := &sourcepolicyfile.File{
		Version: 1, Mode: "enforce",
		Policies: []sourcepolicyfile.PolicyEntry{
			{
				SourceID: sourceID, Authority: "official_team",
				RetainNearDuplicates: true, RepresentativePreference: "high",
				AllowOfficialConfirmation: true, AllowSubstantialUpdate: true,
				MinimumUpdateNovelty: 0.65, EventCooldown: "20m",
			},
		},
	}
	r := sourcepolicyfile.NewResolver(f)
	p := r.Resolve(sourceID.String())
	if p.Authority != sourcepolicy.AuthorityOfficialTeam {
		t.Errorf("Authority = %q, want %q", p.Authority, sourcepolicy.AuthorityOfficialTeam)
	}
	if !p.RetainNearDuplicates || !p.AllowOfficialConfirmation || !p.AllowSubstantialUpdate {
		t.Errorf("unexpected resolved policy: %+v", p)
	}
	if p.RepresentativePreference != sourcepolicy.PreferenceHigh {
		t.Errorf("RepresentativePreference = %q, want %q", p.RepresentativePreference, sourcepolicy.PreferenceHigh)
	}
	if p.EventCooldown != 20*time.Minute {
		t.Errorf("EventCooldown = %v, want 20m", p.EventCooldown)
	}
	if p.Version != "1" {
		t.Errorf("Version = %q, want %q", p.Version, "1")
	}
}

func TestResolver_UnconfiguredSource_ReturnsDefault(t *testing.T) {
	f := &sourcepolicyfile.File{Version: 1, Mode: "enforce"}
	r := sourcepolicyfile.NewResolver(f)
	p := r.Resolve("some-random-blog")
	want := sourcepolicy.DefaultPolicy("some-random-blog")
	if p != want {
		t.Errorf("Resolve() = %+v, want %+v", p, want)
	}
}

func TestResolver_Mode(t *testing.T) {
	f := &sourcepolicyfile.File{Version: 1, Mode: "shadow"}
	r := sourcepolicyfile.NewResolver(f)
	if r.Mode() != sourcepolicy.ModeShadow {
		t.Errorf("Mode() = %q, want %q", r.Mode(), sourcepolicy.ModeShadow)
	}
}

func TestResolver_NilFile_IsFullyInert(t *testing.T) {
	r := sourcepolicyfile.NewResolver(nil)
	if r.Mode() != sourcepolicy.ModeDisabled {
		t.Errorf("Mode() = %q, want %q", r.Mode(), sourcepolicy.ModeDisabled)
	}
	if p := r.Resolve("anything"); p != sourcepolicy.DefaultPolicy("anything") {
		t.Errorf("Resolve() = %+v, want default", p)
	}
}

func TestResolver_TwitterAndInstagramNeverShareAPolicy(t *testing.T) {
	twitterID := mustID(t, source.TypeTwitter, "SprIsotopes")
	f := &sourcepolicyfile.File{
		Version: 1,
		Mode:    "enforce",
		Policies: []sourcepolicyfile.PolicyEntry{
			{SourceID: twitterID, Authority: "official_team", RepresentativePreference: "high"},
		},
	}
	r := sourcepolicyfile.NewResolver(f)

	got := r.Resolve(twitterID.String())
	if got.IsUnconfigured() {
		t.Fatalf("Resolve(%q) = unconfigured, want the configured twitter policy", twitterID)
	}

	instagramID := mustID(t, source.TypeInstagram, "sprisotopes") // same bare handle, different platform
	gotIG := r.Resolve(instagramID.String())
	if !gotIG.IsUnconfigured() {
		t.Fatalf("Resolve(%q) = %+v, want the default (unconfigured) policy — it must NOT inherit Twitter's policy", instagramID, gotIG)
	}

	blueskyID := mustID(t, source.TypeBluesky, "isotopes.bsky.social")
	gotBsky := r.Resolve(blueskyID.String())
	if !gotBsky.IsUnconfigured() {
		t.Fatalf("Resolve(%q) = %+v, want the default (unconfigured) policy", blueskyID, gotBsky)
	}
}

var _ sourcepolicy.Resolver = (*sourcepolicyfile.Resolver)(nil)
