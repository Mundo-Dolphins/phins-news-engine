// Package entities holds the admin actions over the entity catalog and the
// LLM-discovered entity proposals. They mirror the `entities list|show|
// approve|reject|generate|discover|extract|fallback-test` CLI commands plus
// `entities proposals verify`, `entities league evaluate`, `entities roster
// sync` and `entities wikidata evaluate`. Everything that writes configs/ or
// data/ files, calls an LLM or the web, or rejects a proposal is local-only:
// proposals are reviewed on the synced local copy.
package entities

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	sqliteentityproposal "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqliteentityproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/action"
	"github.com/Mundo-Dolphins/phins-news-engine/app/entitydiscovery"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/entitywiring"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

const group = "entities"

var errBadParams = errors.New("entities: unexpected params type")

var (
	bothModes = []action.Mode{action.ModeLocal, action.ModeProduction}
	localOnly = []action.Mode{action.ModeLocal}
)

type listParams struct {
	Type   string `json:"type" help:"only proposals of this entity type" enum:"player,coach,executive,team"`
	Status string `json:"status" help:"only proposals in this status (all = every status)" enum:"pending,approved,rejected,all" default:"pending"`
}

type idParams struct {
	ID string `json:"id" help:"proposal id" required:"true"`
}

type approveParams struct {
	ID         string `json:"id" help:"proposal id" required:"true"`
	MergeInto  string `json:"merge_into" help:"for a possible_duplicate proposal: treat it as a new alias of this existing catalog entity id"`
	AsNew      bool   `json:"as_new" help:"for a possible_duplicate proposal: treat it as a brand new entity anyway"`
	Dictionary string `json:"dictionary" help:"team dictionary YAML file (default dictionary.path)" format:"path"`
	Manual     string `json:"manual" help:"manual entity catalog YAML file (default clustering.entity_catalog.manual_path)" format:"path"`
	Generated  string `json:"generated" help:"generated entity catalog YAML file, read for the duplicate check (default clustering.entity_catalog.generated_path)" format:"path"`
}

type generateParams struct {
	Dictionary string `json:"dictionary" help:"team dictionary YAML file (default dictionary.path)" format:"path"`
	Out        string `json:"out" help:"output path for the generated entity catalog (default clustering.entity_catalog.generated_path)" format:"path"`
}

type catalogParams struct {
	Manual    string `json:"manual" help:"manual entity catalog YAML file (default clustering.entity_catalog.manual_path)" format:"path"`
	Generated string `json:"generated" help:"generated entity catalog YAML file (default clustering.entity_catalog.generated_path)" format:"path"`
}

type rosterParams struct {
	URL  string `json:"url" help:"override the roster CSV URL for this run (default entity_discovery.roster.url, else the nflverse URL of the current season)"`
	Path string `json:"path" help:"write the roster here (default entity_discovery.roster.path)" format:"path"`
}

type extractParams struct {
	Title          string `json:"title" help:"article title"`
	Description    string `json:"description" help:"article description / summary"`
	Body           string `json:"body" help:"article body text"`
	Rules          string `json:"rules" help:"clustering rules file (default clustering.rules_path)" format:"path"`
	Manual         string `json:"manual" help:"manual entity catalog (default clustering.entity_catalog.manual_path)" format:"path"`
	Generated      string `json:"generated" help:"generated entity catalog (default clustering.entity_catalog.generated_path)" format:"path"`
	GeneratedRules string `json:"generated_rules" help:"generated event rules file merged into the rules (default clustering.generated_rules_path)" format:"path"`
	League         bool   `json:"league" help:"also recognise league-wide players from the nflverse roster (mentions only)"`
	Roster         string `json:"roster" help:"nflverse roster CSV for league (default clustering.entity_catalog.league.roster_path)" format:"path"`
	LeagueRules    string `json:"league_rules" help:"league alias rules for league (default clustering.entity_catalog.league.rules_path)" format:"path"`
}

