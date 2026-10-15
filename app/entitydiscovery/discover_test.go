// app/entitydiscovery/discover_test.go
package entitydiscovery_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/entitydiscovery"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/knowledge"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/weblookup"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/entityproposalport"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

type fakeArticleLister struct {
	articles []article.Article
	// isProcessed, when set, makes ListUnprocessedByEntityDiscovery skip
	// articles it reports as processed before applying limit, like the
	// real repository does in SQL.
	isProcessed func(id string) bool
}

func (f fakeArticleLister) ListUnprocessedByEntityDiscovery(_ context.Context, _ time.Time, _ string, limit int) ([]article.Article, error) {
	var out []article.Article
	for _, a := range f.articles {
		if f.isProcessed != nil && f.isProcessed(a.ID) {
			continue
		}
		if len(out) == limit {
			break
		}
		out = append(out, a)
	}
	return out, nil
}

func (f fakeArticleLister) ListSince(_ context.Context, _ time.Time, limit int) ([]article.Article, error) {
	if limit < len(f.articles) {
		return f.articles[:limit], nil
	}
	return f.articles, nil
}

type fakeLLM struct {
	responses [][]appout.EntityDiscoveryEntity
	errs      []error
	calls     int
}

func (f *fakeLLM) Discover(_ context.Context, _ []appout.EntityDiscoveryArticle) (appout.EntityDiscoveryResult, error) {
	i := f.calls
	f.calls++
	if i < len(f.errs) && f.errs[i] != nil {
		return appout.EntityDiscoveryResult{}, f.errs[i]
	}
	if i < len(f.responses) {
		return appout.EntityDiscoveryResult{Entities: f.responses[i]}, nil
	}
	return appout.EntityDiscoveryResult{}, nil
}

func (f *fakeLLM) VerifyCandidate(_ context.Context, _ appout.VerifyCandidateRequest) (weblookup.AnalysisAction, error) {
	return weblookup.AnalysisAction{Action: weblookup.ActionFinal}, nil
}

type fakeProposalRepo struct {
	upserted  []entityproposal.Proposal
	processed map[string]bool
	nextID    int
	// updateErr, when set, is returned by UpdateVerification.
	updateErr error
	updates   int
}

func newFakeProposalRepo() *fakeProposalRepo {
	return &fakeProposalRepo{processed: map[string]bool{}}
}

func (f *fakeProposalRepo) Upsert(_ context.Context, p entityproposal.Proposal) (string, error) {
	f.nextID++
	p.ID = p.NormalizedName
	for i, e := range f.upserted {
		if e.ID != p.ID {
			continue
		}
		if e.Status == entityproposal.StatusRejected && (e.DecisionReason == entityproposal.ReasonAutoRejectedNonNFL || e.DecisionReason == entityproposal.ReasonDuplicateOfExisting) {
			return e.ID, nil
		}
		if e.Status == entityproposal.StatusPending {
			// Minimal stand-in for the real merge: union aliases, keep
			// existing metadata keys, keep the higher confidence.
			seen := map[string]bool{}
			var aliases []string
			for _, a := range append(append([]string{}, e.Aliases...), p.Aliases...) {
				if !seen[a] {
					seen[a] = true
					aliases = append(aliases, a)
				}
			}
			p.Aliases = aliases
			// Like the real merge, verification state is not touched by Upsert.
			p.Verification = e.Verification
			p.WebSources = e.WebSources
			md := map[string]string{}
			for k, v := range p.Metadata {
				md[k] = v
			}
			for k, v := range e.Metadata {
				md[k] = v
			}
			p.Metadata = md
			if e.Confidence > p.Confidence {
				p.Confidence = e.Confidence
			}
			f.upserted[i] = p
			return p.ID, nil
		}
	}
	f.upserted = append(f.upserted, p)
	return p.ID, nil
}

func (f *fakeProposalRepo) Get(_ context.Context, id string) (entityproposal.Proposal, error) {
	for _, p := range f.upserted {
		if p.ID == id {
			return p, nil
		}
	}
	return entityproposal.Proposal{}, errors.New("not found")
}

func (f *fakeProposalRepo) FindByNormalizedName(context.Context, string, entityproposal.EntityType) (entityproposal.Proposal, bool, error) {
	return entityproposal.Proposal{}, false, nil
}

func (f *fakeProposalRepo) FindByAlias(context.Context, string, entityproposal.EntityType) (entityproposal.Proposal, bool, error) {
	return entityproposal.Proposal{}, false, nil
}

func (f *fakeProposalRepo) List(_ context.Context, _ string, status string) ([]entityproposal.Proposal, error) {
	if status == "" {
		return f.upserted, nil
	}
	var out []entityproposal.Proposal
	for _, p := range f.upserted {
		if string(p.Status) == status {
			out = append(out, p)
		}
	}
	return out, nil
}

func (f *fakeProposalRepo) UpdateStatus(context.Context, string, entityproposal.Status, time.Time) error {
	return nil
}

func (f *fakeProposalRepo) FilterUnprocessed(_ context.Context, _ string, ids []string) ([]string, error) {
	var out []string
	for _, id := range ids {
		if !f.processed[id] {
			out = append(out, id)
		}
	}
	return out, nil
}

func (f *fakeProposalRepo) MarkArticlesProcessed(_ context.Context, _ string, ids []string, _ time.Time) error {
	for _, id := range ids {
		f.processed[id] = true
	}
	return nil
}

var _ entityproposalport.EntityProposalRepository = (*fakeProposalRepo)(nil)

func emptyCatalog() entitydiscovery.CatalogIndex {
	return entitydiscovery.BuildCatalogIndex(nil)
}

