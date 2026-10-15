package entitydiscovery_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/entitydiscovery"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/weblookup"
)

type byNameVerifier struct {
	results map[string]entitydiscovery.VerifyResult
	calls   []entitydiscovery.VerifyCandidate
}

func (v *byNameVerifier) Verify(_ context.Context, c entitydiscovery.VerifyCandidate) entitydiscovery.VerifyResult {
	v.calls = append(v.calls, c)
	return v.results[c.Name]
}

func newReverifyFixture() (*fakeProposalRepo, *byNameVerifier) {
	repo := newFakeProposalRepo()
	for _, n := range []string{"Springfield Mackerels", "Patrick Ostrander", "Springfield"} {
		repo.upserted = append(repo.upserted, entityproposal.Proposal{
			ID: entityproposal.Normalize(n), CanonicalName: n, NormalizedName: entityproposal.Normalize(n),
			EntityType: entityproposal.EntityTypePlayer, Kind: entityproposal.KindNewEntity,
			Status: entityproposal.StatusPending, Confidence: 0.6,
			Sources: []entityproposal.Source{{ArticleID: "a1", ArticleTitle: n}},
		})
	}
	// nil metadata on purpose.
	web := []entityproposal.WebSource{{Domain: "x.com", URL: "https://x.com/1"}}
	return repo, &byNameVerifier{results: map[string]entitydiscovery.VerifyResult{
		"Springfield Mackerels": {Disposition: weblookup.DispositionReject, Resolved: true, Reason: "MLB", WebSources: web, Metadata: map[string]string{"league": "MLB"}},
		"Patrick Ostrander":     {Disposition: weblookup.DispositionKeep, Resolved: true, Confidence: 0.95, WebSources: web, Metadata: map[string]string{"league": "NFL"}},
		"Springfield":           {Disposition: weblookup.DispositionUncertain},
	}}
}

