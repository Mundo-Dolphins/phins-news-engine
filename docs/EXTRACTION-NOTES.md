# Extraction notes

This library was extracted from a private news bot. The private repository
keeps its real configuration, dictionaries, entity catalogs, trained models
and datasets. None of them are in this tree. This file records how the test
suite was made to run without that data, and which tests did not come across.

## Synthetic data that replaces the private files

Every fixture below is invented. It uses a fictional league ("Springfield
Isotopes", "Shelbyville Sharks" and four more teams), fictional people, and
example.com, example.org and example.net hosts.

| Path | Replaces | Read by |
|---|---|---|
| `configs/example/settings.yml` | the private settings file | config tests, `cli/cmd/shared`, `cli/cmd/train` |
| `configs/example/sources.yml` | the private source catalog | `configs/example` |
| `configs/example/source-policies.yml` | the private source policies | `configs/example` |
| `configs/example/example_dictionary.yml` | the private classification dictionary | dictionaryfile, classification benchmark |
| `configs/example/event_clustering_rules.yml` | the private clustering rules | clusteringrules, pipelinereplay, CLI tests |
| `configs/example/entity_catalog_manual.yml` | the private hand-written entity catalog | entitycatalog, entitydiscovery, CLI tests |
| `configs/example/entity_catalog_generated.yml` | the private generated entity catalog | clusteringrules, CLI tests |
| `configs/example/league_alias_rules.yml` | the private 32-team league rules | leaguerules, leaguecatalog, entitydiscovery, CLI tests |
| `configs/example/dictionary_mining.yml` | the private mining policy | dictionaryminingfile |
| `configs/example/editorial_vetoes.yml` | the private veto rules | `configs/example` |
| `testdata/clustering_groups.yml` | the private clustering evaluation dataset | clustereval, events and editorial CLI tests |
| `testdata/dedup_pairs.yml` | the private dedup evaluation pairs | dedupeval |
| `infra/dictionaryfile/testdata/` | package fixtures excluded at extraction | dictionaryfile |
| `infra/clusteringrules/testdata/` | package fixtures excluded at extraction | clusteringrules |
| `infra/entitycatalog/testdata/` | package fixture excluded at extraction | entitycatalog |
| `infra/nflverse/testdata/` | package fixture excluded at extraction | nflverse |
| `app/entitydiscovery/testdata/` | Wikidata golden fixture | entitydiscovery |
| `cli/cmd/entities/testdata/` | mini dictionary | entities CLI |

`configs/example/example_test.go` loads every example file through the
loaders the engine uses at start-up. `make validate-config` runs it.

## Fixture paths

Flattening the package tree removed one directory level. Several tests built
paths with a fixed number of ".." segments, and those paths then pointed
outside the repository. Tests now resolve fixtures with
`testsupport/repofs`, which anchors a path at the module root and fails the
test if the path leaves it. `TestNoTestFixturePathEscapesTheModuleRoot` in
that package scans every Go file and fails when:

- a filepath.Join or path.Join climbs above the module root (every ".."
  argument counts, also after a variable base such as the working
  directory);
- a literal starting with "../" or "./../" resolves outside the root;
- a test names an absolute path into a user or system tree ("/Users/...",
  "/home/...", "/tmp/...");
- a test reads the home directory (os.UserHomeDir or the HOME variable).

Three test literals under /tmp are allow-listed in the guard, each with a
reason: they are compared as strings and never opened. The guard's
analyzer has its own tests for each pattern and for benign paths.

## Tests not carried over

| Package | Test | Reason |
|---|---|---|
| domain/classification | TestSeedDictionary_RepresentativeHeadlines | Checks the private production dictionary against real headlines. It tests that data, not the classifier. |
| domain/classification | TestSeedDictionary_MetadataAndCategoryCount | Checks the category count (19) of the private dictionary. |
| app/modeltraining | TestTrain_ReproducesCommittedModelChecksum | Retrains the private committed model from the private seed dataset and compares checksums. It was already skipped upstream. Trainer determinism is still covered by the `*_Deterministic` tests in app/editorialtrain. |
| infra/leaguerules | TestRepoRulesFile_BuildsAnIndexWithAll32Teams | Checks the real 32-team rules file. It was replaced by TestExampleRulesFile_BuildsAWorkingIndex (see below). |
| cli/cmd/dictionary | TestMain (main_test.go) | Registered the private scraper factory. The dictionary tests pass without it. |
| cli/cmd/entities | TestProductionBinaryHasNoKnowledgeSourceDependency | Guarded the private service binary. Its check moved into the engine dependency guard (see below). |

## Assertions that changed

- **infra/leaguerules.** The replacement test runs on the six-team example
  league. It still checks aliases, upper-case abbreviations, singular
  nicknames, compound and truncated forms, undecided terms and the closed-set
  rejection. It drops these assertions about real data: the 32-team count,
  the real team aliases and abbreviations, the forms seen in the real
  backlog, and the conference and division names. Restored: the
  abbreviation-collision check that an abbreviation in any case, including
  a two-letter one, is never rejected ("NH", "Nh", "nh", "SPR", "spr").
- **infra/clusteringrules** (TestLoadWithEntityCatalog_ExampleFiles). The
  minimum entity count went from 150 (the real catalog) to 10. The example
  catalog has 12 entities. The surname-alias regression now resolves a
  fictional player.
- **infra/dictionaryminingfile.** The policy test now runs on the example
  policy with fictional entity names. It drops three common phrases that only
  the private policy listed.
- **infra/config.** The live-lane test uses example source ids and
  fictional teams. The schema drift test now checks that the example settings
  file covers every config field.
- **cli/cmd/shared.** The relevance-filter cases use example sources and
  fictional titles. The three outcomes (rejected, kept, trusted-source
  bypass) are unchanged.
- **engine.** The dependency guard uses the flattened package paths. It also
  forbids the knowledge-source packages (Wikidata and its cache), which is
  the check from the dropped importguard test.
- **Benchmarks.** BenchmarkDecide uses the example dictionary.
  BenchmarkEvaluate uses an in-memory synthetic model, because no trained
  model ships with the library.

## docscheck

docscheck lives in `docscheck` and validates the public tree only. It
checks README.md and the Markdown under docs/. The tools command tree is
`cli.Root()` with no private commands registered. The checks for the service
binary and its flags were removed, because the library has no service binary:
a host program calls engine.Run. TestConfigReferenceCoversSettings came back
with `docs/CONFIG-REFERENCE.md`; it checks the example settings file.

## Coverage

`go test ./... -coverprofile` reported a total of 82.4% of statements when
these fixtures landed. `make cover` enforces max(82.4 - 1, 70) = 81.4%.

## Sanitisation

Every real team, player, coach, executive, reporter, media site, Wikidata
id of a real entity, e-mail address and private host name was replaced by a
consistent fictional equivalent, in code comments, tests, fixtures and
goldens alike: the covered team is "Springfield Isotopes" (city
"Springfield", nickname "Isotopes", alias "Topes", abbreviation SPR), the
other teams map onto the fictional league above (plus a few more fictional
clubs), players keep generic first names and get fictional surnames, media
domains become `*.example.com` hosts, and real Wikidata ids of teams,
people and places become fake `Q9100000NN` ids. Generic Wikidata class ids
(human, American football player, positions, leagues) are kept: the code
needs them. Tests whose behaviour depended on a property of a real name
(word boundaries, a city shared by several teams, a nickname that is also an
English word, typo and truncation distances, apostrophes and hyphens in
names, generational suffixes) keep the property with the fictional names;
no assertion was dropped. Two were rewritten by hand:

- domain/dictionary: the word-boundary test now checks that "topes" does not
  match inside "isotopes" (it used a different word with the same shape).
- domain/eventruleproposal: the single-word team stopword case sets the new
  `TeamStopwords` for the test (the library default has none).

`domain/classification/regressiondata/seed_cases.json` is now a synthetic
16-case set with the same structure and expected outcomes, and the
editorial teacher policy (`app/editorialteacher/policy/publication-policy.md`)
is a team-neutral text.

The migration files are unchanged except for their leading `--` comments,
which named the old `internal/...` paths. The migrator records only the file
name in `schema_migrations` (no checksum of the SQL text), and SQLite does
not store comments outside a statement, so already-migrated databases and
fresh schemas are unaffected.

Placeholders that remain on purpose: the Telegram chat ids `-1000000000000`
and `-1001234567890` (obvious placeholders, also allow-listed by
docscheck), the generic words "NFL" and "nflverse" (data-source
attribution), ESPN's public scoreboard endpoint (`site.api.espn.com`, the
documented match-state source), and the org name in the module path.

