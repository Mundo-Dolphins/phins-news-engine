// app/editorialcycle/training_integration_test.go
//
// A single, larger end-to-end integration test proving Tasks 1-16 of the
// editorial-classifier-governance plan compose correctly against a
// realistically-sized synthetic corpus (~180 examples, not the
// few-example fixtures each task's own unit tests use). Kept as a
// sibling of training_test.go rather than appended to it: the fixture
// generator alone is substantial, and this plan's Global Constraints
// prefer smaller, focused files over one file accreting every kind of
// test.
package editorialcycle_test

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialtrain"
	"github.com/Mundo-Dolphins/phins-news-engine/app/modelpromotion"
	"github.com/Mundo-Dolphins/phins-news-engine/app/modeltraining"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/editorialveto"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/editorialmodel"
)

// vetoSeedPhrase is planted into a handful of synthetic "reject" examples
// and matched by the veto rule built in syntheticVetoConfig — this is
// what lets the test prove Report.VetoImpact actually reflects a
// deterministic rule firing against real text, not just a non-nil struct.
const vetoSeedPhrase = "free bets"

// syntheticFixtureSize is deliberately in the "larger, realistic" range
// the brief calls for (~150-200), not the handful of hand-written
// literals individual unit tests use elsewhere in this plan.
const syntheticFixtureSize = 180

// syntheticVetoConfig builds a minimal editorialveto.Config with one rule
// matching vetoSeedPhrase — a plain, unwrapped phrase (Evaluate
// word-boundary-wraps and case-folds it automatically; see
// domain/editorialveto's own doc comments).
func syntheticVetoConfig() *editorialveto.Config {
	return &editorialveto.Config{
		Version: "integration-test-veto-v1",
		Rules: []editorialveto.Rule{
			{
				Category:   editorialveto.CategoryGambling,
				ReasonCode: "gambling_free_bets",
				Patterns:   []string{vetoSeedPhrase},
			},
		},
	}
}

// specialCase describes one deliberately-constructed fixture example that
// exercises Task 2's leakage-grouping logic (exact-dup, near-dup, or
// shared ClusterID), layered on top of the otherwise-generic synthetic
// corpus. exactDupOf/nearDupOf (when >= 0) mean "reuse the same/similar
// text as the example already generated at this earlier index" — the
// generator below fills these in as it walks the corpus in order, so a
// pair's second member is always defined after its first.
type specialCase struct {
	label      string
	clusterID  string
	veto       bool // seed vetoSeedPhrase into this example's body
	exactDupOf int  // index into the generated slice, or -1
	nearDupOf  int  // index into the generated slice, or -1
}

// syntheticSpecialCases returns the fixed set of index -> specialCase
// overrides layered onto the generic fill: a handful of exact-duplicate
// pairs, near-duplicate pairs, and shared-ClusterID groups, plus several
// veto-phrase-seeded reject examples spread across the full index range
// so at least some land in every partition Split produces (train ~0-125,
// validate ~126-152, test ~153-179 at the 0.7/0.15/0.15 ratios this test
// uses over syntheticFixtureSize examples). One dup pair (122/128) and
// one near-dup pair (148/153) deliberately straddle a partition boundary,
// so Split's leakage-grouping relocation is actually exercised, not just
// grouping that would have landed together anyway.
func syntheticSpecialCases() map[int]specialCase {
	cases := map[int]specialCase{}

	// Veto-seeded reject examples, spread across train/validate/test.
	for _, i := range []int{5, 60, 100, 130, 140, 165, 175} {
		cases[i] = specialCase{label: "reject", veto: true, exactDupOf: -1, nearDupOf: -1}
	}

	// Exact-duplicate pairs (identical title+body across two ArticleIDs).
	for _, pair := range [][2]int{{12, 17}, {45, 49}, {122, 128}} {
		lbl := "publish"
		if pair == [2]int{45, 49} {
			lbl = "reject"
		}
		cases[pair[0]] = specialCase{label: lbl, exactDupOf: -1, nearDupOf: -1}
		cases[pair[1]] = specialCase{label: lbl, exactDupOf: pair[0], nearDupOf: -1}
	}

	// Near-duplicate pairs (similar but not identical text).
	for _, pair := range [][2]int{{55, 58}, {95, 99}, {148, 153}} {
		lbl := "publish"
		if pair == [2]int{95, 99} {
			lbl = "reject"
		}
		cases[pair[0]] = specialCase{label: lbl, exactDupOf: -1, nearDupOf: -1}
		cases[pair[1]] = specialCase{label: lbl, exactDupOf: -1, nearDupOf: pair[0]}
	}

	// Shared-ClusterID groups (same event, different sources/wording).
	for _, i := range []int{20, 22, 24} {
		cases[i] = specialCase{label: "publish", clusterID: "cluster-alpha", exactDupOf: -1, nearDupOf: -1}
	}
	for _, i := range []int{104, 106, 108} {
		cases[i] = specialCase{label: "reject", clusterID: "cluster-beta", exactDupOf: -1, nearDupOf: -1}
	}

	return cases
}