func TestReverifyPending_CountsAndIdempotence(t *testing.T) {
	repo, v := newReverifyFixture()
	res, err := entitydiscovery.ReverifyPending(context.Background(), repo, v, emptyCatalog(), entitydiscovery.ReverifyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Processed != 3 || res.Kept != 1 || res.AutoRejected != 1 || res.Uncertain != 1 || res.Skipped != 0 {
		t.Fatalf("result = %+v", res)
	}
	if len(v.calls) != 3 || len(v.calls[0].Sources) != 1 {
		t.Fatalf("calls = %+v, want 3 with stored sources", v.calls)
	}
	for _, p := range repo.upserted {
		if p.Metadata[entitydiscovery.MetadataVerifyAttemptedAt] == "" {
			t.Errorf("%s has no attempt marker", p.CanonicalName)
		}
	}

	res, err = entitydiscovery.ReverifyPending(context.Background(), repo, v, emptyCatalog(), entitydiscovery.ReverifyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Processed != 0 || res.Skipped != 1+1 || len(v.calls) != 3 {
		t.Fatalf("second run = %+v calls=%d, want nothing re-verified", res, len(v.calls))
	}
	res, err = entitydiscovery.ReverifyPending(context.Background(), repo, v, emptyCatalog(), entitydiscovery.ReverifyOptions{RetryAttempted: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Processed != 1 || res.Uncertain != 1 || len(v.calls) != 4 {
		t.Fatalf("retry run = %+v calls=%d, want Springfield retried", res, len(v.calls))
	}
}

func TestReverifyPending_LimitWalksBacklogWithoutStarvation(t *testing.T) {
	repo := newFakeProposalRepo()
	for _, n := range []string{"A One", "B Two", "C Three", "D Four", "E Five"} {
		repo.upserted = append(repo.upserted, entityproposal.Proposal{
			ID: entityproposal.Normalize(n), CanonicalName: n, NormalizedName: entityproposal.Normalize(n),
			EntityType: entityproposal.EntityTypePlayer, Kind: entityproposal.KindNewEntity,
			Status: entityproposal.StatusPending,
		})
	}
	v := &byNameVerifier{results: map[string]entitydiscovery.VerifyResult{}} // all uncertain
	seen := map[string]bool{}
	for i, want := range []int{2, 2, 1, 0} {
		before := len(v.calls)
		res, err := entitydiscovery.ReverifyPending(context.Background(), repo, v, emptyCatalog(), entitydiscovery.ReverifyOptions{Limit: 2})
		if err != nil {
			t.Fatal(err)
		}
		if res.Processed != want {
			t.Fatalf("run %d processed = %d, want %d", i+1, res.Processed, want)
		}
		for _, c := range v.calls[before:] {
			if seen[c.Name] {
				t.Fatalf("run %d re-verified %s", i+1, c.Name)
			}
			seen[c.Name] = true
		}
	}
	if len(seen) != 5 {
		t.Fatalf("distinct processed = %d, want 5", len(seen))
	}
}

func TestReverifyPending_ConflictCountedAndMarked(t *testing.T) {
	repo, v := newReverifyFixture()
	cr := &conflictOnceRepo{fakeProposalRepo: repo}
	res, err := entitydiscovery.ReverifyPending(context.Background(), cr, v, emptyCatalog(), entitydiscovery.ReverifyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Conflicts != 1 || res.Uncertain != 1 || res.Processed != 3 {
		t.Fatalf("res = %+v", res)
	}
	for _, p := range repo.upserted {
		if p.Metadata[entitydiscovery.MetadataVerifyAttemptedAt] == "" {
			t.Errorf("%s has no marker", p.CanonicalName)
		}
	}
}

// conflictOnceRepo returns ErrConflict for the first non-marker verification
// update (one that changes status or web sources).
type conflictOnceRepo struct {
	*fakeProposalRepo
	done bool
}

func (c *conflictOnceRepo) UpdateVerification(ctx context.Context, p entityproposal.Proposal) error {
	if !c.done && len(p.WebSources) > 0 {
		c.done = true
		return entityproposal.ErrConflict
	}
	return c.fakeProposalRepo.UpdateVerification(ctx, p)
}

func TestReverifyPending_DryRunWritesNothing(t *testing.T) {
	repo, v := newReverifyFixture()
	res, err := entitydiscovery.ReverifyPending(context.Background(), repo, v, emptyCatalog(), entitydiscovery.ReverifyOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Kept != 1 || res.AutoRejected != 1 || res.Uncertain != 1 {
		t.Fatalf("dry-run counts = %+v", res)
	}
	for _, p := range repo.upserted {
		if p.Metadata[entitydiscovery.MetadataVerifyAttemptedAt] != "" {
			t.Fatalf("dry run wrote marker on %s", p.CanonicalName)
		}
	}
	if repo.updates != 0 {
		t.Fatalf("updates = %d, want 0", repo.updates)
	}
	for _, p := range repo.upserted {
		if p.Status != entityproposal.StatusPending {
			t.Fatalf("%s status = %s, want pending", p.CanonicalName, p.Status)
		}
	}
}

func TestReverifyPending_TypeFilterAndKinds(t *testing.T) {
	repo, v := newReverifyFixture()
	repo.upserted[0].EntityType = entityproposal.EntityTypeTeam
	repo.upserted[1].Kind = entityproposal.KindNewAlias
	res, err := entitydiscovery.ReverifyPending(context.Background(), repo, v, emptyCatalog(), entitydiscovery.ReverifyOptions{EntityType: "player"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Processed != 1 || v.calls[0].Name != "Springfield" {
		t.Fatalf("res = %+v calls = %+v, want only Springfield", res, v.calls)
	}
}

// Bookkeeping keys never reach the verifier prompt (finding 8).
func TestReverifyPending_CandidateMetadataOmitsBookkeeping(t *testing.T) {
	repo, v := newReverifyFixture()
	for i := range repo.upserted {
		repo.upserted[i].Metadata = map[string]string{
			entitydiscovery.MetadataVerifyAttemptedAt: "2026-10-01T00:00:00Z", "reason": "earlier", "position": "QB",
		}
	}
	if _, err := entitydiscovery.ReverifyPending(context.Background(), repo, v, emptyCatalog(), entitydiscovery.ReverifyOptions{RetryAttempted: true}); err != nil {
		t.Fatal(err)
	}
	if len(v.calls) != 3 {
		t.Fatalf("calls = %d, want 3", len(v.calls))
	}
	for _, c := range v.calls {
		if _, ok := c.Metadata[entitydiscovery.MetadataVerifyAttemptedAt]; ok {
			t.Fatalf("%s: marker sent to verifier: %v", c.Name, c.Metadata)
		}
		if _, ok := c.Metadata["reason"]; ok || c.Metadata["position"] != "QB" {
			t.Fatalf("%s: metadata = %v, want reason stripped and position kept", c.Name, c.Metadata)
		}
	}
	if repo.upserted[0].Metadata["position"] != "QB" {
		t.Fatal("stored metadata mutated")
	}
}

// --retry-attempted with a --limit walks the backlog oldest-attempt first
// instead of re-verifying the same first N every run (finding 7).
func TestReverifyPending_RetryAttemptedWalksOldestFirst(t *testing.T) {
	repo := newFakeProposalRepo()
	for i, n := range []string{"A One", "B Two", "C Three", "D Four", "E Five"} {
		md := map[string]string{entitydiscovery.MetadataVerifyAttemptedAt: time.Date(2026, 9, 1+i, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)}
		if n == "E Five" {
			md = nil // never attempted: goes first
		}
		repo.upserted = append(repo.upserted, entityproposal.Proposal{
			ID: entityproposal.Normalize(n), CanonicalName: n, NormalizedName: entityproposal.Normalize(n),
			EntityType: entityproposal.EntityTypePlayer, Kind: entityproposal.KindNewEntity,
			Status: entityproposal.StatusPending, Metadata: md,
		})
	}
	v := &byNameVerifier{results: map[string]entitydiscovery.VerifyResult{}} // all uncertain
	var order []string
	for run := 0; run < 3; run++ {
		before := len(v.calls)
		_, err := entitydiscovery.ReverifyPending(context.Background(), repo, v, emptyCatalog(), entitydiscovery.ReverifyOptions{
			Limit: 2, RetryAttempted: true, Now: time.Date(2026, 10, 1, 0, 0, run, 0, time.UTC),
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range v.calls[before:] {
			order = append(order, c.Name)
		}
	}
	// Run 3: D is the oldest remaining; A and E tie on run 1's marker
	// and keep list order.
	want := []string{"E Five", "A One", "B Two", "C Three", "D Four", "A One"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
}

// I1: Wikidata-verified pending rows are skipped (never sent to the
// fallback) by ReverifyPending.
func TestReverifyPending_SkipsWikidataVerifiedRows(t *testing.T) {
	repo := newFakeProposalRepo()
	repo.upserted = append(repo.upserted, entityproposal.Proposal{
		ID: "patrick ostrander", CanonicalName: "Patrick Ostrander", NormalizedName: "patrick ostrander",
		EntityType: entityproposal.EntityTypePlayer, Kind: entityproposal.KindNewEntity,
		Status:       entityproposal.StatusPending,
		Verification: entityproposal.Verification{Provider: "wikidata", ProviderID: "Q42", Version: "wikidata-v1"},
	})
	v := &byNameVerifier{results: map[string]entitydiscovery.VerifyResult{
		"Patrick Ostrander": {Disposition: weblookup.DispositionReject, Resolved: true, Provenance: entitydiscovery.Provenance{Provider: "llm_web"}},
	}}
	res, err := entitydiscovery.ReverifyPending(context.Background(), repo, v, emptyCatalog(), entitydiscovery.ReverifyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Skipped != 1 || res.Processed != 0 || len(v.calls) != 0 {
		t.Fatalf("result = %+v calls = %d, want 1 skipped and no fallback calls", res, len(v.calls))
	}
	if repo.upserted[0].Status != entityproposal.StatusPending {
		t.Fatalf("status = %s", repo.upserted[0].Status)
	}
}

// I1: --retry-attempted does not send Wikidata-verified rows to the
// fallback either (they carry no attempt marker to retry).
func TestReverifyPending_RetryAttemptedStillSkipsWikidataRows(t *testing.T) {
	repo := newFakeProposalRepo()
	repo.upserted = append(repo.upserted, entityproposal.Proposal{
		ID: "patrick ostrander", CanonicalName: "Patrick Ostrander", NormalizedName: "patrick ostrander",
		EntityType: entityproposal.EntityTypePlayer, Kind: entityproposal.KindNewEntity,
		Status:       entityproposal.StatusPending,
		Verification: entityproposal.Verification{Provider: "wikidata", ProviderID: "Q42", Version: "wikidata-v1"},
	})
	v := &byNameVerifier{results: map[string]entitydiscovery.VerifyResult{}}
	res, err := entitydiscovery.ReverifyPending(context.Background(), repo, v, emptyCatalog(), entitydiscovery.ReverifyOptions{RetryAttempted: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Skipped != 1 || len(v.calls) != 0 {
		t.Fatalf("result = %+v calls = %d", res, len(v.calls))
	}
}

func wikidataRow(name, version string, md map[string]string) entityproposal.Proposal {
	return entityproposal.Proposal{
		ID: entityproposal.Normalize(name), CanonicalName: name, NormalizedName: entityproposal.Normalize(name),
		EntityType: entityproposal.EntityTypePlayer, Kind: entityproposal.KindNewEntity,
		Status: entityproposal.StatusPending, Confidence: 0.6, Metadata: md,
		Verification: entityproposal.Verification{Provider: "wikidata", ProviderID: "Q1", Version: version},
		Sources:      []entityproposal.Source{{ArticleID: "a1", ArticleTitle: name}},
	}
}

func TestReverifyPending_StaleWikidataVersionReverifiedOnce(t *testing.T) {
	repo := newFakeProposalRepo()
	// Stale row carries a pre-bump marker with no attempt version (as #316 wrote it).
	repo.upserted = append(repo.upserted,
		wikidataRow("Patrick Ostrander", "wikidata-v0", map[string]string{entitydiscovery.MetadataVerifyAttemptedAt: "2026-01-01T00:00:00Z"}),
		wikidataRow("Tobin Kestrell", entitydiscovery.WikidataVerificationVersion, nil))
	v := &byNameVerifier{results: map[string]entitydiscovery.VerifyResult{
		"Patrick Ostrander": {
			Disposition: weblookup.DispositionKeep, Resolved: true, Confidence: 0.95,
			Metadata:   map[string]string{"league": "NFL"},
			Provenance: entitydiscovery.Provenance{Provider: "wikidata", ProviderID: "Q1", Version: entitydiscovery.WikidataVerificationVersion},
		},
	}}
	res, err := entitydiscovery.ReverifyPending(context.Background(), repo, v, emptyCatalog(), entitydiscovery.ReverifyOptions{Wikidata: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Processed != 1 || res.Skipped != 1 || len(v.calls) != 1 || v.calls[0].Name != "Patrick Ostrander" {
		t.Fatalf("res=%+v calls=%+v", res, v.calls)
	}
	got, _ := repo.Get(context.Background(), "patrick ostrander")
	if got.Verification.Version != entitydiscovery.WikidataVerificationVersion ||
		got.Metadata[entitydiscovery.MetadataVerifyAttemptedVersion] != entitydiscovery.WikidataVerificationVersion {
		t.Fatalf("row = %+v", got)
	}
	// Idempotent: a second run touches nothing.
	res, err = entitydiscovery.ReverifyPending(context.Background(), repo, v, emptyCatalog(), entitydiscovery.ReverifyOptions{Wikidata: true})
	if err != nil || res.Processed != 0 || res.Skipped != 2 || len(v.calls) != 1 {
		t.Fatalf("second run res=%+v err=%v calls=%d", res, err, len(v.calls))
	}
}

func TestReverifyPending_UncertainAttemptUnderOlderVersionIsRetried(t *testing.T) {
	repo := newFakeProposalRepo()
	repo.upserted = append(repo.upserted, entityproposal.Proposal{
		ID: "springfield", CanonicalName: "Springfield", NormalizedName: "springfield",
		EntityType: entityproposal.EntityTypeTeam, Kind: entityproposal.KindNewEntity, Status: entityproposal.StatusPending,
		Metadata: map[string]string{
			entitydiscovery.MetadataVerifyAttemptedAt:      "2026-01-01T00:00:00Z",
			entitydiscovery.MetadataVerifyAttemptedVersion: "wikidata-v0",
		},
	})
	v := &byNameVerifier{results: map[string]entitydiscovery.VerifyResult{"Springfield": {Disposition: weblookup.DispositionUncertain}}}
	res, err := entitydiscovery.ReverifyPending(context.Background(), repo, v, emptyCatalog(), entitydiscovery.ReverifyOptions{})
	if err != nil || res.Processed != 1 || res.Uncertain != 1 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	res, _ = entitydiscovery.ReverifyPending(context.Background(), repo, v, emptyCatalog(), entitydiscovery.ReverifyOptions{})
	if res.Processed != 0 || res.Skipped != 1 {
		t.Fatalf("second run = %+v", res)
	}
}

func legacyMarkedRow(name string) entityproposal.Proposal {
	return entityproposal.Proposal{
		ID: entityproposal.Normalize(name), CanonicalName: name, NormalizedName: entityproposal.Normalize(name),
		EntityType: entityproposal.EntityTypeTeam, Kind: entityproposal.KindNewEntity, Status: entityproposal.StatusPending,
		Metadata: map[string]string{entitydiscovery.MetadataVerifyAttemptedAt: "2026-01-01T00:00:00Z"}, // no version: pre-#317 marker
	}
}

func TestReverifyPending_LegacyMarkerReattemptedWhenWikidataStageOn(t *testing.T) {
	repo := newFakeProposalRepo()
	repo.upserted = append(repo.upserted, legacyMarkedRow("Springfield"))
	v := &byNameVerifier{results: map[string]entitydiscovery.VerifyResult{"Springfield": {Disposition: weblookup.DispositionUncertain}}}
	// Stage off: legacy marker still honored (previous behaviour).
	res, _ := entitydiscovery.ReverifyPending(context.Background(), repo, v, emptyCatalog(), entitydiscovery.ReverifyOptions{})
	if res.Processed != 0 || res.Skipped != 1 {
		t.Fatalf("stage off: %+v", res)
	}
	// Stage on: the backlog attempted before Wikidata existed is re-attempted once.
	opts := entitydiscovery.ReverifyOptions{Wikidata: true}
	res, _ = entitydiscovery.ReverifyPending(context.Background(), repo, v, emptyCatalog(), opts)
	if res.Processed != 1 || res.Uncertain != 1 {
		t.Fatalf("stage on first: %+v", res)
	}
	res, _ = entitydiscovery.ReverifyPending(context.Background(), repo, v, emptyCatalog(), opts)
	if res.Processed != 0 || res.Skipped != 1 {
		t.Fatalf("stage on second: %+v", res)
	}
}

func TestReverifyPending_ProviderErrorLeavesRowRetryable(t *testing.T) {
	repo := newFakeProposalRepo()
	repo.upserted = append(repo.upserted, legacyMarkedRow("Springfield"))
	delete(repo.upserted[0].Metadata, entitydiscovery.MetadataVerifyAttemptedAt)
	v := &byNameVerifier{results: map[string]entitydiscovery.VerifyResult{
		"Springfield": {Disposition: weblookup.DispositionUncertain, ProviderError: true},
	}}
	opts := entitydiscovery.ReverifyOptions{Wikidata: true}
	res, err := entitydiscovery.ReverifyPending(context.Background(), repo, v, emptyCatalog(), opts)
	if err != nil || res.Processed != 1 || res.ProviderErrors != 1 || res.Uncertain != 0 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if _, ok := repo.upserted[0].Metadata[entitydiscovery.MetadataVerifyAttemptedAt]; ok {
		t.Fatalf("provider error must not stamp the row: %+v", repo.upserted[0].Metadata)
	}
	res, _ = entitydiscovery.ReverifyPending(context.Background(), repo, v, emptyCatalog(), opts)
	if res.Processed != 1 || res.ProviderErrors != 1 {
		t.Fatalf("row not retried: %+v", res)
	}
}

func TestReverifyPending_VersionStampedOnlyWhenWikidataStageRan(t *testing.T) {
	repo := newFakeProposalRepo()
	repo.upserted = append(repo.upserted, legacyMarkedRow("Springfield"))
	delete(repo.upserted[0].Metadata, entitydiscovery.MetadataVerifyAttemptedAt)
	v := &byNameVerifier{results: map[string]entitydiscovery.VerifyResult{"Springfield": {Disposition: weblookup.DispositionUncertain}}}
	if _, err := entitydiscovery.ReverifyPending(context.Background(), repo, v, emptyCatalog(), entitydiscovery.ReverifyOptions{}); err != nil {
		t.Fatal(err)
	}
	md := repo.upserted[0].Metadata
	if md[entitydiscovery.MetadataVerifyAttemptedAt] == "" || md[entitydiscovery.MetadataVerifyAttemptedVersion] != "" {
		t.Fatalf("stage off must stamp attempt without version: %+v", md)
	}
	res, _ := entitydiscovery.ReverifyPending(context.Background(), repo, v, emptyCatalog(), entitydiscovery.ReverifyOptions{Wikidata: true})
	if res.Processed != 1 {
		t.Fatalf("a later Wikidata run must still attempt the row: %+v", res)
	}
}

type fakeTextLoader struct {
	texts []entitydiscovery.ArticleText
	err   error
	asked [][]string
}

func (f *fakeTextLoader) Texts(_ context.Context, ids []string) ([]entitydiscovery.ArticleText, error) {
	f.asked = append(f.asked, ids)
	return f.texts, f.err
}

func textRow(name string) entityproposal.Proposal {
	return entityproposal.Proposal{
		ID: entityproposal.Normalize(name), CanonicalName: name, NormalizedName: entityproposal.Normalize(name),
		EntityType: entityproposal.EntityTypePlayer, Kind: entityproposal.KindNewEntity,
		Status: entityproposal.StatusPending, Confidence: 0.6, Metadata: map[string]string{},
		Sources: []entityproposal.Source{{ArticleID: "a1", ArticleTitle: "T"}, {ArticleID: "a2", ArticleTitle: "T2"}},
	}
}

func TestReverifyPending_FillsCandidateTextsFromTheLoader(t *testing.T) {
	repo := newFakeProposalRepo()
	repo.upserted = append(repo.upserted, textRow("Burns"))
	v := &byNameVerifier{results: map[string]entitydiscovery.VerifyResult{"Burns": {Disposition: weblookup.DispositionUncertain}}}
	loader := &fakeTextLoader{texts: []entitydiscovery.ArticleText{{ID: "a1", Title: "T", Summary: "S"}}}

	_, err := entitydiscovery.ReverifyPending(context.Background(), repo, v, emptyCatalog(), entitydiscovery.ReverifyOptions{Texts: loader})
	if err != nil || len(v.calls) != 1 {
		t.Fatalf("err=%v calls=%d", err, len(v.calls))
	}
	if got := v.calls[0].Texts; len(got) != 1 || got[0].Summary != "S" {
		t.Fatalf("candidate Texts = %+v, want the loader's article", got)
	}
	if len(loader.asked) != 1 || len(loader.asked[0]) != 2 || loader.asked[0][0] != "a1" || loader.asked[0][1] != "a2" {
		t.Fatalf("loader asked for %v, want both source article ids", loader.asked)
	}
}

func TestReverifyPending_LoaderErrorDoesNotFailTheRun(t *testing.T) {
	repo := newFakeProposalRepo()
	repo.upserted = append(repo.upserted, textRow("Burns"))
	v := &byNameVerifier{results: map[string]entitydiscovery.VerifyResult{"Burns": {Disposition: weblookup.DispositionUncertain}}}
	loader := &fakeTextLoader{err: errors.New("db down")}

	res, err := entitydiscovery.ReverifyPending(context.Background(), repo, v, emptyCatalog(), entitydiscovery.ReverifyOptions{Texts: loader})
	if err != nil || res.Processed != 1 || len(v.calls) != 1 || len(v.calls[0].Texts) != 0 {
		t.Fatalf("res=%+v err=%v calls=%+v; want the candidate verified with no texts", res, err, v.calls)
	}
}

func TestReverifyPending_UncertainWithSuggestionsStampsMarkerAndSuggestions(t *testing.T) {
	sugg := map[string]string{"suggested_entity": "Dorian Burns", "suggested_team": "WAV", "suggested_basis": "unique_surname"}
	build := func() (*fakeProposalRepo, *byNameVerifier) {
		repo := newFakeProposalRepo()
		repo.upserted = append(repo.upserted, textRow("Burns"))
		return repo, &byNameVerifier{results: map[string]entitydiscovery.VerifyResult{
			"Burns": {Disposition: weblookup.DispositionUncertain, Suggestions: sugg},
		}}
	}

	repo, v := build()
	if _, err := entitydiscovery.ReverifyPending(context.Background(), repo, v, emptyCatalog(), entitydiscovery.ReverifyOptions{}); err != nil {
		t.Fatal(err)
	}
	md := repo.upserted[0].Metadata
	if md[entitydiscovery.MetadataVerifyAttemptedAt] == "" || md["suggested_entity"] != "Dorian Burns" || md["suggested_team"] != "WAV" {
		t.Fatalf("metadata = %v, want the attempt marker and the suggestions", md)
	}

	repo, v = build()
	if _, err := entitydiscovery.ReverifyPending(context.Background(), repo, v, emptyCatalog(), entitydiscovery.ReverifyOptions{DryRun: true}); err != nil {
		t.Fatal(err)
	}
	if md := repo.upserted[0].Metadata; md["suggested_entity"] != "" || md[entitydiscovery.MetadataVerifyAttemptedAt] != "" {
		t.Fatalf("dry run wrote metadata: %v", md)
	}
}
