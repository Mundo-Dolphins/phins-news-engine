package wikidata

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/knowledge"
)

// DefaultProperties is the built-in statement allow-list: instance of,
// sport, occupation, member of sports team, position played, head coach,
// league, country of citizenship. Everything else is dropped to bound
// payload and cache size.
var DefaultProperties = []string{"P31", "P641", "P106", "P54", "P413", "P286", "P118", "P27"}

// qualifierProperties are the only qualifiers kept: start time, end time,
// end cause, "of".
var qualifierProperties = []string{"P580", "P582", "P1534", "P642"}

const (
	propStartTime = "P580"
	propEndTime   = "P582"
	getBatchSize  = 50 // wbgetentities maximum ids per request
)

var qidPattern = regexp.MustCompile(`^Q[0-9]+$`)

type langValue struct {
	Value string `json:"value"`
}

type rawDatavalue struct {
	Type  string          `json:"type"`
	Value json.RawMessage `json:"value"`
}

type rawSnak struct {
	Snaktype  string        `json:"snaktype"`
	Property  string        `json:"property"`
	Datavalue *rawDatavalue `json:"datavalue"`
}

type rawClaim struct {
	Mainsnak   rawSnak              `json:"mainsnak"`
	Rank       string               `json:"rank"`
	Qualifiers map[string][]rawSnak `json:"qualifiers"`
}

type rawEntity struct {
	ID           string                 `json:"id"`
	Missing      *json.RawMessage       `json:"missing"`
	Labels       map[string]langValue   `json:"labels"`
	Descriptions map[string]langValue   `json:"descriptions"`
	Aliases      map[string][]langValue `json:"aliases"`
	Claims       map[string][]rawClaim  `json:"claims"`
}

type getResponse struct {
	Entities map[string]rawEntity `json:"entities"`
	Error    *apiError            `json:"error"`
}

// Get implements ports/out.KnowledgeSource. IDs are deduplicated, fetched
// in batches of 50, and returned in input order; unknown IDs are omitted.
func (c *Client) Get(ctx context.Context, ids []string) ([]knowledge.Entity, error) {
	seen := make(map[string]bool, len(ids))
	ordered := make([]string, 0, len(ids))
	for _, id := range ids {
		if !qidPattern.MatchString(id) {
			return nil, fmt.Errorf("wikidata: invalid entity id %q", id)
		}
		if !seen[id] {
			seen[id] = true
			ordered = append(ordered, id)
		}
	}
	if len(ordered) == 0 {
		return nil, nil
	}

	byID := make(map[string]knowledge.Entity, len(ordered))
	for start := 0; start < len(ordered); start += getBatchSize {
		end := min(start+getBatchSize, len(ordered))
		chunk := ordered[start:end]

		params := url.Values{}
		params.Set("action", "wbgetentities")
		params.Set("format", "json")
		params.Set("ids", strings.Join(chunk, "|"))
		params.Set("props", "labels|aliases|descriptions|claims")
		params.Set("languages", c.cfg.Language)

		body, err := c.do(ctx, params)
		if err != nil {
			return nil, err
		}
		var resp getResponse
		if err := json.Unmarshal(body, &resp); err != nil {
			return nil, fmt.Errorf("wikidata: decode entities response: %w", err)
		}
		if err := resp.Error.check(); err != nil {
			return nil, err
		}
		for _, id := range chunk {
			raw, ok := resp.Entities[id]
			if !ok || raw.Missing != nil {
				continue
			}
			byID[id] = c.toEntity(id, raw)
		}
	}

	out := make([]knowledge.Entity, 0, len(byID))
	for _, id := range ordered {
		if e, ok := byID[id]; ok {
			out = append(out, e)
		}
	}
	return out, nil
}