type fallbackParams struct {
	Candidate string `json:"candidate" help:"candidate text to resolve" required:"true"`
	Context   string `json:"context" help:"surrounding context text the candidate appeared in" required:"true"`
	Field     string `json:"field" help:"field the candidate was found in" enum:"title,description,body" default:"title"`
}

type wikidataParams struct {
	Limit    int    `json:"limit" help:"maximum proposals to evaluate (0 = no cap; ignored with sample)" default:"0"`
	Sample   int    `json:"sample" help:"evaluate a deterministic random sample of this size (e.g. 200-500 for the quality gate)" default:"0"`
	Seed     int64  `json:"seed" help:"seed for sample" default:"1"`
	Status   string `json:"status" help:"proposal status to evaluate" enum:"pending,approved,rejected,all" default:"pending"`
	Kind     string `json:"kind" help:"only evaluate this proposal kind" enum:"new_entity,new_alias,possible_duplicate"`
	Type     string `json:"type" help:"only evaluate this entity type" enum:"player,coach,executive,team"`
	Reviewed string `json:"reviewed" help:"CSV of proposal_id,verdict (correct|wrong) from manual review; adds the observed false-resolution rate" format:"path"`
}

// Actions returns the entities actions: entities.list, entities.show,
// entities.approve, entities.reject, entities.generate, entities.discover,
// entities.proposals_verify, entities.extract, entities.fallback_test,
// entities.league_evaluate, entities.roster_sync and
// entities.wikidata_evaluate.
func Actions() []action.Action {
	return []action.Action{
		{
			Name: "entities.list", Group: group, Title: "List entity proposals",
			Description: "Lists LLM-discovered entity proposals, best-scored first.",
			Kind:        action.KindRead, Modes: bothModes, Params: listParams{}, Run: runList,
		},
		{
			Name: "entities.show", Group: group, Title: "Show entity proposal",
			Description: "Shows one entity proposal with its aliases, metadata, web sources and example news.",
			Kind:        action.KindRead, Modes: bothModes, Params: idParams{}, Run: runShow,
		},
		{
			Name: "entities.approve", Group: group, Title: "Approve entity proposal",
			Description: "Approves a pending entity proposal and writes it into the team dictionary or the manual entity catalog.",
			Kind:        action.KindWrite, Modes: localOnly, Params: approveParams{}, Run: runApprove,
		},
		{
			Name: "entities.reject", Group: group, Title: "Reject entity proposal",
			Description: "Rejects a pending entity proposal; no catalog file is touched.",
			Kind:        action.KindWrite, Modes: localOnly, Params: idParams{}, Run: runReject,
		},
		{
			Name: "entities.generate", Group: group, Title: "Generate entity catalog",
			Description: "Regenerates the player/coach/executive half of the entity catalog from the team dictionary.",
			Kind:        action.KindWrite, Modes: localOnly, Params: generateParams{}, Run: runGenerate,
		},
		{
			Name: "entities.discover", Group: group, Title: "Discover entities",
			Description: "Mines unknown entities and aliases from stored news through the LLM, saving them as proposals.",
			Kind:        action.KindWrite, Modes: localOnly, LongRunning: true, Exclusive: true,
			Params: discoverParams{}, Run: runDiscover,
		},
		{
			Name: "entities.proposals_verify", Group: group, Title: "Verify entity proposals",
			Description: "Verifies the pending proposal backlog through Wikidata and the web fallback using stored sources (resumable, batched with limit).",
			Kind:        action.KindWrite, Modes: localOnly, LongRunning: true, Exclusive: true,
			Params: verifyParams{}, Run: runVerify,
		},
		{
			Name: "entities.extract", Group: group, Title: "Extract entities from text",
			Description: "Runs the production deterministic entity extractor on a text and shows what it recognises (read-only, no network).",
			Kind:        action.KindRead, Modes: bothModes, Params: extractParams{}, Run: runExtract,
		},
		{
			Name: "entities.fallback_test", Group: group, Title: "Test entity fallback",
			Description: "Sends one candidate through the configured online entity resolution fallback and applies the local validation.",
			Kind:        action.KindRead, Modes: localOnly, Params: fallbackParams{}, Run: runFallbackTest,
		},
		{
			Name: "entities.league_evaluate", Group: group, Title: "Evaluate league gazetteer",
			Description: "Read-only quality report of the league gazetteer on the pending proposal backlog.",
			Kind:        action.KindRead, Modes: bothModes, Params: catalogParams{}, Run: runLeagueEvaluate,
		},
		{
			Name: "entities.roster_sync", Group: group, Title: "Sync league roster",
			Description: "Downloads the current nflverse roster CSV (atomic, validated; the previous file is kept on failure).",
			Kind:        action.KindWrite, Modes: localOnly, Params: rosterParams{}, Run: runRosterSync,
		},
		{
			Name: "entities.wikidata_evaluate", Group: group, Title: "Evaluate Wikidata verification",
			Description: "Benchmarks Wikidata verification on stored proposals; never changes proposals, never calls the LLM fallback.",
			Kind:        action.KindRead, Modes: localOnly, LongRunning: true,
			Params: wikidataParams{}, Run: runWikidataEvaluate,
		},
	}
}