func TestDiscover_UnknownPlayerProducesNewEntityProposal(t *testing.T) {
	articles := fakeArticleLister{articles: []article.Article{
		{ID: "a1", Title: "Isotopes sign former Cypress CB Jai'Rene' Mossbank", Summary: "Springfield has signed cornerback Jai'Rene' Mossbank, who previously played at TCU and Cypress."},
	}}
	llm := &fakeLLM{responses: [][]appout.EntityDiscoveryEntity{{
		{Name: "Jai'Rene' Mossbank", Type: "player", Aliases: []string{"Mossbank"}, Metadata: map[string]string{"position": "CB"}, Confidence: 0.9},
	}}}
	repo := newFakeProposalRepo()

	result, err := entitydiscovery.Discover(context.Background(), articles, llm, repo, emptyCatalog(),
		entitydiscovery.DiscoverOptions{BatchSize: 20, Limit: 20, ExtractorVersion: "v1"}, time.Now().UTC(), nil)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if result.ProposalsUpserted != 1 {
		t.Fatalf("ProposalsUpserted = %d, want 1", result.ProposalsUpserted)
	}
	if len(repo.upserted) != 1 || repo.upserted[0].Kind != entityproposal.KindNewEntity {
		t.Fatalf("upserted = %+v, want one KindNewEntity proposal", repo.upserted)
	}
}

func TestDiscover_KnownPlayerProducesNoProposal(t *testing.T) {
	articles := fakeArticleLister{articles: []article.Article{
		{ID: "a1", Title: "Pierce Quenby starts at tackle", Summary: "Pierce Quenby had a strong showing."},
	}}
	llm := &fakeLLM{responses: [][]appout.EntityDiscoveryEntity{{
		{Name: "Pierce Quenby", Type: "player", Confidence: 0.95},
	}}}
	repo := newFakeProposalRepo()
	catalog := entitydiscovery.BuildCatalogIndex([]eventsignals.EntityRule{
		{ID: "pierce-quenby", CanonicalName: "Pierce Quenby", Kind: eventsignals.EntityKindPlayer, Terms: []string{"Pierce Quenby"}},
	})

	result, err := entitydiscovery.Discover(context.Background(), articles, llm, repo, catalog,
		entitydiscovery.DiscoverOptions{BatchSize: 20, Limit: 20, ExtractorVersion: "v1"}, time.Now().UTC(), nil)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if result.ProposalsUpserted != 0 || len(repo.upserted) != 0 {
		t.Fatalf("upserted = %+v, want no proposal for a known entity", repo.upserted)
	}
}

func TestDiscover_CoachHartleyProducesAliasProposalAgainstCatalogEntity(t *testing.T) {
	articles := fakeArticleLister{articles: []article.Article{
		{ID: "a1", Title: "Coach Harrowby speaks to media", Summary: "Coach Harrowby discussed the defense."},
	}}
	llm := &fakeLLM{responses: [][]appout.EntityDiscoveryEntity{{
		{Name: "Coach Harrowby", Type: "coach", Confidence: 0.7},
	}}}
	repo := newFakeProposalRepo()
	catalog := entitydiscovery.BuildCatalogIndex([]eventsignals.EntityRule{
		{ID: "jeff-harrowby", CanonicalName: "Jeff Harrowby", Kind: eventsignals.EntityKindCoach, Terms: []string{"Jeff Harrowby"}},
	})

	result, err := entitydiscovery.Discover(context.Background(), articles, llm, repo, catalog,
		entitydiscovery.DiscoverOptions{BatchSize: 20, Limit: 20, ExtractorVersion: "v1"}, time.Now().UTC(), nil)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if result.ProposalsUpserted != 1 || repo.upserted[0].Kind != entityproposal.KindNewAlias || repo.upserted[0].TargetEntityID != "jeff-harrowby" {
		t.Fatalf("upserted = %+v, want one KindNewAlias proposal targeting jeff-harrowby", repo.upserted)
	}
}

func TestDiscover_RepeatedMentionsAggregateIntoOneProposal(t *testing.T) {
	articles := fakeArticleLister{articles: []article.Article{
		{ID: "a1", Title: "Isotopes sign Jai'Rene' Mossbank", Summary: "Springfield signed Jai'Rene' Mossbank."},
		{ID: "a2", Title: "Mossbank practices with the team", Summary: "Jai'Rene' Mossbank took part in practice."},
	}}
	llm := &fakeLLM{responses: [][]appout.EntityDiscoveryEntity{
		{{Name: "Jai'Rene' Mossbank", Type: "player", Confidence: 0.8}},
		{{Name: "Jai'Rene' Mossbank", Type: "player", Confidence: 0.9}},
	}}
	repo := newFakeProposalRepo()

	result, err := entitydiscovery.Discover(context.Background(), articles, llm, repo, emptyCatalog(),
		entitydiscovery.DiscoverOptions{BatchSize: 1, Limit: 20, ExtractorVersion: "v1"}, time.Now().UTC(), nil)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if result.ProposalsUpserted != 1 {
		t.Fatalf("ProposalsUpserted = %d, want 1 (both mentions aggregated)", result.ProposalsUpserted)
	}
	if repo.upserted[0].Occurrences != 2 || len(repo.upserted[0].Sources) != 2 {
		t.Fatalf("upserted[0] = %+v, want Occurrences=2 and 2 sources", repo.upserted[0])
	}
}

func TestDiscover_TypographicVariantsCollapseIntoOneProposal(t *testing.T) {
	articles := fakeArticleLister{articles: []article.Article{
		{ID: "a1", Title: "Isotopes sign Jai'Rene' Mossbank", Summary: "Springfield signed Jai'Rene' Mossbank."},
		{ID: "a2", Title: "Report: JaiRene Mossbank joins practice squad", Summary: "JaiRene Mossbank is now on the practice squad."},
	}}
	llm := &fakeLLM{responses: [][]appout.EntityDiscoveryEntity{
		{{Name: "Jai'Rene' Mossbank", Type: "player", Confidence: 0.8}},
		{{Name: "JaiRene Mossbank", Type: "player", Confidence: 0.85}},
	}}
	repo := newFakeProposalRepo()

	result, err := entitydiscovery.Discover(context.Background(), articles, llm, repo, emptyCatalog(),
		entitydiscovery.DiscoverOptions{BatchSize: 1, Limit: 20, ExtractorVersion: "v1"}, time.Now().UTC(), nil)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if result.ProposalsUpserted != 1 {
		t.Fatalf("ProposalsUpserted = %d, want 1 (typographic variants share a normalized key)", result.ProposalsUpserted)
	}
}

