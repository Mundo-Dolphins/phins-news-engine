// app/eventrulecycle/sample_test.go
package eventrulecycle_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/eventrulecycle"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventruleproposal"
)

type fakeLister struct {
	samples []eventruleproposal.Sample
	limit   int
}

func (f *fakeLister) ListGeneric(_ context.Context, _ time.Time, limit int) ([]eventruleproposal.Sample, error) {
	f.limit = limit
	return f.samples, nil
}

func sample(id, cluster, host string) eventruleproposal.Sample {
	return eventruleproposal.Sample{
		Article:   article.Article{ID: id, CanonicalURL: "https://" + host + "/" + id},
		ClusterID: cluster,
	}
}

func ids(ss []eventruleproposal.Sample) []string {
	out := []string{}
	for _, s := range ss {
		out = append(out, s.Article.ID)
	}
	return out
}

func TestDraw_CapsPerCluster(t *testing.T) {
	l := &fakeLister{samples: []eventruleproposal.Sample{
		sample("a1", "c1", "a.com"), sample("a2", "c1", "b.com"), sample("a3", "c2", "c.com"),
	}}
	got, err := eventrulecycle.Draw(context.Background(), l, eventrulecycle.SampleOptions{Size: 10, PerDomain: 10})
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(ids(got)) != "[a1 a3]" {
		t.Errorf("got %v", ids(got))
	}
	if l.limit != 50 {
		t.Errorf("over-fetch limit = %d, want 50", l.limit)
	}
}

func TestDraw_CapsPerDomainAndSize(t *testing.T) {
	l := &fakeLister{samples: []eventruleproposal.Sample{
		sample("a1", "c1", "a.com"), sample("a2", "c2", "a.com"), sample("a3", "c3", "a.com"),
		sample("a4", "c4", "b.com"), sample("a5", "c5", "c.com"),
	}}
	got, _ := eventrulecycle.Draw(context.Background(), l, eventrulecycle.SampleOptions{Size: 3, PerDomain: 2})
	if fmt.Sprint(ids(got)) != "[a1 a2 a4]" {
		t.Errorf("got %v", ids(got))
	}
}

func TestDraw_EmptyClusterIsDistinct(t *testing.T) {
	l := &fakeLister{samples: []eventruleproposal.Sample{
		sample("a1", "", "a.com"), sample("a2", "", "b.com"),
	}}
	got, _ := eventrulecycle.Draw(context.Background(), l, eventrulecycle.SampleOptions{Size: 4, PerDomain: 5})
	if len(got) != 2 {
		t.Errorf("got %v", ids(got))
	}
}

func TestDraw_DefaultPerDomain(t *testing.T) {
	var ss []eventruleproposal.Sample
	for i := 0; i < 10; i++ {
		ss = append(ss, sample(fmt.Sprint("a", i), fmt.Sprint("c", i), "a.com"))
	}
	got, _ := eventrulecycle.Draw(context.Background(), &fakeLister{samples: ss}, eventrulecycle.SampleOptions{Size: 8})
	if len(got) != 2 { // 8/4
		t.Errorf("got %v", ids(got))
	}
}

func TestDraw_Empty(t *testing.T) {
	got, err := eventrulecycle.Draw(context.Background(), &fakeLister{}, eventrulecycle.SampleOptions{Size: 5})
	if err != nil || got == nil || len(got) != 0 {
		t.Errorf("got %v, %v", got, err)
	}
}

type panicLister struct{}

func (panicLister) ListGeneric(context.Context, time.Time, int) ([]eventruleproposal.Sample, error) {
	panic("lister must not be called for a non-positive size")
}

func TestDraw_NonPositiveSizeSkipsLister(t *testing.T) {
	for _, size := range []int{0, -3} {
		got, err := eventrulecycle.Draw(context.Background(), panicLister{}, eventrulecycle.SampleOptions{Size: size})
		if err != nil || got == nil || len(got) != 0 {
			t.Errorf("size %d: got %v, %v", size, got, err)
		}
	}
}