func repo(env action.Env) *sqliteentityproposal.EntityProposalRepository {
	return sqliteentityproposal.NewEntityProposalRepository(env.DB)
}

// loggers sends every wiring component to the job log, tagged with its name.
func loggers(env action.Env) entitywiring.Loggers {
	return func(c string) appout.Logger { return action.PortLogger(env.Log.With("component", c)) }
}

func invalid(err error) error {
	return &action.Error{Code: action.CodeInvalidParams, Message: err.Error(), Err: err}
}

func or(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func orDash(s string) string { return or(s, "-") }

// pathParam is a file path param of a both-mode action and the configured
// value it overrides.
type pathParam struct{ name, value, configured string }

// refusePathOverrides rejects, in production, any path param that differs
// from its configured value (like stats.report's log_path): any
// authenticated caller could otherwise make the admin read arbitrary files.
func refusePathOverrides(env action.Env, params ...pathParam) error {
	if env.Mode != action.ModeProduction {
		return nil
	}
	for _, p := range params {
		if p.value != "" && p.value != p.configured {
			return &action.Error{Code: action.CodeInvalidParams, Message: p.name + " cannot be changed in production"}
		}
	}
	return nil
}

func runList(ctx context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*listParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	status := p.Status
	if status == "all" {
		status = ""
	}
	proposals, err := repo(env).List(ctx, p.Type, status)
	if err != nil {
		return action.Result{}, err
	}
	sort.SliceStable(proposals, func(i, j int) bool {
		return entityproposal.Score(proposals[i]) > entityproposal.Score(proposals[j])
	})
	rows := make([]map[string]any, 0, len(proposals))
	for _, pr := range proposals {
		rows = append(rows, map[string]any{
			"id": pr.ID, "type": string(pr.EntityType), "kind": string(pr.Kind), "name": pr.CanonicalName,
			"occurrences": pr.Occurrences, "confidence": fmt.Sprintf("%.2f", pr.Confidence), "status": string(pr.Status),
		})
	}
	return action.Result{
		Render: "table",
		Columns: []action.Column{
			{Key: "id", Label: "ID"}, {Key: "type", Label: "Type"}, {Key: "kind", Label: "Kind"},
			{Key: "name", Label: "Name"}, {Key: "occurrences", Label: "Occurrences"},
			{Key: "confidence", Label: "Confidence"}, {Key: "status", Label: "Status"},
		},
		Rows:    rows,
		Summary: fmt.Sprintf("%d proposal(s)", len(rows)),
	}, nil
}

type webSourceJSON struct {
	Domain string `json:"domain"`
	URL    string `json:"url"`
	Title  string `json:"title"`
}

type proposalJSON struct {
	ID             string            `json:"id"`
	Name           string            `json:"name"`
	Type           string            `json:"type"`
	Kind           string            `json:"kind"`
	TargetEntityID string            `json:"target_entity_id"`
	Aliases        []string          `json:"aliases"`
	Metadata       map[string]string `json:"metadata"`
	WebSources     []webSourceJSON   `json:"web_sources"`
	Occurrences    int               `json:"occurrences"`
	Confidence     float64           `json:"confidence"`
	Status         string            `json:"status"`
	FirstSeen      string            `json:"first_seen"`
	LastSeen       string            `json:"last_seen"`
	DecisionReason string            `json:"decision_reason,omitempty"`
	VerifiedBy     string            `json:"verified_by,omitempty"`
	ExampleNews    []string          `json:"example_news"`
}

func runShow(ctx context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*idParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	pr, err := repo(env).Get(ctx, p.ID)
	if err != nil {
		return action.Result{}, proposalError(err)
	}
	obj := proposalJSON{
		ID: pr.ID, Name: pr.CanonicalName, Type: string(pr.EntityType), Kind: string(pr.Kind),
		TargetEntityID: orDash(pr.TargetEntityID), Aliases: append([]string{}, pr.Aliases...), Metadata: map[string]string{},
		WebSources: []webSourceJSON{}, Occurrences: pr.Occurrences, Confidence: pr.Confidence, Status: string(pr.Status),
		FirstSeen: pr.FirstSeen.Format("2006-01-02"), LastSeen: pr.LastSeen.Format("2006-01-02"),
		DecisionReason: pr.DecisionReason, VerifiedBy: pr.Verification.Provider, ExampleNews: []string{},
	}
	for k, v := range pr.Metadata {
		obj.Metadata[k] = v
	}
	for _, w := range pr.WebSources {
		obj.WebSources = append(obj.WebSources, webSourceJSON{w.Domain, w.URL, w.Title})
	}
	for i, s := range pr.Sources {
		if i >= 5 {
			break
		}
		obj.ExampleNews = append(obj.ExampleNews, s.ArticleTitle)
	}
	return action.Result{Render: "object", Object: obj,
		Summary: fmt.Sprintf("%s (%s, %s): %s", pr.CanonicalName, pr.EntityType, pr.Kind, pr.Status)}, nil
}

// proposalError maps the review and repository errors of one proposal.
func proposalError(err error) error {
	switch {
	case errors.Is(err, entityproposal.ErrNotFound):
		return action.NotFound(err)
	case errors.Is(err, entitydiscovery.ErrNotPending):
		return action.Conflict(err)
	case errors.Is(err, entitydiscovery.ErrConflictingResolution):
		return conflictingResolution()
	}
	return err
}

func conflictingResolution() error {
	return &action.Error{Code: action.CodeInvalidParams, Message: "pass only one of merge_into or as_new"}
}

func runApprove(ctx context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*approveParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	if p.MergeInto != "" && p.AsNew {
		return action.Result{}, conflictingResolution()
	}
	cat := env.Config.Clustering.EntityCatalog
	paths := entitydiscovery.CatalogPaths{
		DictionaryPath:       or(p.Dictionary, env.Config.Dictionary.Path),
		ManualCatalogPath:    or(p.Manual, cat.ManualPath),
		GeneratedCatalogPath: or(p.Generated, cat.GeneratedPath),
	}
	resolved, err := entitydiscovery.Approve(ctx, repo(env), p.ID,
		entitydiscovery.Resolution{MergeInto: p.MergeInto, AsNew: p.AsNew}, paths, env.Now().UTC())
	if errors.Is(err, entitydiscovery.ErrNeedsResolution) {
		return action.Result{}, action.Conflict(fmt.Errorf(
			"proposal %s is a possible duplicate: pass merge_into=<entity-id> or as_new=true", p.ID))
	}
	if err != nil {
		return action.Result{}, proposalError(err)
	}
	env.Log.Info("entity proposal approved", "id", resolved.ID, "name", resolved.CanonicalName, "by", env.Actor)
	text := fmt.Sprintf("approved %s (%s)", resolved.CanonicalName, resolved.ID)
	if resolved.EntityType != entityproposal.EntityTypeTeam {
		text += "; run entities.generate to regenerate the deterministic entity catalog"
	}
	return action.Result{Render: "text", Text: text}, nil
}

func runReject(ctx context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*idParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	pr, err := entitydiscovery.Reject(ctx, repo(env), p.ID, env.Now().UTC())
	if err != nil {
		return action.Result{}, proposalError(err)
	}
	env.Log.Info("entity proposal rejected", "id", pr.ID, "name", pr.CanonicalName, "by", env.Actor)
	return action.Result{Render: "text", Text: fmt.Sprintf("rejected %s (%s)", pr.CanonicalName, pr.ID)}, nil
}

func runGenerate(_ context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*generateParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	outPath := or(p.Out, env.Config.Clustering.EntityCatalog.GeneratedPath)
	n, err := entitywiring.GenerateCatalog(or(p.Dictionary, env.Config.Dictionary.Path), outPath)
	if err != nil {
		return action.Result{}, err
	}
	return action.Result{Render: "text", Text: fmt.Sprintf("wrote %d entities to %s", n, outPath)}, nil
}

func runExtract(_ context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*extractParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	if strings.TrimSpace(p.Title+p.Description+p.Body) == "" {
		return action.Result{}, &action.Error{Code: action.CodeInvalidParams,
			Message: "give at least one of title, description or body", Err: entitywiring.ErrNoText}
	}
	cl := env.Config.Clustering
	if err := refusePathOverrides(env,
		pathParam{"rules", p.Rules, cl.RulesPath},
		pathParam{"manual", p.Manual, cl.EntityCatalog.ManualPath},
		pathParam{"generated", p.Generated, cl.EntityCatalog.GeneratedPath},
		pathParam{"generated_rules", p.GeneratedRules, cl.GeneratedRulesPath},
		pathParam{"roster", p.Roster, cl.EntityCatalog.League.RosterPath},
		pathParam{"league_rules", p.LeagueRules, cl.EntityCatalog.League.RulesPath},
	); err != nil {
		return action.Result{}, err
	}
	in := entitywiring.ExtractInput{
		Title: p.Title, Description: p.Description, Body: p.Body,
		RulesPath: p.Rules, ManualPath: p.Manual, GeneratedPath: p.Generated, GeneratedRulesPath: p.GeneratedRules,
		League: p.League, RosterPath: p.Roster, LeagueRulesPath: p.LeagueRules,
	}
	// Extract hands its logger to the league provider unguarded: never nil.
	rep, warnings, err := entitywiring.Extract(env.Config, in, env.Now(), action.PortLogger(env.Log.With("component", "extract")))
	if errors.Is(err, entitywiring.ErrNoText) {
		return action.Result{}, invalid(err)
	}
	if err != nil {
		return action.Result{}, err
	}
	for _, w := range warnings {
		env.Log.Warn("generated event rules", "warning", w)
	}
	return action.Result{Render: "object", Object: rep,
		Summary: fmt.Sprintf("primary=%s team=%s", or(rep.PrimaryEntity, "(none)"), or(rep.PrimaryTeam, "(none)"))}, nil
}

type fallbackJSON struct {
	Provider        string  `json:"provider"`
	Error           string  `json:"error,omitempty"`
	Resolved        bool    `json:"resolved"`
	Type            string  `json:"type"`
	CanonicalName   string  `json:"canonical_name"`
	Confidence      float64 `json:"confidence"`
	ValidationError string  `json:"validation_error,omitempty"`
}

func runFallbackTest(ctx context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*fallbackParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	fb := env.Config.EntityExtraction.Fallback
	if !fb.Enabled {
		return action.Result{}, action.Conflict(entitywiring.ErrFallbackDisabled)
	}
	ctx, cancel := context.WithTimeout(ctx, fb.Timeout())
	defer cancel()
	report := entitywiring.FallbackTest(ctx, entitywiring.NewFallbackResolver(fb), fb, p.Candidate, p.Context, p.Field)
	obj := fallbackJSON{Provider: report.Provider}
	var summary string
	switch {
	case report.Err != nil:
		obj.Error = report.Err.Error()
		summary = "error: " + obj.Error
	case report.ValidateErr != nil:
		obj.ValidationError = report.ValidateErr.Error()
		summary = "rejected: " + obj.ValidationError
	case !report.Result.Resolved:
		summary = "rejected: not resolved by the provider"
	default:
		summary = "accepted"
	}
	if report.Err == nil {
		r := report.Result
		obj.Resolved, obj.Type, obj.CanonicalName, obj.Confidence = r.Resolved, r.Type, r.CanonicalName, r.Confidence
	}
	return action.Result{Render: "object", Object: obj, Summary: summary}, nil
}

func runLeagueEvaluate(ctx context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*catalogParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	cat := env.Config.Clustering.EntityCatalog
	if err := refusePathOverrides(env,
		pathParam{"manual", p.Manual, cat.ManualPath},
		pathParam{"generated", p.Generated, cat.GeneratedPath},
	); err != nil {
		return action.Result{}, err
	}
	rep, err := entitywiring.EvaluateLeague(ctx, env.DB, env.Config, or(p.Manual, cat.ManualPath), or(p.Generated, cat.GeneratedPath), env.Now(), loggers(env))
	if errors.Is(err, entitywiring.ErrLeagueDisabled) {
		return action.Result{}, action.Conflict(err)
	}
	if err != nil {
		return action.Result{}, err
	}
	return action.Result{Render: "text", Text: rep.Text()}, nil
}

func runRosterSync(ctx context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*rosterParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	rc := env.Config.EntityDiscovery.Roster
	if p.Path == "" && rc.Path == "" {
		return action.Result{}, action.Conflict(errors.New("entity_discovery.roster.path is empty (or pass path)"))
	}
	count, dest, err := entitywiring.SyncRoster(ctx, rc, p.URL, p.Path, env.Now())
	if err != nil {
		return action.Result{}, err
	}
	return action.Result{Render: "text", Text: fmt.Sprintf("roster: %d players written to %s", count, dest)}, nil
}

func runWikidataEvaluate(ctx context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*wikidataParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	cat := env.Config.Clustering.EntityCatalog
	f := entitywiring.WikidataEvalFlags{
		Limit: p.Limit, Sample: p.Sample, Seed: p.Seed, Status: p.Status, Kind: p.Kind, EntityType: p.Type,
		ReviewedPath: p.Reviewed, ManualPath: cat.ManualPath, GeneratedPath: cat.GeneratedPath,
	}
	if err := entitywiring.ValidateWikidataEvalFlags(f); err != nil {
		return action.Result{}, invalid(err)
	}
	rep, err := entitywiring.EvaluateWikidata(ctx, env.DB, env.Config, f, loggers(env))
	if err != nil {
		return action.Result{}, err
	}
	t := rep.Totals
	return action.Result{Render: "object", Object: rep, Summary: fmt.Sprintf(
		"evaluated=%d verified_nfl=%d verified_non_nfl=%d ambiguous=%d not_found=%d errors=%d",
		t.Evaluated, t.VerifiedNFL, t.VerifiedNonNFL, t.Ambiguous, t.NotFound, t.Errors)}, nil
}