func TestDiscover_FailedBatchIsSkippedButRunContinues(t *testing.T) {
	articles := fakeArticleLister{articles: []article.Article{
		{ID: "a1", Title: "Isotopes sign Jai'Rene' Mossbank", Summary: "Springfield signed Jai'Rene' Mossbank."},
		{ID: "a2", Title: "Isotopes sign Idris Westbridge", Summary: "Springfield signed Idris Westbridge."},
	}}
	llm := &fakeLLM{
		errs:      []error{errors.New("connection refused"), nil},
		responses: [][]appout.EntityDiscoveryEntity{nil, {{Name: "Idris Westbridge", Type: "player", Confidence: 0.8}}},
	}
	repo := newFakeProposalRepo()

	result, err := entitydiscovery.Discover(context.Background(), articles, llm, repo, emptyCatalog(),
		entitydiscovery.DiscoverOptions{BatchSize: 1, Limit: 20, ExtractorVersion: "v1"}, time.Now().UTC(), nil)
	if err != nil {
		t.Fatalf("Discover() error = %v, want the run to succeed overall despite one failed batch", err)
	}
	if result.BatchesFailed != 1 || result.BatchesTotal != 2 {
		t.Fatalf("BatchesFailed/BatchesTotal = %d/%d, want 1/2", result.BatchesFailed, result.BatchesTotal)
	}
	if result.ProposalsUpserted != 1 {
		t.Fatalf("ProposalsUpserted = %d, want 1 (the surviving batch's entity)", result.ProposalsUpserted)
	}
	if repo.processed["a1"] {
		t.Fatal(`processed["a1"] = true, want false — a failed batch's articles must remain unprocessed for retry`)
	}
	if !repo.processed["a2"] {
		t.Fatal(`processed["a2"] = false, want true — a succeeded batch's articles must be marked processed`)
	}
}

func TestDiscover_EntityWithoutTextualEvidenceIsDiscarded(t *testing.T) {
	articles := fakeArticleLister{articles: []article.Article{
		{ID: "a1", Title: "Isotopes beat the Sharks", Summary: "Springfield won the game on Sunday."},
	}}
	llm := &fakeLLM{responses: [][]appout.EntityDiscoveryEntity{{
		{Name: "Ezo Quarrington", Type: "player", Confidence: 0.99}, // not mentioned in the supplied text at all
	}}}
	repo := newFakeProposalRepo()

	result, err := entitydiscovery.Discover(context.Background(), articles, llm, repo, emptyCatalog(),
		entitydiscovery.DiscoverOptions{BatchSize: 20, Limit: 20, ExtractorVersion: "v1"}, time.Now().UTC(), nil)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if result.ProposalsUpserted != 0 {
		t.Fatalf("ProposalsUpserted = %d, want 0 — the entity has no evidence in the batch's own text", result.ProposalsUpserted)
	}
}

func TestDiscover_AmbiguousSurnameAcrossTwoNewCandidatesFlagsBothAsPossibleDuplicate(t *testing.T) {
	articles := fakeArticleLister{articles: []article.Article{
		{ID: "a1", Title: "Isotopes sign John Smith", Summary: "Springfield signed John Smith to the practice squad."},
		{ID: "a2", Title: "Isotopes sign Robert Smith", Summary: "Springfield also signed Robert Smith on a tryout."},
	}}
	llm := &fakeLLM{responses: [][]appout.EntityDiscoveryEntity{{
		{Name: "John Smith", Type: "player", Confidence: 0.7},
		{Name: "Robert Smith", Type: "player", Confidence: 0.7},
	}}}
	repo := newFakeProposalRepo()

	result, err := entitydiscovery.Discover(context.Background(), articles, llm, repo, emptyCatalog(),
		entitydiscovery.DiscoverOptions{BatchSize: 20, Limit: 20, ExtractorVersion: "v1"}, time.Now().UTC(), nil)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if result.ProposalsUpserted != 2 {
		t.Fatalf("ProposalsUpserted = %d, want 2 (both kept as separate proposals)", result.ProposalsUpserted)
	}
	for _, p := range repo.upserted {
		if p.Kind != entityproposal.KindPossibleDuplicate {
			t.Errorf("proposal %q Kind = %q, want possible_duplicate", p.CanonicalName, p.Kind)
		}
	}
}

type fakeVerifier struct {
	result   entitydiscovery.VerifyResult
	requests []entitydiscovery.VerifyCandidate
}

func (f *fakeVerifier) Verify(_ context.Context, req entitydiscovery.VerifyCandidate) entitydiscovery.VerifyResult {
	f.requests = append(f.requests, req)
	return f.result
}

func TestDiscover_VerifierEnrichesUncertainCandidate(t *testing.T) {
	articles := fakeArticleLister{articles: []article.Article{
		{ID: "a1", Title: "Isotopes sign CB Jai'Rene' Mossbank", Summary: "Springfield has signed cornerback Jai'Rene' Mossbank."},
	}}
	llm := &fakeLLM{responses: [][]appout.EntityDiscoveryEntity{{
		{Name: "Jai'Rene' Mossbank", Type: "player", Confidence: 0.5}, // low confidence -> should trigger verification
	}}}
	repo := newFakeProposalRepo()
	verifier := &fakeVerifier{result: entitydiscovery.VerifyResult{
		Disposition: weblookup.DispositionKeep, Confidence: 0.97, Resolved: true,
		WebSources: []entityproposal.WebSource{{Domain: "isotopes.example.com", URL: "https://isotopes.example.com/news/1", Title: "Isotopes Add CB"}},
	}}

	_, err := entitydiscovery.Discover(context.Background(), articles, llm, repo, emptyCatalog(),
		entitydiscovery.DiscoverOptions{BatchSize: 20, Limit: 20, ExtractorVersion: "v1", VerifyMinConfidence: 0.85}, time.Now().UTC(), verifier)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(verifier.requests) != 1 {
		t.Fatalf("verifier invocations = %d, want 1", len(verifier.requests))
	}
	if len(repo.upserted) != 1 {
		t.Fatalf("upserted proposals = %d, want 1", len(repo.upserted))
	}
	p := repo.upserted[0]
	if p.Confidence != 0.97 {
		t.Fatalf("Confidence = %v, want 0.97 (from the verifier)", p.Confidence)
	}
	if len(p.WebSources) != 1 || p.WebSources[0].Domain != "isotopes.example.com" {
		t.Fatalf("WebSources = %+v, want the verifier's web source attached", p.WebSources)
	}
}

