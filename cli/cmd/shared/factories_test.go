package shared_test

import (
	"errors"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

func TestNewScraperFromConfig_DefaultAndRegistered(t *testing.T) {
	shared.SetScraperFactory(nil)
	if _, err := shared.NewScraperFromConfig(&config.Config{}); !errors.Is(err, shared.ErrNoScraper) {
		t.Fatalf("err = %v, want ErrNoScraper", err)
	}
	want := errors.New("stub")
	shared.SetScraperFactory(func(*config.Config) (out.Scraper, error) { return nil, want })
	defer shared.SetScraperFactory(nil)
	if _, err := shared.NewScraperFromConfig(&config.Config{}); !errors.Is(err, want) {
		t.Fatalf("err = %v, want stub error", err)
	}
}

func TestRemoteOps_UnsetAndSet(t *testing.T) {
	shared.ClearRemoteOps()
	if _, err := shared.GetRemoteOps(); !errors.Is(err, shared.ErrNoRemoteOps) {
		t.Fatalf("err = %v, want ErrNoRemoteOps", err)
	}
	called := false
	shared.SetRemoteOps(shared.RemoteOps{NewSync: func(string, string) out.RemoteSync { called = true; return nil }})
	defer shared.ClearRemoteOps()
	r, err := shared.GetRemoteOps()
	if err != nil {
		t.Fatal(err)
	}
	r.NewSync("", "")
	if !called {
		t.Fatal("registered NewSync not returned")
	}
}
