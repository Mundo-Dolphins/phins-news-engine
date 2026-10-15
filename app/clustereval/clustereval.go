// Package clustereval offers offline scoring of a hand-labeled dataset of
// article groups — each group representing one real-world event — against
// the configured event-clustering rules, so join/ambiguous/create
// thresholds and representative-ranking weights can be calibrated against
// real examples without touching code. See the phins-news-engine-tools
// "clustering evaluate" command.
//
// clustereval mirrors dedupeval's shape: a YAML dataset loader kept
// separate from a pure Evaluate function, and metrics exposed as methods on
// a small struct (PairwiseMetrics, mirroring dedupeval.BinaryMetrics). It
// differs from dedupeval in what it measures: dedupeval scores independent
// pairs against a binary same/different label; clustereval simulates
// clustering.DecideJoin incrementally over an entire dataset (see Evaluate)
// and scores the resulting cluster assignment against each article's gold
// group with pairwise precision/recall/F1, purity, over-grouping,
// fragmentation, and representative-ranking accuracy.
package clustereval

import (
	"fmt"
	"os"
	"sort"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/clustering"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/editorial"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
)

// DatasetArticle is one article within a labeled Group. Every field besides
// ID/Title feeds eventsignals.Extract (mirroring the subset of
// article.Article it actually reads) or clustering's representative-ranking
// signals; none of it is read directly by clustering.DecideJoin itself.
type DatasetArticle struct {
	ID          string    `yaml:"id"`
	Title       string    `yaml:"title"`
	Summary     string    `yaml:"summary"`
	SourceID    string    `yaml:"source_id"`
	SourceType  string    `yaml:"source_type"`
	Domain      string    `yaml:"domain"`
	ContentType string    `yaml:"content_type"`
	PublishedAt time.Time `yaml:"published_at"`

	// ExpectedRole, if set, is compared against the clustering.MemberRole
	// DecideJoin actually assigned this article (or "first_report" for
	// whichever article started its predicted cluster) — see Report.
	// RoleTotal/RoleCorrect. Optional; "" means this article's role is not
	// checked.
	ExpectedRole string `yaml:"expected_role"`

	// IsPrimarySource feeds clustering.RepresentativeCandidate.
	// IsPrimarySource for representative-accuracy scoring.
	IsPrimarySource bool `yaml:"is_primary_source"`

	// ExpectedAmbiguous marks an article the dataset author expects
	// clustering.DecideJoin to return ActionAmbiguous for. It is still run
	// through DecideJoin exactly like every other article (so
	// Report.AmbiguousCorrectlyFlagged can confirm the expectation actually
	// holds, and so it remains a real candidate for later articles), but it
	// is excluded from the pairwise/purity/over-grouped/fragmented
	// calculations — see Evaluate's doc comment for the full reasoning.
	ExpectedAmbiguous bool `yaml:"expected_ambiguous"`
}

// Group is a set of articles the dataset author asserts describe the same
// real-world event: every article in Articles should end up in the same
// predicted cluster, and no article outside Articles should join it.
type Group struct {
	ID string `yaml:"id"`

	// Representative, if set, is the ArticleID of the article the dataset
	// author expects clustering.RankRepresentative to rank first among this
	// group's actual predicted-cluster members. Optional: a group with no
	// clear single best representative (an "ambiguous" case for
	// representative selection specifically) can leave this empty and it is
	// simply excluded from Report.RepresentativeTotal.
	Representative string `yaml:"representative"`

	Articles []DatasetArticle `yaml:"articles"`
}

// Dataset is a labeled set of event groups.
type Dataset struct {
	Groups []Group `yaml:"groups"`
}

// LoadDataset reads and parses a YAML dataset file.
func LoadDataset(path string) (Dataset, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- path is an operator-provided CLI flag, not user input
	if err != nil {
		return Dataset{}, fmt.Errorf("clustereval: read %s: %w", path, err)
	}
	var dataset Dataset
	if err := yaml.Unmarshal(data, &dataset); err != nil {
		return Dataset{}, fmt.Errorf("clustereval: parse %s: %w", path, err)
	}
	return dataset, nil
}

// LatestPublishedAt returns the latest DatasetArticle.PublishedAt across
// every group in dataset, or the zero time.Time for an empty dataset.
// Offline evaluators use it as Evaluate's now so a given dataset always
// scores identically, whatever the wall clock says.
func LatestPublishedAt(dataset Dataset) time.Time {
	var latest time.Time
	for _, g := range dataset.Groups {
		for _, a := range g.Articles {
			if a.PublishedAt.After(latest) {
				latest = a.PublishedAt
			}
		}
	}
	return latest
}

