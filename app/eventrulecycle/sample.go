// app/eventrulecycle/sample.go

// Package eventrulecycle orchestrates event rule discovery.
package eventrulecycle

import (
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventruleproposal"
)

// GenericLister lists recent articles classified as generic, newest first.
type GenericLister interface {
	ListGeneric(ctx context.Context, since time.Time, limit int) ([]eventruleproposal.Sample, error)
}

// SampleOptions bounds the evaluation sample.
type SampleOptions struct {
	Since      time.Time
	Size       int
	PerCluster int // default 1
	PerDomain  int // default Size/4, minimum 1
}

// Draw returns a deterministic, diversity-capped sample of generic articles.
func Draw(ctx context.Context, l GenericLister, opt SampleOptions) ([]eventruleproposal.Sample, error) {
	if opt.Size <= 0 {
		return []eventruleproposal.Sample{}, nil
	}
	if opt.PerCluster <= 0 {
		opt.PerCluster = 1
	}
	if opt.PerDomain <= 0 {
		opt.PerDomain = max(opt.Size/4, 1)
	}
	all, err := l.ListGeneric(ctx, opt.Since, opt.Size*5)
	if err != nil {
		return nil, err
	}
	out := make([]eventruleproposal.Sample, 0, min(opt.Size, len(all)))
	perCluster := map[string]int{}
	perDomain := map[string]int{}
	for _, s := range all {
		if len(out) >= opt.Size {
			break
		}
		d := sampleDomain(s)
		if s.ClusterID != "" {
			if perCluster[s.ClusterID] >= opt.PerCluster {
				continue
			}
		}
		if perDomain[d] >= opt.PerDomain {
			continue
		}
		if s.ClusterID != "" {
			perCluster[s.ClusterID]++
		}
		perDomain[d]++
		out = append(out, s)
	}
	return out, nil
}

func sampleDomain(s eventruleproposal.Sample) string {
	if d := strings.TrimSpace(s.Article.Source.Domain); d != "" {
		return strings.ToLower(d)
	}
	for _, raw := range []string{s.Article.CanonicalURL, s.Article.OriginalURL} {
		if u, err := url.Parse(raw); err == nil && u.Hostname() != "" {
			return strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
		}
	}
	return ""
}
