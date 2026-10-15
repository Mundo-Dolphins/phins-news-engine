package shared

import (
	"errors"

	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// ErrNoScraper is returned by NewScraperFromConfig when no factory is registered.
var ErrNoScraper = errors.New("no scraper registered: this build ships none")

// ErrNoRemoteOps is returned by GetRemoteOps when no RemoteOps is registered.
var ErrNoRemoteOps = errors.New("no remote ops registered: this build ships none")

// ScraperFactory builds the out.Scraper for dictionary commands.
type ScraperFactory func(cfg *config.Config) (out.Scraper, error)

var scraperFactory ScraperFactory

// SetScraperFactory registers the scraper factory.
func SetScraperFactory(f ScraperFactory) { scraperFactory = f }

// NewScraperFromConfig delegates to the registered factory; ErrNoScraper if unset.
func NewScraperFromConfig(cfg *config.Config) (out.Scraper, error) {
	if scraperFactory == nil {
		return nil, ErrNoScraper
	}
	return scraperFactory(cfg)
}

// RemoteDeployer is the combined deploy/health/rollback client.
type RemoteDeployer interface {
	out.RemoteDeploy
	out.RemoteHealthCheck
	out.RemoteRollback
}

// RemoteOps bundles the script-backed clients the editorial cycle and deploy use.
type RemoteOps struct {
	NewSync   func(scriptPath, keyPath string) out.RemoteSync
	NewDeploy func(deployScript, healthScript, rollbackScript, keyPath string) RemoteDeployer
}

var remoteOps *RemoteOps

// SetRemoteOps registers the remote-ops constructors.
func SetRemoteOps(r RemoteOps) { remoteOps = &r }

// GetRemoteOps returns the registered RemoteOps, or ErrNoRemoteOps if unset.
func GetRemoteOps() (RemoteOps, error) {
	if remoteOps == nil {
		return RemoteOps{}, ErrNoRemoteOps
	}
	return *remoteOps, nil
}

// ClearRemoteOps unregisters the RemoteOps (for tests that need the unset state).
func ClearRemoteOps() { remoteOps = nil }