// PairwiseMetrics is a confusion-matrix-derived precision/recall/F1 view
// over article pairs, mirroring dedupeval.BinaryMetrics exactly (same
// fields, same methods, same zero-denominator-is-zero convention) — see
// Evaluate's doc comment for what TP/FP/FN/TN mean in this pairwise
// context.
type PairwiseMetrics struct {
	TP, FP, FN, TN int
}

// Precision returns TP/(TP+FP), or 0 when the denominator is 0.
func (m PairwiseMetrics) Precision() float64 {
	if m.TP+m.FP == 0 {
		return 0
	}
	return float64(m.TP) / float64(m.TP+m.FP)
}

// Recall returns TP/(TP+FN), or 0 when the denominator is 0.
func (m PairwiseMetrics) Recall() float64 {
	if m.TP+m.FN == 0 {
		return 0
	}
	return float64(m.TP) / float64(m.TP+m.FN)
}

// F1 returns the harmonic mean of Precision and Recall, or 0 when both are 0.
func (m PairwiseMetrics) F1() float64 {
	p, r := m.Precision(), m.Recall()
	if p+r == 0 {
		return 0
	}
	return 2 * p * r / (p + r)
}

// RoleAssignment is one article's predicted cluster placement, kept for
// audit/CLI printing.
type RoleAssignment struct {
	ArticleID          string
	GoldGroupID        string
	PredictedClusterID string
	Action             clustering.Action
	MemberRole         clustering.MemberRole
	ExpectedAmbiguous  bool
	Excluded           bool
}

// Report is the full output of Evaluate.
type Report struct {
	ArticlesTotal  int
	ArticlesScored int // ArticlesTotal minus ExpectedAmbiguous exclusions

	Pairwise PairwiseMetrics
	Purity   float64

	OverGroupedClusters int
	FragmentedClusters  int

	RepresentativeTotal    int
	RepresentativeCorrect  int
	RepresentativeAccuracy float64

	AmbiguousExpected         int
	AmbiguousCorrectlyFlagged int
	// UnexpectedAmbiguous counts articles DecideJoin returned
	// ActionAmbiguous for that the dataset did NOT mark ExpectedAmbiguous —
	// a genuine prediction error, not excluded from Pairwise/Purity/
	// OverGroupedClusters/FragmentedClusters (see Evaluate's doc comment).
	UnexpectedAmbiguous int

	// RoleTotal/RoleCorrect count DatasetArticle.ExpectedRole checks: how
	// many articles declared an expected role, and how many of those
	// matched the role DecideJoin (or Evaluate's first-report convention)
	// actually assigned.
	RoleTotal   int
	RoleCorrect int

	// Assignments lists every article's predicted placement, in the order
	// Evaluate processed them (see processingOrder), for CLI/audit
	// printing.
	Assignments []RoleAssignment
}

// processed is Evaluate's running record for one already-processed article:
// its own event Representation (computed once, reused both as a later
// article's candidate and, for representative-accuracy, directly as a
// RepresentativeCandidate input) plus everything Evaluate needs to score it
// afterward.
type processed struct {
	DatasetArticle
	GoldGroupID        string
	Representation     eventsignals.Representation
	PredictedClusterID string
	Action             clustering.Action
	MemberRole         clustering.MemberRole
}