func TestDiscover_HighConfidenceNewEntityStillVerified(t *testing.T) {
	articles := fakeArticleLister{articles: []article.Article{
		{ID: "a1", Title: "Isotopes name Jeff Harrowby defensive coordinator", Summary: "Springfield has promoted Jeff Harrowby to defensive coordinator."},
	}}
	llm := &fakeLLM{responses: [][]appout.EntityDiscoveryEntity{{
		{Name: "Jeff Harrowby", Type: "coach", Confidence: 0.95},
	}}}
	repo := newFakeProposalRepo()
	verifier := &fakeVerifier{result: entitydiscovery.VerifyResult{Resolved: true}}

	_, err := entitydiscovery.Discover(context.Background(), articles, llm, repo, emptyCatalog(),
		entitydiscovery.DiscoverOptions{BatchSize: 20, Limit: 20, ExtractorVersion: "v1", VerifyMinConfidence: 0.85}, time.Now().UTC(), verifier)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	// New policy: a new_entity is verified regardless of confidence.
	if len(verifier.requests) != 1 {
		t.Fatalf("verifier invocations = %d, want 1 (new_entity always verifies)", len(verifier.requests))
	}
}

func TestDiscover_NilVerifierBehavesLikeFastMode(t *testing.T) {
	articles := fakeArticleLister{articles: []article.Article{
		{ID: "a1", Title: "Isotopes sign CB Jai'Rene' Mossbank", Summary: "Springfield has signed cornerback Jai'Rene' Mossbank."},
	}}
	llm := &fakeLLM{responses: [][]appout.EntityDiscoveryEntity{{
		{Name: "Jai'Rene' Mossbank", Type: "player", Confidence: 0.5},
	}}}
	repo := newFakeProposalRepo()

	result, err := entitydiscovery.Discover(context.Background(), articles, llm, repo, emptyCatalog(),
		entitydiscovery.DiscoverOptions{BatchSize: 20, Limit: 20, ExtractorVersion: "v1", VerifyMinConfidence: 0.85}, time.Now().UTC(), nil)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if result.ProposalsUpserted != 1 {
		t.Fatalf("ProposalsUpserted = %d, want 1 (nil verifier must not block proposal creation)", result.ProposalsUpserted)
	}
}

// UpdateVerification mirrors the real repository's guards: only a
// still-pending row is written, and a pending identity collision is
// ErrConflict.
func (f *fakeProposalRepo) UpdateVerification(_ context.Context, p entityproposal.Proposal) error {
	if f.updateErr != nil {
		return f.updateErr
	}
	for i := range f.upserted {
		if f.upserted[i].ID == p.ID {
			if f.upserted[i].Status != entityproposal.StatusPending {
				return entityproposal.ErrConflict
			}
			if p.Status == entityproposal.StatusPending {
				for j, o := range f.upserted {
					if j != i && o.Status == entityproposal.StatusPending && o.NormalizedName == p.NormalizedName && o.EntityType == p.EntityType {
						return entityproposal.ErrConflict
					}
				}
			}
			f.upserted[i] = p
			f.updates++
			return nil
		}
	}
	return entityproposal.ErrNotFound
}

func discoverOne(t *testing.T, name, typ, title string, repo entityproposalport.EntityProposalRepository, v entitydiscovery.Verifier, cat entitydiscovery.CatalogIndex) entitydiscovery.DiscoverResult {
	t.Helper()
	articles := fakeArticleLister{articles: []article.Article{{ID: "a1", Title: title, Summary: title}}}
	llm := &fakeLLM{responses: [][]appout.EntityDiscoveryEntity{{{Name: name, Type: typ, Confidence: 0.9}}}}
	res, err := entitydiscovery.Discover(context.Background(), articles, llm, repo, cat,
		entitydiscovery.DiscoverOptions{BatchSize: 20, Limit: 20, ExtractorVersion: "v1", VerifyMinConfidence: 0.85}, time.Now().UTC(), v)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	return res
}

func TestDiscover_RejectPersistsAutoRejectedAndLeavesNothingPending(t *testing.T) {
	repo := newFakeProposalRepo()
	v := &fakeVerifier{result: entitydiscovery.VerifyResult{
		Disposition: weblookup.DispositionReject, Resolved: true, Reason: "MLB team",
		Metadata:   map[string]string{"sport": "baseball", "league": "MLB"},
		WebSources: []entityproposal.WebSource{{Domain: "baseball.example.com", URL: "https://baseball.example.com/mackerels"}},
	}}
	res := discoverOne(t, "Springfield Mackerels", "team", "Springfield Mackerels win again", repo, v, emptyCatalog())
	if res.AutoRejected != 1 {
		t.Fatalf("AutoRejected = %d, want 1", res.AutoRejected)
	}
	all, _ := repo.List(context.Background(), "", "")
	if len(all) != 1 || all[0].Status != entityproposal.StatusRejected || all[0].DecisionReason != entityproposal.ReasonAutoRejectedNonNFL {
		t.Fatalf("stored = %+v, want one rejected/auto_rejected_non_nfl", all)
	}
	if len(all[0].WebSources) != 1 || all[0].WebSources[0].Domain != "baseball.example.com" {
		t.Fatalf("WebSources = %+v, want evidence kept", all[0].WebSources)
	}
	pending, _ := repo.List(context.Background(), "", string(entityproposal.StatusPending))
	if len(pending) != 0 {
		t.Fatalf("pending = %+v, want none", pending)
	}
}