func (c *Client) toEntity(requestedID string, raw rawEntity) knowledge.Entity {
	lang := c.cfg.Language
	e := knowledge.Entity{
		ID:          requestedID,
		Label:       raw.Labels[lang].Value,
		Description: raw.Descriptions[lang].Value,
	}
	if raw.ID != "" && raw.ID != requestedID {
		e.CanonicalID = raw.ID
	}
	for _, a := range raw.Aliases[lang] {
		e.Aliases = append(e.Aliases, a.Value)
	}
	for _, prop := range c.properties {
		for _, claim := range raw.Claims[prop] {
			e.Statements = append(e.Statements, toStatement(prop, claim))
		}
	}
	return e
}

func toStatement(prop string, claim rawClaim) knowledge.Statement {
	st := knowledge.Statement{
		Property: prop,
		Value:    toValue(claim.Mainsnak),
		Rank:     toRank(claim.Rank),
	}
	for _, qp := range qualifierProperties {
		for _, snak := range claim.Qualifiers[qp] {
			v := toValue(snak)
			st.Qualifiers = append(st.Qualifiers, knowledge.Qualifier{Property: qp, Value: v})
			if v.Kind != knowledge.KindTime || v.Time == nil {
				continue
			}
			switch qp {
			case propStartTime:
				if st.ValidFrom == nil {
					st.ValidFrom = v.Time
				}
			case propEndTime:
				if st.ValidTo == nil {
					st.ValidTo = v.Time
				}
			}
		}
	}
	return st
}

func toRank(r string) knowledge.Rank {
	switch r {
	case "preferred":
		return knowledge.RankPreferred
	case "deprecated":
		return knowledge.RankDeprecated
	default:
		return knowledge.RankNormal
	}
}

func toValue(s rawSnak) knowledge.Value {
	switch s.Snaktype {
	case "novalue":
		return knowledge.Value{Kind: knowledge.KindNoValue}
	case "somevalue":
		return knowledge.Value{Kind: knowledge.KindSomeValue}
	}
	if s.Datavalue == nil {
		return knowledge.Value{Kind: knowledge.KindOther}
	}
	switch s.Datavalue.Type {
	case "wikibase-entityid":
		var v struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(s.Datavalue.Value, &v) == nil && v.ID != "" {
			return knowledge.Value{Kind: knowledge.KindEntity, EntityID: v.ID}
		}
	case "string":
		var text string
		if json.Unmarshal(s.Datavalue.Value, &text) == nil {
			return knowledge.Value{Kind: knowledge.KindString, Text: text}
		}
	case "time":
		var v struct {
			Time      string `json:"time"`
			Precision int    `json:"precision"`
		}
		if json.Unmarshal(s.Datavalue.Value, &v) == nil {
			if t, ok := parseWikidataTime(v.Time); ok {
				return knowledge.Value{Kind: knowledge.KindTime, Time: &t, Precision: v.Precision}
			}
		}
	case "quantity":
		var v struct {
			Amount string `json:"amount"`
		}
		if json.Unmarshal(s.Datavalue.Value, &v) == nil {
			return knowledge.Value{Kind: knowledge.KindQuantity, Text: strings.TrimPrefix(v.Amount, "+")}
		}
	}
	return knowledge.Value{Kind: knowledge.KindOther}
}

// parseWikidataTime parses "+2018-04-29T00:00:00Z". Coarse-precision
// values carry "-00" month/day, which become "-01" (the earliest instant
// of the period). BCE (leading "-") and out-of-range years are reported
// as not parseable rather than guessed.
func parseWikidataTime(s string) (time.Time, bool) {
	if !strings.HasPrefix(s, "+") {
		return time.Time{}, false
	}
	s = strings.TrimPrefix(s, "+")
	if len(s) < 10 {
		return time.Time{}, false
	}
	date, rest := s[:10], s[10:]
	parts := strings.Split(date, "-")
	if len(parts) != 3 {
		return time.Time{}, false
	}
	if parts[1] == "00" {
		parts[1] = "01"
	}
	if parts[2] == "00" {
		parts[2] = "01"
	}
	t, err := time.Parse("2006-01-02T15:04:05Z", strings.Join(parts, "-")+rest)
	if err != nil {
		return time.Time{}, false
	}
	return t.UTC(), true
}