// Evaluate runs clustering's real decision logic incrementally over every
// article in dataset — in ascending PublishedAt order (ties broken by
// ArticleID for determinism), mirroring the order articles would actually
// arrive in production — and scores the resulting predicted cluster
// assignment against dataset's gold groups.
//
// Evaluation mechanics: this is an offline evaluation of a small labeled
// dataset, not the production SQL-bounded candidate search
// (ClusterRepository.FindCandidatesByEventKeys); for each article, every
// previously processed article is offered to clustering.DecideJoin as a
// candidate (an in-memory, all-previously-seen-articles-as-candidates
// approach, appropriate and simpler for a dataset this size than
// replicating that bounded lookup). Each article's own
// eventsignals.Representation is computed once via eventsignals.Extract and
// reused both when it is later offered as a candidate and, for
// representative-accuracy, as direct RankRepresentative input — never
// recomputed.
//
// Predicted cluster assignment: ActionJoin uses Decision.ClusterID/
// MemberRole verbatim. ActionCreate and ActionAmbiguous both start a new
// predicted cluster (ID: "cluster:"+ArticleID) with MemberRole
// MemberRoleFirstReport — mirroring ProcessCycle's actual production
// behavior of always creating a new open cluster for ActionAmbiguous too
// (the one-cluster-per-article invariant), so this evaluator's simulated
// pipeline stays faithful to what production actually does. This is a
// deliberate choice among two the task brief flagged as ours to make:
// silently excluding an ActionAmbiguous article from clustering entirely
// would evaluate a pipeline that doesn't match production; assigning it a
// cluster the same way ActionCreate does keeps the simulation faithful
// while ambiguity itself is still tracked (see below) and, for articles the
// dataset did NOT expect to be ambiguous, still counted as a real
// prediction error via the pairwise metrics below (an article whose gold
// group expected it to join an existing cluster, but which
// unexpectedly triggered ActionAmbiguous and started its own new cluster
// instead, naturally shows up as a same-gold/different-predicted pair —
// i.e. a false negative on recall — with no special-casing needed).
//
// Ambiguous handling: DatasetArticle.ExpectedAmbiguous marks an article the
// dataset author already flagged as a known hard case. It is still run
// through DecideJoin in full (so AmbiguousCorrectlyFlagged can confirm the
// expectation holds, and so later articles see it as a real candidate,
// exactly as production would), but it is excluded from the Pairwise/
// Purity/OverGroupedClusters/FragmentedClusters calculations entirely —
// matching dedupeval's "ambiguous pairs excluded, counted separately"
// pattern. An article the dataset did NOT mark ExpectedAmbiguous that
// DecideJoin nonetheless calls ActionAmbiguous on is a different case
// entirely: that is itself a metric-worthy prediction error (see previous
// paragraph), so it is deliberately NOT excluded — only counted separately
// via UnexpectedAmbiguous for visibility.
//
// Representative accuracy: for every Group with a non-empty Representative,
// Evaluate finds that article's actual predicted cluster, gathers every
// article (from the full, unfiltered processed set — ambiguous-excluded or
// not) that landed in that same predicted cluster, and runs
// clustering.RankRepresentative over their stored Representations. It
// counts as correct only when the top-ranked result is exactly the
// declared Representative.
func Evaluate(dataset Dataset, rules eventsignals.Rules, decide clustering.Config, rep clustering.RepresentativeConfig, now time.Time) (Report, error) {
	order := processingOrder(dataset)

	byID := make(map[string]*processed, len(order))
	report := Report{ArticlesTotal: len(order)}

	for _, item := range order {
		p, err := runOne(item, byID, order, rules, decide, now)
		if err != nil {
			return Report{}, err
		}
		byID[p.ID] = p
		report.Assignments = append(report.Assignments, RoleAssignment{
			ArticleID:          p.ID,
			GoldGroupID:        p.GoldGroupID,
			PredictedClusterID: p.PredictedClusterID,
			Action:             p.Action,
			MemberRole:         p.MemberRole,
			ExpectedAmbiguous:  p.ExpectedAmbiguous,
			Excluded:           p.ExpectedAmbiguous,
		})

		actualAmbiguous := p.Action == clustering.ActionAmbiguous
		switch {
		case p.ExpectedAmbiguous:
			report.AmbiguousExpected++
			if actualAmbiguous {
				report.AmbiguousCorrectlyFlagged++
			}
		case actualAmbiguous:
			report.UnexpectedAmbiguous++
		}

		if p.ExpectedRole != "" {
			report.RoleTotal++
			if string(p.MemberRole) == p.ExpectedRole {
				report.RoleCorrect++
			}
		}
	}

	scored := make([]*processed, 0, len(order))
	for _, item := range order {
		p := byID[item.ID]
		if !p.ExpectedAmbiguous {
			scored = append(scored, p)
		}
	}
	report.ArticlesScored = len(scored)

	report.Pairwise = computePairwise(scored)
	report.Purity = computePurity(scored)
	report.OverGroupedClusters = countOverGrouped(scored)
	report.FragmentedClusters = countFragmented(scored)

	total, correct, err := computeRepresentativeAccuracy(dataset, byID, rep, now)
	if err != nil {
		return Report{}, err
	}
	report.RepresentativeTotal = total
	report.RepresentativeCorrect = correct
	if total > 0 {
		report.RepresentativeAccuracy = float64(correct) / float64(total)
	}

	return report, nil
}