func TestDiscover_KeepPersistsMetadata(t *testing.T) {
	repo := newFakeProposalRepo()
	v := &fakeVerifier{result: entitydiscovery.VerifyResult{
		Disposition: weblookup.DispositionKeep, Resolved: true, Confidence: 0.96,
		Metadata: map[string]string{"sport": "football", "league": "NFL", "position": "QB", "team": "Capital City Capitals"},
	}}
	discoverOne(t, "Patrick Ostrander", "player", "Patrick Ostrander throws", repo, v, emptyCatalog())
	all, _ := repo.List(context.Background(), "", "")
	if len(all) != 1 || all[0].Status != entityproposal.StatusPending {
		t.Fatalf("stored = %+v, want one pending", all)
	}
	if m := all[0].Metadata; m["position"] != "QB" || m["team"] != "Capital City Capitals" || m["league"] != "NFL" {
		t.Fatalf("Metadata = %+v, want verified metadata", m)
	}
	if all[0].Confidence != 0.96 {
		t.Fatalf("Confidence = %v, want 0.96", all[0].Confidence)
	}
}

func TestDiscover_KeepPersistsCorrectedTypeAndName(t *testing.T) {
	repo := newFakeProposalRepo()
	v := &fakeVerifier{result: entitydiscovery.VerifyResult{
		Disposition: weblookup.DispositionKeep, Resolved: true, Confidence: 0.9,
		CanonicalName: "Jeff Harrowby", EntityType: entityproposal.EntityTypeCoach,
	}}
	discoverOne(t, "Jeff Harrowy", "player", "Jeff Harrowy leads defense", repo, v, emptyCatalog())
	all, _ := repo.List(context.Background(), "", "")
	if len(all) != 1 || all[0].EntityType != entityproposal.EntityTypeCoach || all[0].CanonicalName != "Jeff Harrowby" || all[0].NormalizedName != entityproposal.Normalize("Jeff Harrowby") {
		t.Fatalf("stored = %+v, want corrected coach/Jeff Harrowby", all)
	}
}

func TestDiscover_UncertainLeavesProposalPending(t *testing.T) {
	repo := newFakeProposalRepo()
	v := &fakeVerifier{result: entitydiscovery.VerifyResult{Disposition: weblookup.DispositionUncertain}}
	res := discoverOne(t, "Some Guy", "player", "Some Guy signs", repo, v, emptyCatalog())
	if res.Uncertain != 1 || res.AutoRejected != 0 {
		t.Fatalf("result = %+v, want Uncertain=1", res)
	}
	pending, _ := repo.List(context.Background(), "", string(entityproposal.StatusPending))
	if len(pending) != 1 || repo.updates != 0 {
		t.Fatalf("pending = %+v updates=%d, want one untouched pending", pending, repo.updates)
	}
}

func TestDiscover_ConflictFallsBackToPendingAndCountsUncertain(t *testing.T) {
	repo := newFakeProposalRepo()
	repo.updateErr = entityproposal.ErrConflict
	v := &fakeVerifier{result: entitydiscovery.VerifyResult{Disposition: weblookup.DispositionReject, Resolved: true}}
	res := discoverOne(t, "Springfield Mackerels", "team", "Springfield Mackerels win", repo, v, emptyCatalog())
	if res.Uncertain != 1 || res.AutoRejected != 0 {
		t.Fatalf("result = %+v, want Uncertain=1 AutoRejected=0", res)
	}
	pending, _ := repo.List(context.Background(), "", string(entityproposal.StatusPending))
	if len(pending) != 1 {
		t.Fatalf("pending = %+v, want the plain proposal left pending", pending)
	}
}

func preseed(repo *fakeProposalRepo, p entityproposal.Proposal) {
	p.ID = p.NormalizedName
	repo.upserted = append(repo.upserted, p)
}

func TestDiscover_VerificationPreservesEarlierRunState(t *testing.T) {
	for _, tc := range []struct {
		name string
		vr   entitydiscovery.VerifyResult
	}{
		{"keep", entitydiscovery.VerifyResult{Disposition: weblookup.DispositionKeep, Resolved: true, Confidence: 0.2}},
		{"reject", entitydiscovery.VerifyResult{Disposition: weblookup.DispositionReject, Resolved: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := newFakeProposalRepo()
			preseed(repo, entityproposal.Proposal{
				CanonicalName: "Some Guy", NormalizedName: entityproposal.Normalize("Some Guy"),
				EntityType: entityproposal.EntityTypePlayer, Status: entityproposal.StatusPending,
				Aliases: []string{"Old Alias"}, Metadata: map[string]string{"extra": "keep-me"}, Confidence: 0.95,
			})
			discoverOne(t, "Some Guy", "player", "Some Guy signs", repo, &fakeVerifier{result: tc.vr}, emptyCatalog())
			all, _ := repo.List(context.Background(), "", "")
			if len(all) != 1 {
				t.Fatalf("rows = %d, want 1", len(all))
			}
			got := all[0]
			hasAlias := false
			for _, a := range got.Aliases {
				hasAlias = hasAlias || a == "Old Alias"
			}
			if !hasAlias || got.Metadata["extra"] != "keep-me" || got.Confidence != 0.95 {
				t.Fatalf("row = %+v, want earlier alias, metadata and confidence preserved", got)
			}
		})
	}
}

