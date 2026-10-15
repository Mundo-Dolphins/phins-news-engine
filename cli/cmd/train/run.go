package train

import (
	"database/sql"
	"fmt"
	"log"
	"strings"
	"time"
	"unicode"

	"github.com/spf13/cobra"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqliteeditoriallabelproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlitereviewqueue"
	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialtrain"
	"github.com/Mundo-Dolphins/phins-news-engine/app/retraintrigger"
	"github.com/Mundo-Dolphins/phins-news-engine/app/trainrun"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/dictionaryfile"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/entitycatalog"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/llmclient"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// retrainTriggerConfigFrom converts config.RetrainTriggersConfig into
// retraintrigger.Config -- the cmd layer's job
// (application packages never import infrastructure/config). Excludes
// AutoEnabled deliberately: that field is checked directly against
// cfg.OfflineTraining.RetrainTriggers below, before Evaluate is ever
// called.
func retrainTriggerConfigFrom(c config.RetrainTriggersConfig) retraintrigger.Config {
	return retraintrigger.Config{
		MinNewGoldExamples: c.MinNewGoldExamples, MinNewSilverExamples: c.MinNewSilverExamples,
		MaxDisagreementRate: c.MaxDisagreementRate, MaxManualErrorRate: c.MaxManualErrorRate,
		NovelVocabularyThreshold: c.NovelVocabularyThreshold, MinHardExampleCount: c.MinHardExampleCount,
	}
}

// knownTermsFrom builds the lowercase word set trainmine.UnknownTerms
// treats as "already known": every canonical name/alias in the
// classification dictionary (cfg.Dictionary.Path) plus every canonical
// name/term in the entity catalog (cfg.Clustering.EntityCatalog), each
// split into individual words the same way UnknownTerms tokenizes
// titles. A load failure on either source logs a warning and
// contributes nothing from that source -- this is a best-effort report
// nudge, never something train run should abort
// a promotion run over.
func knownTermsFrom(cfg *config.Config) map[string]bool {
	known := map[string]bool{}
	addWords := func(phrase string) {
		for _, w := range strings.FieldsFunc(phrase, func(r rune) bool { return !unicode.IsLetter(r) }) {
			known[strings.ToLower(w)] = true
		}
	}

	if cfg.Dictionary.Path != "" {
		dict, err := dictionaryfile.Load(cfg.Dictionary.Path)
		if err != nil {
			log.Printf("train run: load dictionary %s for known-terms nudge: %v (continuing without it)", cfg.Dictionary.Path, err)
		} else {
			for _, cat := range dict.Categories {
				for _, e := range cat.Entries {
					addWords(e.CanonicalName)
					for _, alias := range e.Aliases {
						addWords(alias)
					}
				}
			}
		}
	}

	if cfg.Clustering.EntityCatalog.ManualPath != "" && cfg.Clustering.EntityCatalog.GeneratedPath != "" {
		entities, err := entitycatalog.Load(cfg.Clustering.EntityCatalog.ManualPath, cfg.Clustering.EntityCatalog.GeneratedPath)
		if err != nil {
			log.Printf("train run: load entity catalog for known-terms nudge: %v (continuing without it)", err)
		} else {
			for _, e := range entities {
				addWords(e.CanonicalName)
				for _, term := range e.Terms {
					addWords(term)
				}
			}
		}
	}

	return known
}

// newRunCommand builds `train run`, the full-workflow orchestrator
// (#253 part 22): build -> analyze -> evaluate -> promote, aborting
// cleanly on any stage failure. --auto (#253 part 23) makes it check
// offline_training.retrain_triggers first: disabled
// (auto_enabled=false, the default) is an immediate, DB-untouched
// no-op; enabled but no threshold fired is also a no-op, just after a
// cheap signal-gathering read; only a fired threshold runs the full
// pipeline.
func newRunCommand() *cobra.Command {
	var force, auto bool
	var datasetPath, candidatesDir string
	var includeDB []string
	var maxPerCluster int
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run the full safe-promotion workflow end-to-end: build, analyze, evaluate, promote",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(shared.ConfigPath)
			if err != nil {
				return err
			}
			if cfg.Model.Path == "" {
				return fmt.Errorf("train run: model.path is not configured")
			}
			if cfg.OfflineTraining.LLM.AutoLabelThreshold <= 0 {
				return fmt.Errorf("train run: offline_training.llm.auto_label_threshold must be configured above 0")
			}

			if auto && !cfg.OfflineTraining.RetrainTriggers.AutoEnabled {
				cmd.Println("auto training disabled (offline_training.retrain_triggers.auto_enabled=false)")
				return nil
			}

			db, err := infrasqlite.Open(cfg.SQLite.Path, cfg.SQLite.BusyTimeout())
			if err != nil {
				return err
			}
			defer db.Close()
			if err := infrasqlite.Migrate(cmd.Context(), db, migrations.FS); err != nil {
				return err
			}
			var archivedDBs []*sql.DB
			defer func() {
				for _, archived := range archivedDBs {
					_ = archived.Close()
				}
			}()
			var archivedLabels []out.HumanLabelRepository
			for _, path := range includeDB {
				archive, openErr := infrasqlite.OpenReadOnly(path, cfg.SQLite.BusyTimeout())
				if openErr != nil {
					return openErr
				}
				archivedDBs = append(archivedDBs, archive)
				archivedLabels = append(archivedLabels, sqliteadapter.NewHumanLabelRepository(archive))
			}

			trustedSourceIDs := make(map[string]bool, len(cfg.Classification.TrustedSourceIDs))
			for _, id := range cfg.Classification.TrustedSourceIDs {
				trustedSourceIDs[id] = true
			}

			deps := trainrun.Deps{
				Registry:       sqliteadapter.NewModelRegistryRepository(db),
				RunRepo:        sqliteadapter.NewTrainingRunRepository(db),
				UOW:            sqliteadapter.NewUnitOfWork(db),
				Labels:         sqliteadapter.NewHumanLabelRepository(db),
				Articles:       sqliteadapter.NewArticleRepository(db),
				Proposals:      sqliteeditoriallabelproposal.NewEditorialLabelProposalRepository(db),
				Queue:          sqlitereviewqueue.NewReviewQueueRepository(db),
				ArchivedLabels: archivedLabels,
			}
			deps.LLM = llmclient.New(shared.OfflineTrainingLLMClientConfig(cfg.OfflineTraining.LLM))
			deps.HardExamples = sqliteadapter.NewHardExampleRepository(db)
			trainCfg, err := shared.EditorialTrainConfig(cfg, fmt.Sprintf("fs-%d", time.Now().UTC().UnixNano()))
			if err != nil {
				return err
			}
			// The teacher is deliberately offline-only. Its output may
			// improve this candidate dataset, but the production model
			// remains Naive Bayes.
			trainCfg.Algorithms = []string{"naive_bayes"}
			opts := trainrun.Options{
				DatasetPath: datasetPath, MaxPerCluster: maxPerCluster, CandidatesDir: candidatesDir,
				ModelPath: cfg.Model.Path, TrustedSourceIDs: trustedSourceIDs,
				AutoLabelThreshold: cfg.OfflineTraining.LLM.AutoLabelThreshold,
				TrainCfg:           trainCfg,
				GateCfg: editorialtrain.GateConfig{
					MinPrecision: cfg.EditorialGates.MinPrecision, MaxRecallRegression: cfg.EditorialGates.MaxRecallRegression,
					MinRegressionBaselineExamples: cfg.EditorialGates.MinRegressionBaselineExamples,
					MinPRAUC:                      cfg.EditorialGates.MinPRAUC, MaxFalsePositiveRate: cfg.EditorialGates.MaxFalsePositiveRate,
					MinPerSourcePrecision: cfg.EditorialGates.MinPerSourcePrecision, MinSourceExamples: cfg.EditorialGates.MinSourceExamples,
					ExcludedSources:      cfg.EditorialGates.ExcludedSources,
					MaxArtifactSizeBytes: cfg.EditorialGates.MaxArtifactSizeBytes, MaxInferenceNanos: cfg.EditorialGates.MaxInferenceNanos,
				},
				PromotionCfg: promotionCriteriaFrom(cfg.OfflineTraining.Promotion),
				Force:        force, Auto: auto,
				MiningBatchLimit: cfg.OfflineTraining.Mining.BatchLimit, MiningMinNBConfidence: cfg.OfflineTraining.Mining.MinNBConfidence,
				KnownTerms: knownTermsFrom(cfg),
			}

			if auto {
				signals, err := trainrun.GatherAutoSignals(cmd.Context(), deps, opts)
				if err != nil {
					return err
				}
				result := retraintrigger.Evaluate(retrainTriggerConfigFrom(cfg.OfflineTraining.RetrainTriggers), signals)
				if !result.Triggered {
					cmd.Println("no retrain triggered: no configured criteria were met")
					return nil
				}
				opts.TriggerReasons = result.Reasons
			}

			outcome, err := trainrun.Run(cmd.Context(), deps, opts, time.Now().UTC())
			if err != nil {
				return err
			}
			for _, warning := range outcome.Warnings {
				cmd.Println(warning)
			}
			cmd.Print(outcome.Report)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false,
		"promote even if the candidate fails the offline_training.promotion criteria; "+
			"editorial_gates are a hard floor this flag cannot bypass — a candidate that fails them is never eligible for promotion, forced or not")
	cmd.Flags().BoolVar(&auto, "auto", false, "only run if offline_training.retrain_triggers.auto_enabled is true and a configured trigger fires")
	cmd.Flags().StringVar(&datasetPath, "dataset", "data/editorial_dataset.yml", "path to write the built dataset to")
	cmd.Flags().StringVar(&candidatesDir, "candidates", "models/editorial/candidates", "directory to write trained candidates to")
	cmd.Flags().IntVar(&maxPerCluster, "max-per-cluster", 1, "maximum number of examples kept from any one event cluster")
	cmd.Flags().StringSliceVar(&includeDB, "include-db", nil, "read-only SQLite snapshot whose human labels should be merged into Gold (repeatable)")
	return cmd
}
