# phins-news-engine

[![CI](https://github.com/Mundo-Dolphins/phins-news-engine/actions/workflows/ci.yml/badge.svg)](https://github.com/Mundo-Dolphins/phins-news-engine/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

A Go library that runs a news pipeline for a single topic, such as one
sports team: it fetches items from feeds and APIs, removes duplicates,
groups items about the same event, scores relevance and editorial quality,
and publishes the survivors to a Telegram channel through a transactional
outbox. Everything is stored in one SQLite file.

The library is the engine only. A **host program** (your own `main`
package) chooses the sources, supplies the data files and credentials, and
can plug in extra fetchers, a scraper or a channel-history client. With
nothing registered, the engine runs on the built-in sources (RSS/Atom,
Reddit, Bluesky, YouTube feeds) and publishes with the Telegram Bot API.

> Status: `v0.x`. The API can change between minor versions; pin a version
> in your `go.mod`. See [Versioning](#versioning).

## Architecture

The code follows ports and adapters ([ADR 0001](docs/adr/0001-hexagonal-architecture.md)).
Dependencies point inward: `domain` imports nothing from this module,
`app` depends on `domain` and the port interfaces in `ports`, and adapters
implement those ports.

```text
              host program (your main package)
                 |                     |
            engine.Run             cli.Root()
                 |                     |
  +--------------v---------------------v---------------+
  |  adapters/inbound (scheduler)    cli/cmd/* (tools)  |
  |                 |                     |             |
  |        +--------v---------------------v------+      |
  |        |  ports/in  (ProcessCycle, ...)      |      |
  |        |  app/usecase, app/* (orchestration) |      |
  |        |  domain/*   (pure decision rules)   |      |
  |        |  ports/out  (Fetcher, Publisher,    |      |
  |        |   repositories, Scraper, ...)       |      |
  |        +--------^---------------------^------+      |
  |                 |                     |             |
  |  adapters/outbound: sqlite, rss,   host adapters:   |
  |  reddit, bluesky, youtube,         scrapers, extra  |
  |  telegram, espnscoreboard          fetchers, history|
  |  infra: config, netguard, llm...   client, ...      |
  +-----------------------------------------------------+
```

More detail: [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md),
[docs/PIPELINE.md](docs/PIPELINE.md).

## Package layout

| Directory | Contents |
|---|---|
| `domain` | Pure decision rules and value objects: article lifecycle, normalization, deduplication, similarity, relevance classification, event signals and clustering, editorial scoring, source policy, publishing policy. No I/O. |
| `ports` | Interfaces. `ports/in` holds the driving ports (process a cycle, publish the outbox, close clusters, run retention); `ports/out` holds the driven ports (repositories, `Fetcher`, `Publisher`, `Scraper`, `TelegramChannelImporter`, LLM and search clients, remote operations). |
| `app` | Use cases (`app/usecase`) and offline workflows: training, evaluation, dictionary maintenance, entity discovery, replay and backtests. |
| `adapters` | `adapters/inbound/scheduler` and the outbound adapters: SQLite repositories, the RSS, Reddit, Bluesky and YouTube fetchers, the composite fetcher, the Telegram publisher and the ESPN scoreboard client. |
| `infra` | Configuration loading and validation, the SQLite connection and migrator, logging, clock, file loaders, the LLM, web-search and Wikidata clients, the network guard (`infra/netguard`) and the fetcher wiring (`infra/fetcherwiring`). |
| `engine` | `engine.Run`: wires every adapter and runs the scheduler service. |
| `cli` | The operator command tree (`cli.Root()`), built with Cobra. |
| `migrations` | Embedded SQL migrations. |
| `configs/example` | A complete, fictional example configuration. |

## Using the library from a host program

```bash
go get github.com/Mundo-Dolphins/phins-news-engine@latest
```

A minimal host registers nothing and runs the service. Build it twice (or
behind a sub-command) if you also want the operator tools:

```go
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"github.com/Mundo-Dolphins/phins-news-engine/engine"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/logging"
)

func main() {
	profile, err := config.ResolveProfile("") // NEWS_ENGINE_PROFILE, default "local"
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	logger := logging.New("info", false)
	opts := engine.Options{ServiceName: "my-news-service"}
	if err := engine.Run(ctx, "configs/example/settings.yml", profile, logger, opts); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
```

Run it from the repository root of your host, with the secrets in the
environment (`TELEGRAM_BOT_TOKEN`, `WEB_SEARCH_API_KEY` when web lookup is
enabled). Every relative path in `configs/example/settings.yml` resolves
against the working directory. `engine.Run` applies the migrations, checks
every configured file and blocks until the context is cancelled.

For the operator tools, call `cli.Execute()` (or `cli.Root()` to embed the
tree under your own root command). `tools/smoke/consumer.sh` builds both
from a throwaway module; CI runs it on every change.

### Extension points

| Extension point | What the host supplies |
|---|---|
| `fetcherwiring.Extensions` (`engine.Options.Fetchers`) | `Categories`: a `fetcherwiring.CategoryBuilder` for a fetch category the library does not implement (`website`, `twitter`, `instagram`). An enabled category with no builder is reported as such in the capability log, never skipped silently. `Scraper`: builds the `out.Scraper` used as the RSS fallback. |
| `engine.Options.ServiceName` | The outbox claim owner and the name in the start and stop log lines. |
| `engine.Options.ChannelImporter` | Builds an optional `out.TelegramChannelImporter`, a history client provided by the host, used to reconcile sends whose outcome is unknown ([ADR 0021](docs/adr/0021-channel-history-client-port.md)). |
| `cli.Root`, `cli.Add`, `cli.Execute` | The tools command tree; `cli.Add` registers host commands. |
| `cli.SetProgramName`, `cli.SetServiceBinaryName`, `cli.SetDefaultDictionaryPath` | Names and defaults used by the tools. |
| `shared.SetScraperFactory`, `shared.SetRemoteOps`, `shared.SetTelegramImportClient`, `shared.SetFetcherExtensions` (package `cli/cmd/shared`) | Factories that the tools commands call. Each one has a clear "nothing registered" error. |
| `out.Scraper` | Fetches the rendered HTML of a URL ([ADR 0011](docs/adr/0011-scraper-port.md)). The library ships no implementation. |
| `out.TelegramChannelImporter` | Reads the target channel's recent history. No implementation ships. |
| `out.RemoteSync`, `out.RemoteDeploy`, `out.RemoteHealthCheck`, `out.RemoteRollback` | Remote operations used by the editorial cycle and deployment workflows. No implementation ships. |
| `regressiondata.SetCases` / `SetCasesJSON` | The dictionary regression dataset (the default is 16 synthetic cases). |
| `llmclient.SetPrompts` | Prompts for entity discovery, resolution, verification and event rules. |
| `editorialteacher.SetPublicationPolicy` | The policy text and version the LLM teacher labels against. |
| `eventruleproposal.TeamStopwords`, `classifierdictionary.StopValues`, `entitydiscovery.AmbiguousTokens`, `config.ProfileEnvVar` | Topic-specific word lists and the profile variable name. |

The full list, with each default and what happens when a host forgets a
setting, is in [docs/EXTRACTION-NOTES.md](docs/EXTRACTION-NOTES.md#host-settings).

## What is not included, and why

- **Scrapers.** No code downloads or parses third-party web pages, and there
  are no headless browsers. Sites without a feed or API need a host adapter
  behind `out.Scraper` or a `CategoryBuilder`; whoever runs it answers for
  its use.
- **Data.** No real sources, dictionaries, entity catalogs, rosters,
  articles, labels or trained models. The fixtures and
  `configs/example` are fictional (the "Springfield Isotopes" and an
  invented league, `example.com` hosts).
- **Models.** The editorial classifier needs a model trained on your own
  labels (`phins-news-engine-tools train run`). The example configuration
  runs it in `shadow` mode, so you can try the pipeline without one by
  setting `editorial.mode` to `disabled`.
- **Credentials.** Tokens and API keys come from environment variables at
  run time and are never stored in YAML ([docs/CONFIG-REFERENCE.md](docs/CONFIG-REFERENCE.md#secrets-and-environment-variables)).

## Legal notes

The code is MIT licensed ([LICENSE](LICENSE)). [NOTICE](NOTICE) covers
third-party data and services: the nflverse roster data used by the roster
sync is CC BY 4.0 and needs attribution when you redistribute it; the ESPN
scoreboard endpoint used for match state is undocumented and unofficial,
may change or be restricted, is disabled by default, and is used at your
own risk under the provider's terms. Every other service is reached through
its public or official API and is subject to its own terms. The project is
not affiliated with any league, team or service it mentions.

## Security

Report vulnerabilities privately through GitHub security advisories; see
[SECURITY.md](SECURITY.md). Every outbound HTTP client has a timeout, a
body cap, a redirect policy, an SSRF guard checked again at dial time, and
credential redaction in errors. Files the library writes are `0600`.
Details and caveats (for example, proxies) are in
[docs/SECURITY.md](docs/SECURITY.md).

## Development

```bash
make build
make test
make cover
make lint
make docs-check
make validate-config
make smoke
make ci
```

`make docs-check` validates this README and everything under `docs/`
against the code: links, referenced paths, documented commands and flags,
and YAML snippets.

## Versioning

The module follows semantic versioning. While the major version is `0`,
a minor release may break the API; patch releases do not. Every commit
merged to `main` is released automatically as a patch version once CI passes
on it (`.github/workflows/release.yml`). Put `[skip release]` in the commit
message (with squash merges: the PR title or description) to opt out; a
skipped commit ships with the next release. Minor and major versions are
tagged by hand.

## Documentation

| Document | Topic |
|---|---|
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | Layers, packages, dependency rule, composition |
| [docs/PIPELINE.md](docs/PIPELINE.md) | The online pipeline, article states and reason codes |
| [docs/DEDUPLICATION.md](docs/DEDUPLICATION.md) | The layers that stop repeats and their guarantees |
| [docs/CLASSIFIER.md](docs/CLASSIFIER.md) | Relevance gate, vetoes and the editorial classifier |
| [docs/DATABASE.md](docs/DATABASE.md) | SQLite settings, schema map, migrations, retention |
| [docs/CONFIG-REFERENCE.md](docs/CONFIG-REFERENCE.md) | Every configuration section and environment variable |
| [docs/SECURITY.md](docs/SECURITY.md) | Network, file and command hardening |
| [docs/adr/README.md](docs/adr/README.md) | Architecture decision records |
| [docs/EXTRACTION-NOTES.md](docs/EXTRACTION-NOTES.md) | How the library was extracted; host settings |