func TestDiscover_AlreadyAutoRejectedIsNotReverifiedOrResurrected(t *testing.T) {
	repo := newFakeProposalRepo()
	preseed(repo, entityproposal.Proposal{
		CanonicalName: "Springfield Mackerels", NormalizedName: entityproposal.Normalize("Springfield Mackerels"),
		EntityType: entityproposal.EntityTypeTeam, Status: entityproposal.StatusRejected,
		DecisionReason: entityproposal.ReasonAutoRejectedNonNFL,
	})
	v := &fakeVerifier{result: entitydiscovery.VerifyResult{Disposition: weblookup.DispositionKeep, Resolved: true}}
	res := discoverOne(t, "Springfield Mackerels", "team", "Springfield Mackerels win", repo, v, emptyCatalog())
	if len(v.requests) != 0 || res.AutoRejected != 0 || res.Uncertain != 0 {
		t.Fatalf("requests=%d result=%+v, want no verification and no counts", len(v.requests), res)
	}
	all, _ := repo.List(context.Background(), "", "")
	if all[0].Status != entityproposal.StatusRejected {
		t.Fatalf("status = %s, want still rejected", all[0].Status)
	}
}

// A verification rename/retype onto an identity that is already pending
// must not leave a second pending row that conflicts forever.
func TestDiscover_RenameOntoExistingPendingRejectsDuplicate(t *testing.T) {
	for _, tc := range []struct {
		name, extracted, typ, title string
		existing                    entityproposal.Proposal
		vr                          entitydiscovery.VerifyResult
	}{
		{
			"surname", "Ostrander", "player", "Ostrander throws",
			entityproposal.Proposal{CanonicalName: "Patrick Ostrander", NormalizedName: "patrick ostrander", EntityType: entityproposal.EntityTypePlayer, Status: entityproposal.StatusPending},
			entitydiscovery.VerifyResult{
				Disposition: weblookup.DispositionKeep, Resolved: true, Confidence: 0.9, CanonicalName: "Patrick Ostrander",
				WebSources: []entityproposal.WebSource{{Domain: "league.example.com", URL: "https://league.example.com/m"}},
			},
		},
		{
			"retyped", "Fennow", "player", "Fennow calls the defense",
			entityproposal.Proposal{CanonicalName: "Brian Fennow", NormalizedName: "brian fennow", EntityType: entityproposal.EntityTypeCoach, Status: entityproposal.StatusPending},
			entitydiscovery.VerifyResult{Disposition: weblookup.DispositionKeep, Resolved: true, Confidence: 0.9, CanonicalName: "Brian Fennow", EntityType: entityproposal.EntityTypeCoach},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := newFakeProposalRepo()
			preseed(repo, tc.existing)
			res := discoverOne(t, tc.extracted, tc.typ, tc.title, repo, &fakeVerifier{result: tc.vr}, emptyCatalog())
			pending, _ := repo.List(context.Background(), "", string(entityproposal.StatusPending))
			if len(pending) != 1 || pending[0].ID != tc.existing.NormalizedName {
				t.Fatalf("pending = %+v, want only the existing row", pending)
			}
			dup, err := repo.Get(context.Background(), entityproposal.Normalize(tc.extracted))
			if err != nil {
				t.Fatal(err)
			}
			if dup.Status != entityproposal.StatusRejected || dup.DecisionReason != entityproposal.ReasonDuplicateOfExisting {
				t.Fatalf("duplicate row = %+v, want rejected/%s", dup, entityproposal.ReasonDuplicateOfExisting)
			}
			if dup.NormalizedName != entityproposal.Normalize(tc.extracted) {
				t.Fatalf("duplicate row identity = %q, want the pre-verification name kept", dup.NormalizedName)
			}
			if res.Duplicates != 1 || res.Uncertain != 0 {
				t.Fatalf("result = %+v, want Duplicates=1", res)
			}
		})
	}
}

// If the duplicate cannot be marked either, the row is left pending.
func TestDiscover_KeepConflictThatCannotBeMarkedStaysPending(t *testing.T) {
	repo := newFakeProposalRepo()
	repo.updateErr = entityproposal.ErrConflict
	v := &fakeVerifier{result: entitydiscovery.VerifyResult{Disposition: weblookup.DispositionKeep, Resolved: true, Confidence: 0.9, CanonicalName: "Patrick Ostrander"}}
	res := discoverOne(t, "Ostrander", "player", "Ostrander throws", repo, v, emptyCatalog())
	pending, _ := repo.List(context.Background(), "", string(entityproposal.StatusPending))
	if len(pending) != 1 || res.Uncertain != 1 || res.Duplicates != 0 {
		t.Fatalf("pending=%+v result=%+v, want left pending and counted uncertain", pending, res)
	}
}

// approveDuringVerifyRepo simulates a human approving the row while the
// verifier was running.
type approveDuringVerifyRepo struct {
	*fakeProposalRepo
}

func (r approveDuringVerifyRepo) UpdateVerification(ctx context.Context, p entityproposal.Proposal) error {
	for i := range r.upserted {
		if r.upserted[i].ID == p.ID {
			r.upserted[i].Status = entityproposal.StatusApproved
		}
	}
	return r.fakeProposalRepo.UpdateVerification(ctx, p)
}

func TestDiscover_HumanDecisionDuringVerificationIsKept(t *testing.T) {
	repo := newFakeProposalRepo()
	v := &fakeVerifier{result: entitydiscovery.VerifyResult{Disposition: weblookup.DispositionReject, Resolved: true}}
	discoverOne(t, "Some Guy", "player", "Some Guy signs", approveDuringVerifyRepo{repo}, v, emptyCatalog())
	got, _ := repo.Get(context.Background(), "some guy")
	if got.Status != entityproposal.StatusApproved || got.DecisionReason != "" {
		t.Fatalf("row = %+v, want the human approval untouched", got)
	}
}

type countingKnowledge struct{ searches, gets int }

func (k *countingKnowledge) Search(context.Context, string, int) ([]knowledge.Candidate, error) {
	k.searches++
	return nil, nil
}

func (k *countingKnowledge) Get(context.Context, []string) ([]knowledge.Entity, error) {
	k.gets++
	return nil, nil
}

type countingFallback struct {
	calls int
	res   entitydiscovery.VerifyResult
}

func (f *countingFallback) Verify(context.Context, entitydiscovery.VerifyCandidate) entitydiscovery.VerifyResult {
	f.calls++
	return f.res
}