## Host settings

Names and data that change behaviour are configurable, with team-neutral
library defaults. A host that wants its previous behaviour sets them at
start-up. Every row is REQUIRED-FOR-PRODUCTION-PARITY for a host that had
production values before the extraction. "Silent" means a forgotten setting
produces no error, only different behaviour.

| Setting | Library default | If the host forgets it |
|---|---|---|
| `engine.Options.ServiceName` (outbox claim owner, log name) | `phins-news-engine` | silent: new claim owner |
| `cli.SetProgramName` (root command, operator hints, deployed tools binary, dictionary `Tool` value) | `phins-news-engine-tools` | silent |
| `cli.SetServiceBinaryName` (deployed service binary under build/) | `phins-news-engine` | deploy fails: binary not found |
| `cli.SetDefaultDictionaryPath` (`--dictionary` flag default) | the `configs` dictionary file `dictionary.yml` | load error when the file is absent |
| `espn_scoreboard.user_agent` (settings.yml) | `phins-news-engine/1.0` | silent |
| `config.ProfileEnvVar` (profile environment variable) | `NEWS_ENGINE_PROFILE` | **silent**: the old variable is ignored and the profile falls back to local |
| `llmclient.SetPrompts` (entity discovery, resolution and verification, event-rule prompts) | team-neutral prompts | silent |
| `editorialteacher.SetPublicationPolicy` (teacher policy text and version) | generic policy, `generic-publication-policy-v1` | silent |
| `regressiondata.SetCases` / `SetCasesJSON` (dictionary regression dataset) | 16 synthetic cases | loud: `regressioncheck.Run` returns `ErrCasesDoNotFitDictionary` when no case expected to be related is related under the previous dictionary |
| `eventruleproposal.TeamStopwords` | none | silent |
| `classifierdictionary.StopValues` | `Super Bowl`, `NFL Draft` | silent |
| `entitydiscovery.AmbiguousTokens` | none | **silent**: bare city and shared-nickname aliases are no longer scrubbed |
| `weblookup.BuildTemplateQuery(kind, name, team)` | falls back to "NFL" when no team is given | n/a (callers pass the team) |
| `cli.SetRemoteSyncScriptPath` (remote database sync script run by the editorial cycle's sync step) | `scripts/sync-remote-sqlite.sh` | sync step fails: script not found. The private host passes `scripts/sync-rpi-sqlite-safe.sh` |
| `cli.SetStatsLogPath` (process log `stats` scans when `--log-path` is not given) | `data/logs/out.log` | silent: the process log is not found and cycle activity is skipped. The private host passes `data/rpi-logs/out.log` |
| `train seed-v1 --v1-team-slug` | `team` | silent: team-specific v1 rejects are skipped |

The live-lane milestone list also accepts any `touchdown_<team>` tag.

## Deliberate exceptions

- Douglas Adams with Wikidata id Q42 stays in the Wikidata fixtures: it is
  the canonical Wikidata example item, not a sports entity.
- The org name `Mundo-Dolphins` appears only in the module path and import
  paths.

## Fix round 1

A cross-check against a real roster found names and look-alikes that the
first pass missed. They were replaced with clearly invented names that match
no real roster entry (first name, last name, or both), keeping each property
under test: initials with dots, apostrophes, hyphens, suffixes, shared
surnames, typo and truncation distances, and common-word surnames. Real
non-NFL clubs, a real tweet id, a real article id and an aggregator host were
replaced by fictional ones.