// syntheticExamples builds syntheticFixtureSize TrainingExamples
// programmatically: a handful of topic/source templates cycled over a
// loop (not hand-written literals), a ~70/30 publish/reject label
// distribution, PublishedAt spread over ~180 days across several
// SourceIDs, plus the exact-dup/near-dup/ClusterID/veto special cases
// syntheticSpecialCases lays out.
func syntheticExamples() []editorialtrain.TrainingExample {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	sources := []string{"sportsnet-nfl", "springfield-herald", "isotopes-official", "fan-blog-network", "sports-wire"}
	publishTopics := []string{
		"Isotopes win overtime thriller against division rival",
		"Isotopes injury report signals lineup changes",
		"Isotopes sign veteran pass rusher in free agency",
		"Isotopes rookie shines in preseason debut",
		"Isotopes coaching staff previews upcoming matchup",
		"Isotopes quarterback sets new franchise passing record",
	}
	rejectTopics := []string{
		"Sponsored shop now huge discount on team gear",
		"Advertisement weight loss supplement miracle results",
		"Unrelated celebrity gossip roundup for today",
		"Clickbait quiz which player are you really",
	}
	// publishFillersA/B (and rejectFillersA/B) are combined one-from-each
	// into every generic body, on top of its topic sentence, via i %
	// len(A) and i % len(B) — deliberately sized 11 and 13 (coprime to
	// each other and to the 6/4-topic cycle) so the combined period of
	// (topic, fillerA, fillerB) is lcm(6,11,13) = 858, far longer than
	// syntheticFixtureSize. That guarantees no two *unintended* indices
	// in the whole 0-179 range ever reconstruct the same body modulo
	// only its ref number/source. This matters for NearDupThreshold=0.5:
	// two same-topic bodies differing only by index number and source
	// would otherwise shingle-overlap far above the threshold, and
	// Split's leakage grouping would (correctly, per its own contract)
	// chain them — and everything transitively near-dup to them — into
	// one group anchored at its earliest member, starving
	// validate/test. Sizing the banks to avoid periodic collisions
	// keeps grouping restricted to the *intentional* near-dup/exact-dup/
	// cluster fixtures below.
	publishFillersA := []string{
		"The coaching staff addressed questions after Wednesday's practice.",
		"Local beat reporters gathered outside the practice facility Tuesday.",
		"Season ticket holders received an email update this week.",
		"The broadcast crew previewed the matchup during the pregame show.",
		"A former player offered analysis on the postgame radio show.",
		"Fans shared reactions across social media throughout the afternoon.",
		"The team's official channel posted new content on Thursday night.",
		"Weather conditions could play a factor heading into the weekend.",
		"Ticket sales for the next home game opened Monday morning.",
		"The stadium unveiled new signage ahead of the season opener.",
		"Analysts debated roster decisions during the midweek broadcast.",
	}
	publishFillersB := []string{
		"A youth clinic drew hundreds of participants over the weekend.",
		"The general manager met with reporters after the team flight.",
		"Practice squad moves were finalized ahead of Sunday's roster deadline.",
		"The equipment staff prepared new uniforms for the road trip.",
		"A national broadcast crew arrived in town for the primetime game.",
		"Longtime season subscribers received priority access to playoff packages.",
		"The team's training facility hosted a charity blood drive Friday.",
		"Assistant coaches reviewed film late into Tuesday evening.",
		"A rookie orientation session covered media training basics.",
		"The mascot made an appearance at a local elementary school.",
		"Beat writers filed notes from the team's walkthrough session.",
		"The front office extended a scouting trip through the weekend.",
		"A veteran leadership council met privately before Thursday's practice.",
	}
	rejectFillersA := []string{
		"Readers are encouraged to click the link below right now.",
		"Limited stock remains so act before the offer expires today.",
		"This message was sponsored by an unrelated third-party advertiser.",
		"Follow the account and share for a chance to win big.",
		"Our partners have prepared an exclusive deal just for you.",
		"Terms and conditions apply, see the fine print for details.",
		"This post is unrelated to football but trending elsewhere online.",
		"A celebrity was spotted somewhere completely unrelated to sports.",
		"The listicle ranks unrelated topics with no football connection.",
		"Comment below to enter today's unrelated giveaway drawing.",
		"This roundup covers gossip with nothing to do with the team.",
	}
	rejectFillersB := []string{
		"An unrelated influencer promoted the product in a viral video.",
		"A mystery box subscription service is advertised at the bottom.",
		"The banner ad rotates through several unrelated retail promotions.",
		"A pop-up survey interrupts the page before the content loads.",
		"An affiliate link tracks clicks for the sponsoring retailer.",
		"The article recycles wire copy with no original reporting.",
		"A countdown timer pressures readers to act before it resets.",
		"The page auto-refreshes every few seconds to inflate view counts.",
		"An unrelated horoscope segment is appended below the fold.",
		"A chatbot widget offers unrelated product recommendations.",
		"The sidebar promotes unrelated merchandise from a different brand.",
		"A newsletter signup modal blocks the rest of the page.",
		"An unrelated crossword puzzle is embedded at the very bottom.",
	}
	// vetoTitles/vetoBodies are seven distinct sentences, each containing
	// vetoSeedPhrase but otherwise sharing little text with one another
	// — deliberately, so the seeded veto examples themselves don't
	// near-dup-group together (which would collapse them into a single
	// partition and defeat spreading them across train/validate/test).
	vetoTitles := []string{
		"Exclusive signup bonus available this week",
		"New account promotion unlocked today",
		"Special welcome offer live now",
		"Limited time deal for new members",
		"Bonus credit promotion begins today",
		"Big kickoff weekend promotion announced",
		"Fresh account reward now available",
	}
	vetoBodies := []string{
		"Sign up now and claim your free bets bonus instantly.",
		"New customers receive free bets after their first deposit today.",
		"This weekend only, unlock free bets with any qualifying entry.",
		"Free bets are waiting for anyone who registers this week.",
		"Claim exclusive free bets before Sunday's kickoff arrives.",
		"This special promo grants free bets to first time users only.",
		"Register in minutes and receive free bets plus bonus credit now.",
	}

	// genericBody builds a title+body for a templated topic sentence,
	// diluted with one filler sentence from each of two independently-
	// sized banks so that different indices reusing the same recycled
	// topic don't shingle-overlap above NearDupThreshold — see the
	// filler bank comment above for why fillersA/fillersB must have
	// coprime-ish lengths rather than sharing one bank.
	genericBody := func(topic string, i int, source string, fillersA, fillersB []string) (title, body string) {
		f1 := fillersA[i%len(fillersA)]
		f2 := fillersB[i%len(fillersB)]
		title = fmt.Sprintf("%s (%d)", topic, i)
		body = fmt.Sprintf("%s. %s %s (ref %d, via %s)", topic, f1, f2, i, source)
		return title, body
	}

	special := syntheticSpecialCases()
	examples := make([]editorialtrain.TrainingExample, syntheticFixtureSize)
	generic := 0  // position counter among non-special indices only, drives the 70/30 split
	vetoSeen := 0 // position counter among veto special cases only, indexes into vetoTitles/vetoBodies

	for i := 0; i < syntheticFixtureSize; i++ {
		publishedAt := base.AddDate(0, 0, i)
		source := sources[i%len(sources)]

		sc, isSpecial := special[i]

		var title, body, label, clusterID string
		switch {
		case isSpecial && sc.exactDupOf >= 0:
			// Reuse the earlier example's text verbatim — an exact
			// duplicate under dedup.go's normalized title+body key.
			title, body = examples[sc.exactDupOf].Title, examples[sc.exactDupOf].Body
			label = sc.label
		case isSpecial && sc.nearDupOf >= 0:
			// Reworded but still >= NearDupThreshold shingle-similar:
			// append one clause rather than changing the core sentence.
			title = examples[sc.nearDupOf].Title
			body = examples[sc.nearDupOf].Body + " Updated with additional context today."
			label = sc.label
		case isSpecial && sc.veto:
			label = "reject"
			title = fmt.Sprintf("%s (%d)", vetoTitles[vetoSeen%len(vetoTitles)], i)
			body = fmt.Sprintf("%s (ref %d)", vetoBodies[vetoSeen%len(vetoBodies)], i)
			vetoSeen++
		case isSpecial:
			label = sc.label
			if label == "publish" {
				topic := publishTopics[i%len(publishTopics)]
				title, body = genericBody(topic, i, source, publishFillersA, publishFillersB)
			} else {
				topic := rejectTopics[i%len(rejectTopics)]
				title, body = genericBody(topic, i, source, rejectFillersA, rejectFillersB)
			}
		default:
			if generic%10 < 7 {
				label = "publish"
				topic := publishTopics[i%len(publishTopics)]
				title, body = genericBody(topic, i, source, publishFillersA, publishFillersB)
			} else {
				label = "reject"
				topic := rejectTopics[i%len(rejectTopics)]
				title, body = genericBody(topic, i, source, rejectFillersA, rejectFillersB)
			}
			generic++
		}
		if isSpecial {
			clusterID = sc.clusterID
		}

		examples[i] = editorialtrain.TrainingExample{
			ArticleID:       fmt.Sprintf("synth-%04d", i),
			Title:           title,
			Body:            body,
			SourceID:        source,
			SourceType:      "rss",
			Domain:          source + ".example.com",
			ContentType:     "article",
			PublishedAt:     publishedAt,
			FetchedAt:       publishedAt.Add(2 * time.Hour),
			ClusterID:       clusterID,
			IsPrimarySource: source == "isotopes-official",
			Label:           label,
			LabeledAt:       publishedAt.Add(24 * time.Hour),
		}
	}
	return examples
}