func TestDiscover_ReportsCatalogHitsAndChainStats(t *testing.T) {
	articles := fakeArticleLister{articles: []article.Article{
		{ID: "a1", Title: "Ezo Quarrington and Some Guy practice", Summary: "Ezo Quarrington and Some Guy practiced."},
	}}
	llm := &fakeLLM{responses: [][]appout.EntityDiscoveryEntity{{
		{Name: "Ezo Quarrington", Type: "player", Confidence: 0.95},
		{Name: "Some Guy", Type: "player", Confidence: 0.9},
	}}}
	repo := newFakeProposalRepo()
	catalog := entitydiscovery.BuildCatalogIndex([]eventsignals.EntityRule{
		{ID: "ezo-quarrington", CanonicalName: "Ezo Quarrington", Kind: eventsignals.EntityKindPlayer, Terms: []string{"Ezo Quarrington"}},
	})
	fb := &countingFallback{res: entitydiscovery.VerifyResult{Disposition: weblookup.DispositionUncertain}}
	chain := &entitydiscovery.ChainVerifier{Fallback: fb, Catalog: catalog}

	res, err := entitydiscovery.Discover(context.Background(), articles, llm, repo, catalog,
		entitydiscovery.DiscoverOptions{BatchSize: 20, Limit: 20, ExtractorVersion: "v1", VerifyMinConfidence: 0.85}, time.Now().UTC(), chain)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	v := res.Verification
	if v.CatalogHits != 1 || v.CandidatesConsidered != 1 || v.Chain.Considered != 1 || v.Chain.FallbackCalls != 1 || v.LeftForReview != 1 {
		t.Fatalf("Verification = %+v, want CatalogHits=1 CandidatesConsidered=1 Considered=1 FallbackCalls=1 LeftForReview=1", v)
	}
}

func TestDiscover_ManualDecisionIsNeverReverified(t *testing.T) {
	repo := newFakeProposalRepo()
	preseed(repo, entityproposal.Proposal{
		CanonicalName: "Some Guy", NormalizedName: entityproposal.Normalize("Some Guy"),
		EntityType: entityproposal.EntityTypePlayer, Status: entityproposal.StatusRejected,
	})
	k := &countingKnowledge{}
	fb := &countingFallback{res: entitydiscovery.VerifyResult{Disposition: weblookup.DispositionKeep, Resolved: true}}
	chain := &entitydiscovery.ChainVerifier{Knowledge: k, Fallback: fb, Catalog: emptyCatalog()}

	discoverOne(t, "Some Guy", "player", "Some Guy signs", repo, chain, emptyCatalog())

	if k.searches != 0 || fb.calls != 0 {
		t.Fatalf("searches=%d fallback calls=%d, want 0/0 for a manually decided proposal", k.searches, fb.calls)
	}
	all, _ := repo.List(context.Background(), "", "")
	// The fake Upsert appends a pending row for a manually rejected name
	// (as the real repository does); only the seeded row is asserted.
	if len(all) == 0 || all[0].Status != entityproposal.StatusRejected || all[0].DecisionReason != "" {
		t.Fatalf("stored = %+v, want seeded row unchanged: rejected with empty DecisionReason", all)
	}
}

func TestDiscover_LocalCatalogHitNeverReachesVerifier(t *testing.T) {
	articles := fakeArticleLister{articles: []article.Article{
		{ID: "a1", Title: "Pierce Quenby starts at tackle", Summary: "Pierce Quenby had a strong showing."},
	}}
	llm := &fakeLLM{responses: [][]appout.EntityDiscoveryEntity{{
		{Name: "Pierce Quenby", Type: "player", Confidence: 0.95},
	}}}
	repo := newFakeProposalRepo()
	catalog := entitydiscovery.BuildCatalogIndex([]eventsignals.EntityRule{
		{ID: "pierce-quenby", CanonicalName: "Pierce Quenby", Kind: eventsignals.EntityKindPlayer, Terms: []string{"Pierce Quenby"}},
	})
	k := &countingKnowledge{}
	fb := &countingFallback{}
	chain := &entitydiscovery.ChainVerifier{Knowledge: k, Fallback: fb, Catalog: catalog}

	_, err := entitydiscovery.Discover(context.Background(), articles, llm, repo, catalog,
		entitydiscovery.DiscoverOptions{BatchSize: 20, Limit: 20, ExtractorVersion: "v1", VerifyMinConfidence: 0.85}, time.Now().UTC(), chain)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if k.searches != 0 || fb.calls != 0 || len(repo.upserted) != 0 {
		t.Fatalf("searches=%d fallback=%d upserted=%d, want all zero", k.searches, fb.calls, len(repo.upserted))
	}
}

// M1: a duplicate_of_existing row keeps the Wikidata audit trail.
func TestDiscover_DuplicateKeepsVerificationProvenance(t *testing.T) {
	repo := newFakeProposalRepo()
	preseed(repo, entityproposal.Proposal{CanonicalName: "Patrick Ostrander", NormalizedName: "patrick ostrander", EntityType: entityproposal.EntityTypePlayer, Status: entityproposal.StatusPending})
	v := &fakeVerifier{result: entitydiscovery.VerifyResult{
		Disposition: weblookup.DispositionKeep, Resolved: true, Confidence: 0.9, CanonicalName: "Patrick Ostrander",
		Provenance: entitydiscovery.Provenance{Provider: "wikidata", ProviderID: "Q1", Version: "wikidata-v1"},
	}}
	discoverOne(t, "Ostrander", "player", "Ostrander throws", repo, v, emptyCatalog())
	dup, err := repo.Get(context.Background(), "ostrander")
	if err != nil {
		t.Fatal(err)
	}
	if dup.DecisionReason != entityproposal.ReasonDuplicateOfExisting || dup.Verification.Provider != "wikidata" || dup.Verification.ProviderID != "Q1" {
		t.Fatalf("duplicate row = %+v, want duplicate_of_existing keeping wikidata provenance", dup)
	}
}