// CountChangedSignals returns how many dataset articles get a different
// EventType, Action or ContentSignal from eventsignals.Extract under after
// than under before. Zero means a rule change does not touch the dataset,
// so a gate built on Evaluate passes without being exercised.
func CountChangedSignals(dataset Dataset, before, after eventsignals.Rules, now time.Time) (int, error) {
	n := 0
	for _, item := range processingOrder(dataset) {
		art, err := toArticle(item.DatasetArticle)
		if err != nil {
			return 0, err
		}
		b, a := eventsignals.Extract(art, before, now), eventsignals.Extract(art, after, now)
		if b.EventType != a.EventType || b.Action != a.Action || b.ContentSignal != a.ContentSignal {
			n++
		}
	}
	return n, nil
}

// flatArticle is one dataset article paired with its gold group ID, before
// any clustering decision has been made.
type flatArticle struct {
	DatasetArticle
	GoldGroupID string
}

// processingOrder flattens dataset's groups into one global list, sorted by
// PublishedAt ascending (ties broken by ArticleID for determinism) — the
// order Evaluate simulates articles "arriving", mirroring how the
// production pipeline processes articles as they are published rather than
// however the dataset file happens to declare them.
func processingOrder(dataset Dataset) []flatArticle {
	var order []flatArticle
	for _, g := range dataset.Groups {
		for _, a := range g.Articles {
			order = append(order, flatArticle{DatasetArticle: a, GoldGroupID: g.ID})
		}
	}
	sort.SliceStable(order, func(i, j int) bool {
		if !order[i].PublishedAt.Equal(order[j].PublishedAt) {
			return order[i].PublishedAt.Before(order[j].PublishedAt)
		}
		return order[i].ID < order[j].ID
	})
	return order
}

// toArticle converts a DatasetArticle into the article.Article
// eventsignals.Extract expects, populating only the fields Extract actually
// reads (see eventsignals.Extract's doc comment: Title, Summary, and the
// fields Representation copies verbatim).
func toArticle(a DatasetArticle) (article.Article, error) {
	sourceType := source.Type(a.SourceType)
	if sourceType == "" {
		sourceType = source.TypeRSS
	}
	contentType := article.ContentType(a.ContentType)
	if contentType == "" {
		contentType = article.ContentTypeArticle
	}
	// a.SourceID is a hand-authored dataset label (e.g. "src-official"),
	// and must satisfy source.NewID's per-platform charset for its
	// declared/defaulted source_type exactly like real ingested data does
	// — a malformed dataset label is a real fixture bug, not something to
	// paper over, so this propagates the error rather than silently
	// zeroing Source.ID (see Evaluate's single caller,
	// cli/cmd/clustering.go, which surfaces it to the
	// operator).
	id, err := source.NewID(sourceType, a.SourceID)
	if err != nil {
		return article.Article{}, fmt.Errorf("clustereval: article %s: invalid source_id %q for source_type %q: %w", a.ID, a.SourceID, sourceType, err)
	}
	return article.Article{
		ID:          a.ID,
		Title:       a.Title,
		Summary:     a.Summary,
		Domain:      a.Domain,
		ContentType: contentType,
		PublishedAt: a.PublishedAt,
		Source: source.Source{
			ID:   id,
			Name: a.SourceID,
			Type: sourceType,
		},
	}, nil
}

// runOne extracts item's Representation, offers every already-processed
// article (in processedSoFar/byID) to clustering.DecideJoin as a candidate,
// and returns item's full processed record — see Evaluate's doc comment for
// the predicted-cluster-assignment rules.
func runOne(item flatArticle, byID map[string]*processed, order []flatArticle, rules eventsignals.Rules, decide clustering.Config, now time.Time) (*processed, error) {
	art, err := toArticle(item.DatasetArticle)
	if err != nil {
		return nil, err
	}
	repn := eventsignals.Extract(art, rules, now)

	var candidates []clustering.Candidate
	for _, prior := range order {
		if prior.ID == item.ID {
			break
		}
		p, ok := byID[prior.ID]
		if !ok {
			continue
		}
		candidates = append(candidates, clustering.Candidate{
			ArticleID:      p.ID,
			ClusterID:      p.PredictedClusterID,
			Representation: p.Representation,
		})
	}

	decision := clustering.DecideJoin(repn, candidates, decide, now)

	p := &processed{
		DatasetArticle: item.DatasetArticle,
		GoldGroupID:    item.GoldGroupID,
		Representation: repn,
		Action:         decision.Action,
	}
	switch decision.Action {
	case clustering.ActionJoin:
		p.PredictedClusterID = decision.ClusterID
		p.MemberRole = decision.MemberRole
	default: // ActionCreate, ActionAmbiguous — see Evaluate's doc comment
		p.PredictedClusterID = "cluster:" + item.ID
		p.MemberRole = clustering.MemberRoleFirstReport
	}
	return p, nil
}