// partitionSplitConfig is shared by every Train/Split call in this test
// so the partition boundaries (and therefore which special-case indices
// land where) stay consistent across assertions.
func partitionSplitConfig() editorialtrain.SplitConfig {
	return editorialtrain.SplitConfig{
		TrainRatio: 0.7, ValidateRatio: 0.15, TestRatio: 0.15,
		NearDupThreshold: 0.5,
	}
}

func permissiveGateConfig() editorialtrain.GateConfig {
	return editorialtrain.GateConfig{
		MinPrecision:          0.4,
		MaxRecallRegression:   1.0,
		MinPRAUC:              0.0,
		MaxFalsePositiveRate:  1.0,
		MinPerSourcePrecision: 0.0,
		MinSourceExamples:     1_000_000, // effectively disables the per-source gate for this fixture's size
		MaxArtifactSizeBytes:  10 << 20,
		MaxInferenceNanos:     1 << 30,
	}
}

func TestEndToEnd_SyntheticCorpusTrainGateVetoPromoteRollback(t *testing.T) {
	examples := syntheticExamples()
	dataset := editorialtrain.Dataset{Examples: examples}

	trainCfg := editorialtrain.TrainConfig{
		Split:         partitionSplitConfig(),
		Prune:         editorialtrain.PruneConfig{MinDocFreq: 0.01, MaxDocFreq: 0.9, MaxFeatures: 2000},
		Thresholds:    editorialtrain.ThresholdConfig{AcceptTargetPrecision: 0.9, RejectTargetPrecision: 0.6},
		Seed:          42,
		SchemaVersion: "integration-test-fs-v1",
		VetoCfg:       syntheticVetoConfig(),
	}

	// --- Structural sanity: Split's own no-leakage guarantee holds when
	// exercised against a realistically-sized, deliberately-messy corpus
	// (dup/near-dup/cluster groups included), not just the tiny fixtures
	// split_test.go already covers in isolation.
	train, validate, test := editorialtrain.Split(dataset, trainCfg.Split)
	seen := map[string]string{} // ArticleID -> partition name
	assertUnseen := func(partition string, ex editorialtrain.TrainingExample) {
		if prior, ok := seen[ex.ArticleID]; ok {
			t.Fatalf("ArticleID %q appears in both %q and %q partitions", ex.ArticleID, prior, partition)
		}
		seen[ex.ArticleID] = partition
	}
	for _, ex := range train.Examples {
		assertUnseen("train", ex)
	}
	for _, ex := range validate.Examples {
		assertUnseen("validate", ex)
	}
	for _, ex := range test.Examples {
		assertUnseen("test", ex)
	}
	if len(seen) != syntheticFixtureSize {
		t.Fatalf("total examples across partitions = %d, want %d (every ArticleID accounted for exactly once)", len(seen), syntheticFixtureSize)
	}

	// --- RunAndRegister + SelectWinner: trivial_majority is produced,
	// passes gates (a permissive config, so its exclusion below is
	// actually about SelectWinner's own policy, not it merely failing
	// gates), and is never selected as the winner.
	registry := &fakeModelRegistry{}
	results, err := modeltraining.RunAndRegister(context.Background(), modeltraining.Deps{Registry: registry},
		dataset, trainCfg, t.TempDir(), permissiveGateConfig(), nil)
	if err != nil {
		t.Fatalf("modeltraining.RunAndRegister() error = %v", err)
	}

	// --- At least one trained candidate's veto impact actually observed
	// the seeded phrase triggering (not merely a non-nil, all-zero
	// struct). RunAndRegister has already trained every algorithm, so
	// inspecting its candidates avoids repeating that expensive work.
	var sawVetoTrigger bool
	for _, r := range results {
		if r.Candidate.TestReport.VetoImpact != nil && r.Candidate.TestReport.VetoImpact.Triggered > 0 {
			sawVetoTrigger = true
		}
		if r.Candidate.ValidateReport.VetoImpact != nil && r.Candidate.ValidateReport.VetoImpact.Triggered > 0 {
			sawVetoTrigger = true
		}
	}
	if !sawVetoTrigger {
		t.Error("no candidate's VetoImpact.Triggered > 0 on either validate or test — the seeded veto phrase never reached those partitions")
	}

	var trivialResult modeltraining.Result
	var sawTrivial bool
	for _, r := range results {
		if r.Candidate.Model.Algorithm == "trivial_majority" {
			trivialResult = r
			sawTrivial = true
		}
	}
	if !sawTrivial {
		t.Fatal("RunAndRegister() results have no trivial_majority candidate")
	}
	if !trivialResult.Gates.Passed {
		t.Fatalf("trivial_majority candidate failed gates (%v) — need it to pass so its exclusion below demonstrates SelectWinner's policy, not a coincidental gate failure", trivialResult.Gates.Failures)
	}

	winner, hasWinner := modeltraining.SelectWinner(results)
	if !hasWinner {
		t.Fatal("SelectWinner() found no winner among gate-passing candidates")
	}
	if winner.Candidate.Model.Algorithm == "trivial_majority" {
		t.Error("SelectWinner() chose the trivial_majority candidate, want it always excluded regardless of gate status")
	}

	// --- Cost sweep: ExpectedCost genuinely responds to different
	// ThresholdConfig choices, not a coincidental match. Train the same
	// naive_bayes algorithm (deterministic given the fixed seed) under
	// two different threshold configs and compare its Test report's
	// ExpectedCost under one fixed CostConfig.
	costs := editorialtrain.CostConfig{FalsePublishCost: 5, MissedNewsCost: 1}
	sweepConfigs := []editorialtrain.ThresholdConfig{
		{AcceptTargetPrecision: 0.95, RejectTargetPrecision: 0.95},
		{AcceptTargetPrecision: 0.5, RejectTargetPrecision: 0.5},
	}
	sweepCosts := make([]float64, len(sweepConfigs))
	for i, tc := range sweepConfigs {
		cfg := trainCfg
		cfg.Thresholds = tc
		cfg.Algorithms = []string{"naive_bayes"}
		cands, err := editorialtrain.Train(dataset, cfg)
		if err != nil {
			t.Fatalf("Train() with sweep config %d error = %v", i, err)
		}
		if len(cands) != 1 {
			t.Fatalf("Train() with sweep config %d returned %d candidates, want 1", i, len(cands))
		}
		sweepCosts[i] = cands[0].TestReport.ExpectedCost(costs)
	}
	if sweepCosts[0] == sweepCosts[1] {
		t.Errorf("ExpectedCost identical (%v) across two different ThresholdConfig sweeps (%+v vs %+v) — expected the cost model to respond to threshold changes",
			sweepCosts[0], sweepConfigs[0], sweepConfigs[1])
	}

	// --- Promote then Rollback end to end against a temp
	// models/editorial/current directory, using two of the real trained
	// candidates from RunAndRegister above (never trivial_majority —
	// that floor is never meant to be promotable).
	var first, second modeltraining.Result
	for _, r := range results {
		if r.Candidate.Model.Algorithm == "trivial_majority" {
			continue
		}
		if first.Dir == "" {
			first = r
		} else if second.Dir == "" && r.Candidate.Model.Algorithm != first.Candidate.Model.Algorithm {
			second = r
		}
	}
	if first.Dir == "" || second.Dir == "" {
		t.Fatal("need at least two distinct non-trivial candidates to exercise Promote then Rollback")
	}

	base := t.TempDir()
	modelPath := filepath.Join(base, "models", "editorial", "current")
	previousPath := filepath.Join(base, "models", "editorial", "previous")
	promoteDeps := modelpromotion.Deps{Registry: registry, UOW: fakeUOW{}}

	if err := modelpromotion.Promote(context.Background(), promoteDeps, first.Dir, "v1.0.0", modelPath); err != nil {
		t.Fatalf("first Promote() error = %v", err)
	}
	if err := modelpromotion.Promote(context.Background(), promoteDeps, second.Dir, "v2.0.0", modelPath); err != nil {
		t.Fatalf("second Promote() error = %v", err)
	}

	beforeRollback, err := editorialmodel.Load(modelPath)
	if err != nil || beforeRollback.Version != "v2.0.0" {
		t.Fatalf("current model before rollback = %+v, err=%v, want version v2.0.0", beforeRollback, err)
	}

	if err := modelpromotion.Rollback(context.Background(), promoteDeps, modelPath); err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}

	afterRollback, err := editorialmodel.Load(modelPath)
	if err != nil {
		t.Fatalf("editorialmodel.Load(%s) after rollback error = %v", modelPath, err)
	}
	if afterRollback.Version != "v1.0.0" {
		t.Errorf("current model after rollback Version = %q, want %q (the prior, pre-second-promotion version)", afterRollback.Version, "v1.0.0")
	}
	if afterRollback.Algorithm != first.Candidate.Model.Algorithm {
		t.Errorf("current model after rollback Algorithm = %q, want %q", afterRollback.Algorithm, first.Candidate.Model.Algorithm)
	}

	rolledAside, err := editorialmodel.Load(previousPath)
	if err != nil || rolledAside.Version != "v2.0.0" {
		t.Fatalf("previous slot after rollback = %+v, err=%v, want version v2.0.0 (the rolled-back-from model)", rolledAside, err)
	}
}