// I1: a Wikidata-kept pending row is never overridden by a later fallback
// (LLM/web) result in Discover.
func TestDiscover_WikidataKeptRowNotOverriddenByFallbackReject(t *testing.T) {
	repo := newFakeProposalRepo()
	wd := &fakeVerifier{result: entitydiscovery.VerifyResult{
		Disposition: weblookup.DispositionKeep, Resolved: true, Confidence: 0.95,
		Metadata:   map[string]string{"league": "NFL"},
		Provenance: entitydiscovery.Provenance{Provider: "wikidata", ProviderID: "Q42", Version: "wikidata-v1"},
	}}
	discoverOne(t, "Patrick Ostrander", "player", "Patrick Ostrander throws", repo, wd, emptyCatalog())
	before, _ := repo.Get(context.Background(), "patrick ostrander")
	if before.Verification.Provider != "wikidata" || before.Status != entityproposal.StatusPending {
		t.Fatalf("setup row = %+v", before)
	}

	repo.processed = map[string]bool{} // a later run re-extracts the same name
	fb := &fakeVerifier{result: entitydiscovery.VerifyResult{
		Disposition: weblookup.DispositionReject, Resolved: true, Reason: "not NFL",
		Metadata:   map[string]string{"league": "MLB"},
		WebSources: []entityproposal.WebSource{{Domain: "x.com", URL: "https://x.com/1"}},
		Provenance: entitydiscovery.Provenance{Provider: "llm_web", Version: "web-lookup-v1"},
	}}
	res := discoverOne(t, "Patrick Ostrander", "player", "Patrick Ostrander throws", repo, fb, emptyCatalog())
	after, _ := repo.Get(context.Background(), "patrick ostrander")
	if after.Status != entityproposal.StatusPending || after.Verification != before.Verification || after.Metadata["league"] != "NFL" || len(after.WebSources) != 0 {
		t.Fatalf("row after fallback reject = %+v, want unchanged Wikidata keep", after)
	}
	if res.AutoRejected != 0 || res.Uncertain != 1 {
		t.Fatalf("result = %+v, want fallback result ignored as uncertain", res)
	}
}

func TestDiscover_LimitCountsOnlyUnprocessedArticles(t *testing.T) {
	repo := newFakeProposalRepo()
	repo.processed["a1"] = true
	repo.processed["a2"] = true
	articles := fakeArticleLister{
		articles: []article.Article{
			{ID: "a1", Title: "t1", Summary: "s1"},
			{ID: "a2", Title: "t2", Summary: "s2"},
			{ID: "a3", Title: "t3", Summary: "s3"},
		},
		isProcessed: func(id string) bool { return repo.processed[id] },
	}

	result, err := entitydiscovery.Discover(context.Background(), articles, &fakeLLM{}, repo, emptyCatalog(),
		entitydiscovery.DiscoverOptions{BatchSize: 20, Limit: 1, ExtractorVersion: "v1"}, time.Now().UTC(), nil)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if result.ArticlesConsidered != 1 {
		t.Fatalf("ArticlesConsidered = %d, want 1 — already-processed articles must not consume the limit", result.ArticlesConsidered)
	}
	if !repo.processed["a3"] {
		t.Fatal(`processed["a3"] = false, want true`)
	}
}

func TestDiscover_SkipsNoiseAndKnownLeagueEntities(t *testing.T) {
	idx := leagueStage(t, leaguePlayers()).Index
	articles := fakeArticleLister{articles: []article.Article{
		{ID: "a1", Title: "Sharks win", Summary: "Falls and Jalen Falls led the Sharks; @bot spam; Topes fans; Brand New Rookie debuts"},
	}}
	llm := &fakeLLM{responses: [][]appout.EntityDiscoveryEntity{{
		{Name: "Jalen Falls", Type: "player", Confidence: 0.9},      // known league player (full name)
		{Name: "Falls", Type: "player", Confidence: 0.9},            // unique surname corroborated by "Sharks"
		{Name: "@bot", Type: "player", Confidence: 0.9},             // noise
		{Name: "Topes", Type: "team", Confidence: 0.9},              // NFL team alias
		{Name: "Brand New Rookie", Type: "player", Confidence: 0.9}, // genuinely unknown -> a proposal
	}}}
	repo := newFakeProposalRepo()

	result, err := entitydiscovery.Discover(context.Background(), articles, llm, repo, emptyCatalog(),
		entitydiscovery.DiscoverOptions{BatchSize: 20, Limit: 20, ExtractorVersion: "v1", League: idx}, time.Now().UTC(), nil)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if result.SkippedNoise != 1 || result.AlreadyKnownLeague != 3 || result.ProposalsUpserted != 1 {
		t.Fatalf("result = %+v; want 1 noise skipped, 3 known league entities skipped, 1 proposal", result)
	}
	if len(repo.upserted) != 1 || repo.upserted[0].CanonicalName != "Brand New Rookie" {
		t.Fatalf("upserted = %+v, want only the genuinely unknown player", repo.upserted)
	}
}

func TestDiscover_WithoutALeagueIndexBehavesAsBefore(t *testing.T) {
	articles := fakeArticleLister{articles: []article.Article{{ID: "a1", Title: "T", Summary: "Dorian Burns and @bot"}}}
	llm := &fakeLLM{responses: [][]appout.EntityDiscoveryEntity{{
		{Name: "Dorian Burns", Type: "player", Confidence: 0.9},
		{Name: "@bot", Type: "player", Confidence: 0.9},
	}}}
	repo := newFakeProposalRepo()
	result, err := entitydiscovery.Discover(context.Background(), articles, llm, repo, emptyCatalog(),
		entitydiscovery.DiscoverOptions{BatchSize: 20, Limit: 20, ExtractorVersion: "v1"}, time.Now().UTC(), nil)
	if err != nil || result.ProposalsUpserted != 2 || result.SkippedNoise != 0 || result.AlreadyKnownLeague != 0 {
		t.Fatalf("result = %+v, err = %v; want both mentions proposed when the league index is off", result, err)
	}
}