// computePairwise scores every unordered pair among scored: TP = same
// predicted cluster AND same gold group, FP = same predicted cluster but
// different gold group, FN = different predicted cluster but same gold
// group, TN = different predicted cluster and different gold group.
func computePairwise(scored []*processed) PairwiseMetrics {
	var m PairwiseMetrics
	for i := 0; i < len(scored); i++ {
		for j := i + 1; j < len(scored); j++ {
			samePred := scored[i].PredictedClusterID == scored[j].PredictedClusterID
			sameGold := scored[i].GoldGroupID == scored[j].GoldGroupID
			switch {
			case samePred && sameGold:
				m.TP++
			case samePred && !sameGold:
				m.FP++
			case !samePred && sameGold:
				m.FN++
			default:
				m.TN++
			}
		}
	}
	return m
}

// computePurity groups scored by PredictedClusterID, sums each predicted
// cluster's majority-gold-label count, and divides by len(scored). Returns
// 0 for an empty input rather than dividing by zero.
func computePurity(scored []*processed) float64 {
	if len(scored) == 0 {
		return 0
	}
	byCluster := groupByPredictedCluster(scored)

	clusterIDs := sortedKeys(byCluster)
	majoritySum := 0
	for _, clusterID := range clusterIDs {
		counts := map[string]int{}
		for _, p := range byCluster[clusterID] {
			counts[p.GoldGroupID]++
		}
		best := 0
		for _, c := range counts {
			if c > best {
				best = c
			}
		}
		majoritySum += best
	}
	return float64(majoritySum) / float64(len(scored))
}

// countOverGrouped counts predicted clusters (among scored) whose members
// span more than one distinct gold group — a false merge.
func countOverGrouped(scored []*processed) int {
	byCluster := groupByPredictedCluster(scored)
	count := 0
	for _, members := range byCluster {
		golds := map[string]bool{}
		for _, p := range members {
			golds[p.GoldGroupID] = true
		}
		if len(golds) > 1 {
			count++
		}
	}
	return count
}

// countFragmented counts gold groups (among scored) whose members span more
// than one distinct predicted cluster — a false split.
func countFragmented(scored []*processed) int {
	byGold := map[string]map[string]bool{}
	for _, p := range scored {
		if byGold[p.GoldGroupID] == nil {
			byGold[p.GoldGroupID] = map[string]bool{}
		}
		byGold[p.GoldGroupID][p.PredictedClusterID] = true
	}
	count := 0
	for _, clusters := range byGold {
		if len(clusters) > 1 {
			count++
		}
	}
	return count
}

func groupByPredictedCluster(scored []*processed) map[string][]*processed {
	byCluster := map[string][]*processed{}
	for _, p := range scored {
		byCluster[p.PredictedClusterID] = append(byCluster[p.PredictedClusterID], p)
	}
	return byCluster
}

func sortedKeys(m map[string][]*processed) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// computeRepresentativeAccuracy checks, for every Group with a non-empty
// Representative, whether clustering.RankRepresentative — run over every
// article that actually landed in the declared representative's predicted
// cluster (the full, unfiltered processed set) — ranks the declared
// Representative first.
func computeRepresentativeAccuracy(dataset Dataset, byID map[string]*processed, cfg clustering.RepresentativeConfig, now time.Time) (total, correct int, err error) {
	for _, g := range dataset.Groups {
		if g.Representative == "" {
			continue
		}
		want, ok := byID[g.Representative]
		if !ok {
			continue
		}
		total++

		var candidates []clustering.RepresentativeCandidate
		for _, p := range byID {
			if p.PredictedClusterID != want.PredictedClusterID {
				continue
			}
			art, artErr := toArticle(p.DatasetArticle)
			if artErr != nil {
				return 0, 0, artErr
			}
			candidates = append(candidates, clustering.RepresentativeCandidate{
				ArticleID:       p.ID,
				Representation:  p.Representation,
				IsPrimarySource: p.IsPrimarySource,
				Editorial:       editorial.Evaluate(art, p.IsPrimarySource, nil),
			})
		}
		sort.Slice(candidates, func(i, j int) bool { return candidates[i].ArticleID < candidates[j].ArticleID })

		ranked := clustering.RankRepresentative(candidates, cfg, now)
		if len(ranked) > 0 && ranked[0].ArticleID == g.Representative {
			correct++
		}
	}
	return total, correct, nil
}
