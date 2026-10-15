package editorial

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"

	"github.com/spf13/cobra"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/app/activelearning"
	"github.com/Mundo-Dolphins/phins-news-engine/app/deployment"
	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialcycle"
	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialsync"
	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialtrain"
	"github.com/Mundo-Dolphins/phins-news-engine/app/modelpromotion"
	"github.com/Mundo-Dolphins/phins-news-engine/app/telegramimport"
	toolsdictionary "github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/dictionary"
	toolsentities "github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/entities"
	toolsevents "github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/events"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/reviewdecision"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/clock"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/shellrunner"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

func newCycleCommand() *cobra.Command {
	var dryRun, resume, redecide, nonInteractive bool
	var sampleSize int
	var skipSync, skipDictionary, skipTrain, skipDeploy, skipTelegramImport, force bool
	var skipEntities, skipRosterSync, skipEventRules bool
	var entitiesVerification, eventRulesDataset string
	var remote, keyPath string
	cycleCmd := &cobra.Command{
		Use:   "cycle",
		Short: "Run the interactive end-to-end editorial review cycle",
		// The error return is named so the panic-recovery defer
		// registered below can convert a recovered panic into this
		// invocation's returned error instead of swallowing it into a
		// nil return (design spec §8).
		RunE: func(cmd *cobra.Command, _ []string) (err error) {
			// root command is a package-level singleton re-Execute()'d across
			// many tests in this package (see cmd_test.go's execute /
			// executeWithStdin helpers); pflag never resets a flag back
			// to its default when a later invocation's args omit it, so
			// without this reset a --dry-run (or --resume /
			// --non-interactive) from one test would silently leak into
			// the next one that doesn't pass it.
			defer func() {
				dryRun, resume, redecide, nonInteractive, sampleSize = false, false, false, false, 0
				skipSync, skipDictionary, skipTrain, skipDeploy, skipTelegramImport, force = false, false, false, false, false, false
				skipEntities, skipRosterSync, entitiesVerification = false, false, toolsentities.CycleVerificationLocal
				skipEventRules, eventRulesDataset = false, toolsevents.DefaultDatasetPath
				remote, keyPath = "", ""
			}()

			cfg, err := config.Load(shared.ConfigPath)
			if err != nil {
				return err
			}
			if err := toolsentities.ValidateCycleVerification(entitiesVerification); err != nil {
				return fmt.Errorf("editorial cycle: %w", err)
			}
			if remote == "" && (!skipSync || !skipDeploy) {
				return fmt.Errorf("editorial cycle: --remote is required unless both --skip-sync and --skip-deploy are set")
			}
			if !skipSync || !skipDeploy {
				if _, err := shared.GetRemoteOps(); err != nil {
					return fmt.Errorf("editorial cycle: %w", err)
				}
			}
			// Telegram history is required input for reconstructing
			// publications and training data: the strict,
			// recommended flow fails here — before sync or any other
			// database write below — rather than silently running
			// without it. --skip-telegram-import is the only sanctioned
			// bypass; it still prints a visible warning and is recorded
			// in this run's audit report.
			if !skipTelegramImport {
				if err := shared.ValidateTelegramImportConfig(cfg.Telegram); err != nil {
					return fmt.Errorf("editorial cycle: telegram import required: %w (pass --skip-telegram-import to run without it)", err)
				}
			}
			var db *sql.DB
			switch {
			case !skipSync:
				ops, err := shared.GetRemoteOps()
				if err != nil {
					return fmt.Errorf("editorial cycle: sync: %w", err)
				}
				syncClient := ops.NewSync(shared.RemoteSyncScriptPath, keyPath)
				syncedDB, syncResult, err := editorialsync.Run(cmd.Context(), editorialsync.Deps{Sync: syncClient}, editorialsync.Request{
					Host: remote, RemoteDBHint: cfg.Remote.SQLiteRemoteDBHint, LocalDBPath: cfg.SQLite.Path,
					RetentionDays: cfg.Remote.RetentionDays, BusyTimeout: cfg.SQLite.BusyTimeout(), MigrationsFS: migrations.FS,
				})
				if err != nil {
					return fmt.Errorf("editorial cycle: sync: %w", err)
				}
				db = syncedDB
				cmd.Printf("sync complete (merged=%v migrated=%v, source=%s:%s @ %s, %d bytes, sha256=%s)\n",
					syncResult.Merged, syncResult.Migrated, syncResult.SourceHost, syncResult.SourcePath,
					syncResult.SnapshotCreatedAt.Format(time.RFC3339), syncResult.IncomingSizeBytes, syncResult.IncomingChecksumSHA256)
				for _, w := range syncResult.FKWarnings {
					cmd.Printf("  warning: %s\n", w)
				}
			case dryRun:
				// --dry-run --skip-sync opens the database read-only, so
				// "writes nothing" is enforced by SQLite itself rather than
				// only by this function's control flow (design spec §9).
				// Migrations are deliberately not applied here; the plan
				// reports any pending one by name instead.
				openedDB, openErr := infrasqlite.OpenReadOnly(cfg.SQLite.Path, cfg.SQLite.BusyTimeout())
				if openErr != nil {
					return fmt.Errorf("editorial cycle: dry run: %w", openErr)
				}
				db = openedDB
			default:
				openedDB, err := infrasqlite.Open(cfg.SQLite.Path, cfg.SQLite.BusyTimeout())
				if err != nil {
					return err
				}
				if err := infrasqlite.Migrate(cmd.Context(), openedDB, migrations.FS); err != nil {
					_ = openedDB.Close()
					return err
				}
				if err := infrasqlite.Integrity(cmd.Context(), openedDB); err != nil {
					_ = openedDB.Close()
					return fmt.Errorf("editorial cycle: integrity check: %w", err)
				}
				db = openedDB
			}
			defer db.Close()

			resolvedSampleSize := sampleSize
			if resolvedSampleSize <= 0 {
				resolvedSampleSize = cfg.EditorialCycle.SampleSize
			}

			articles := sqliteadapter.NewArticleRepository(db)
			labels := sqliteadapter.NewHumanLabelRepository(db)
			events := sqliteadapter.NewDecisionRepository(db)
			reviewDecisions := sqliteadapter.NewReviewDecisionRepository(db)
			sessions := sqliteadapter.NewEditorialCycleSessionRepository(db)
			uow := sqliteadapter.NewUnitOfWork(db)
			realClock := clock.Real{}

			sessionCandidates := sqliteadapter.NewEditorialCycleSessionCandidateRepository(db)

			excludedSourceIDs := make(map[string]bool, len(cfg.Editorial.ModelBypassSourceIDs)+len(cfg.Editorial.UnconditionalPublishSourceIDs))
			for _, id := range cfg.Editorial.ModelBypassSourceIDs {
				excludedSourceIDs[id] = true
			}
			for _, id := range cfg.Editorial.UnconditionalPublishSourceIDs {
				excludedSourceIDs[id] = true
			}
			cycleDeps := editorialcycle.Deps{
				Sessions: sessions, Candidates: sessionCandidates, ReviewDecisions: reviewDecisions,
				Labels: labels, Articles: articles, Events: events, UOW: uow, Clock: realClock,
				ExcludedSourceIDs: excludedSourceIDs,
			}
			// report is assembled incrementally as each step below
			// completes and is written out — as JSON plus a markdown
			// summary on stdout — no matter how this run ends, including
			// on an early failure partway through the continuation: see
			// editorialcycle.AuditReport and editorialcycle.WriteAuditReport.
			//
			// It is created (and its writer deferred) here, before the
			// Telegram import phase and therefore before any session
			// exists, so a run that dies during that import still leaves
			// an audit trail. SessionID is stamped below, once phase 2
			// has actually created or resumed a session; a report written
			// with an empty SessionID can only mean the run never got
			// that far.
			report := editorialcycle.AuditReport{}
			// currentSession mirrors the session this run is driving, once
			// phase 2 below has produced one. It is declared here, ahead of
			// the panic-recovery defer, purely so that defer's closure can
			// reach it — a closure cannot capture a variable declared after
			// it. Its value is assigned exactly once, immediately after
			// StartOrResume returns, and never mutated afterwards, so it
			// always satisfies transition's CALLER INVARIANT (it is the
			// freshest known value for that session).
			var currentSession out.EditorialCycleSession
			defer func() {
				// --dry-run is phase 1 only and writes nothing at all —
				// including no audit-report file (design spec §5/§9).
				if dryRun {
					return
				}
				auditDir := filepath.Join(filepath.Dir(cfg.SQLite.Path), "audit")
				path, writeErr := editorialcycle.WriteAuditReport(auditDir, report, realClock.Now())
				if writeErr != nil {
					cmd.PrintErrf("editorial cycle: failed to write audit report: %v\n", writeErr)
					return
				}
				cmd.Printf("audit report: %s\n", path)
				cmd.Print(report.MarkdownSummary())
			}()

			// A panic anywhere below is converted into this invocation's
			// returned error (and recorded in the audit report) rather
			// than crashing the process, so cobra's normal exit-code path
			// applies — see design spec §8. Registered after report so it
			// can write to it, and before the phases that can panic;
			// deferred functions run LIFO, so this one runs before the
			// audit-report writer above and its report.Error therefore
			// lands in the written file.
			defer func() {
				if r := recover(); r != nil {
					panicErr := fmt.Errorf("editorial cycle: recovered panic: %v", r)
					report.Error = panicErr.Error()
					err = panicErr
					// A panic mid-review leaves the session "failed" (panic
					// recovery), not
					// "running" with nobody driving it. A panic before phase 2
					// has no session to fail. The status flip runs on a
					// non-cancellable context for the same reason the
					// interrupted path's Pause does — it must land.
					if currentSession.ID != "" {
						if failErr := editorialcycle.Fail(context.WithoutCancel(cmd.Context()), cycleDeps, currentSession, panicErr); failErr != nil {
							cmd.PrintErrf("editorial cycle: failed to mark session failed after panic: %v\n", failErr)
						}
					}
				}
			}()

			// Telegram history import — prerequisites were already
			// validated above (unless --skip-telegram-import), so a
			// failure from here on is a real operational failure, not a
			// missing-config one. Its outcome (skipped, succeeded, or
			// failed partway through) is always recorded in report
			// before this function can return, so the audit trail never
			// shows a false success.
			if skipTelegramImport {
				report.TelegramImportSkipped = true
				report.TelegramImportSkipReason = "explicit --skip-telegram-import flag"
				cmd.PrintErrln("WARNING: --skip-telegram-import is set — this session's publications will be reconstructed without Telegram history. Only use this when Telegram credentials are genuinely unavailable.")
			} else {
				tgClient, err := shared.NewTelegramImportClient(out.TelegramImportClientConfig{
					APIID: cfg.Telegram.APIID, APIHash: cfg.Telegram.APIHash, Phone: cfg.Telegram.Phone, SessionPath: cfg.Telegram.SessionPath,
				})
				if err != nil {
					err = fmt.Errorf("editorial cycle: telegram client: %w", err)
					report.Error = err.Error()
					report.TelegramImportFailed = 1
					return err
				}
				messages, err := tgClient.ExportChannelMessages(cmd.Context(), out.TelegramImportRequest{ChannelRef: cfg.Telegram.ChannelID, Limit: 1000})
				if err != nil {
					err = fmt.Errorf("editorial cycle: telegram import: %w", err)
					report.Error = err.Error()
					report.TelegramImportFailed = 1
					return err
				}
				urlCfg, resolvers := cfg.Normalization.URL.Policy()
				tgResult, tgErr := telegramimport.Import(cmd.Context(), telegramimport.Deps{Articles: articles, Labels: labels, Clock: realClock}, messages, urlCfg, resolvers, dryRun)
				report.TelegramImportProcessed = tgResult.Processed
				report.TelegramImportImported = tgResult.Imported
				report.TelegramImportAlreadyKnown = tgResult.AlreadyKnown
				report.TelegramImportUnmatched = tgResult.Unmatched
				report.TelegramImportSkippedMessages = tgResult.Skipped
				report.TelegramImportFailed = tgResult.Failed
				cmd.Printf("telegram import: processed=%d imported=%d already_known=%d unmatched=%d skipped=%d failed=%d\n",
					tgResult.Processed, tgResult.Imported, tgResult.AlreadyKnown, tgResult.Unmatched, tgResult.Skipped, tgResult.Failed)
				if tgErr != nil {
					tgErr = fmt.Errorf("editorial cycle: telegram import: %w", tgErr)
					report.Error = tgErr.Error()
					return tgErr
				}
			}

			// Roster prerequisite (spec 2026-10-08-editorial-cycle-entities):
			// the entity step's gazetteer needs a fresh nflverse roster, so it
			// is synchronised here, before any review, like the database sync
			// and the Telegram import. It is not needed when the entity step
			// is off, and a dry run never downloads anything.
			entityReason := ""
			switch {
			case skipEntities:
				entityReason = "explicit --skip-entities flag"
			case nonInteractive:
				// An unattended run must not download, call the LLM or prompt.
				entityReason = "non-interactive run"
			default:
				entityReason = toolsentities.CycleStepDisabledReason(cfg)
			}
			eventRulesReason := ""
			switch {
			case skipEventRules:
				eventRulesReason = "explicit --skip-event-rules flag"
			case nonInteractive:
				// The step prompts the operator and calls the LLM.
				eventRulesReason = "non-interactive run"
			default:
				eventRulesReason = toolsevents.CycleStepDisabledReason(cfg)
			}
			switch {
			case entityReason != "" || !cfg.EntityDiscovery.Roster.Enabled:
				report.RosterSync = "skipped:entity step off or roster disabled"
			case skipRosterSync:
				report.RosterSync = "skipped:--skip-roster-sync"
				cmd.PrintErrln("WARNING: --skip-roster-sync is set — the entity step will use the existing nflverse roster (or run without the roster gazetteer if it is stale).")
			case dryRun:
				report.RosterSync = "skipped:dry-run"
				cmd.Println("dry run: would synchronise the nflverse roster")
			default:
				players, dest, rerr := toolsentities.SyncRoster(cmd.Context(), cfg.EntityDiscovery.Roster, "", "")
				if rerr != nil {
					rerr = fmt.Errorf("editorial cycle: roster sync: %w (pass --skip-roster-sync to continue with the existing roster)", rerr)
					report.RosterSync = "failed"
					report.Error = rerr.Error()
					return rerr
				}
				report.RosterSync = fmt.Sprintf("synced:%d", players)
				cmd.Printf("roster sync: %d players written to %s\n", players, dest)
			}

			// Phase 1, planning: --dry-run stops here, having written
			// nothing at all — no session, no frozen candidate set, no
			// decision, not even an audit report (design spec §9). It
			// must return before editorialcycle.StartOrResume below,
			// which is the write this used to reach.
			if dryRun {
				pendingMigrations, pmErr := infrasqlite.PendingMigrations(cmd.Context(), db, migrations.FS)
				if pmErr != nil {
					cmd.PrintErrf("editorial cycle: dry run: could not check pending migrations: %v\n", pmErr)
					pendingMigrations = nil
				}
				// Printed before BuildPlan runs, deliberately: on a database
				// whose schema predates this branch's migrations (e.g.
				// pre-0024/0025), BuildPlan's GetLatestResumable can fail
				// with a raw "no such column" SQL error rather than a
				// friendly one, since --dry-run --skip-sync opens the
				// database read-only and therefore never applies pending
				// migrations (see the read-only branch above). An operator
				// must see this actionable warning before hitting that
				// error, not only after — or not at all, if BuildPlan
				// returns early.
				if len(pendingMigrations) > 0 {
					cmd.PrintErrf("warning: %d pending migration(s) not applied in read-only mode: %v\n", len(pendingMigrations), pendingMigrations)
				}
				plan, planErr := editorialcycle.BuildPlan(cmd.Context(), cycleDeps,
					editorialcycle.Flags{SampleSize: resolvedSampleSize, DryRun: true}, pendingMigrations)
				if planErr != nil {
					return fmt.Errorf("editorial cycle: dry run: %w", planErr)
				}
				if plan.ResumingSessionID != "" {
					cmd.Printf("dry run: would resume session %s\n", plan.ResumingSessionID)
				} else {
					cmd.Printf("dry run: would start a new session\n")
				}
				if plan.DriftWarning != "" {
					cmd.PrintErrf("warning: %s\n", plan.DriftWarning)
				}
				label := "candidate(s) selected"
				if plan.IsPreview {
					label = "candidate(s) previewed — not yet frozen; the exact set is finalized only once the session actually starts"
				}
				if entityReason != "" {
					cmd.Printf("dry run: entities: step off (%s)\n", entityReason)
				} else {
					cmd.Printf("dry run: entities: step would run with verification %s\n", entitiesVerification)
				}
				if eventRulesReason != "" {
					cmd.Printf("dry run: event rules: step off (%s)\n", eventRulesReason)
				} else {
					cmd.Println("dry run: event rules: step would run")
				}
				cmd.Printf("dry run: %d %s, no changes made\n", len(plan.Pending), label)
				for _, c := range plan.Pending {
					cmd.Printf("  %s  score=%.2f  %s\n", c.Article.ID, c.Score, c.Article.Title)
				}
				return nil
			}

			// Phase 2, session create/resume. The selection happens here,
			// after the Telegram import has had its chance to add
			// articles and labels, and its result becomes the new
			// session's frozen candidate set — which is what makes
			// Complete's pending guard mean anything. It is ignored
			// entirely when resuming (see StartOrResume).
			alDeps := activelearning.Deps{Articles: articles, Labels: labels, Events: events, ReviewDecisions: reviewDecisions, Clock: realClock, ExcludedSourceIDs: excludedSourceIDs}
			freshCandidates, err := activelearning.Select(cmd.Context(), alDeps, activelearning.Options{
				Since:             realClock.Now().AddDate(0, 0, -30),
				PoolLimit:         cfg.EditorialCycle.PoolLimit,
				SampleSize:        resolvedSampleSize,
				AmbiguousCooldown: cfg.EditorialCycle.AmbiguousCooldown(),
			})
			if err != nil {
				report.Error = err.Error()
				return err
			}

			session, resumed, backfilled, err := editorialcycle.StartOrResume(cmd.Context(), cycleDeps,
				editorialcycle.Flags{SampleSize: resolvedSampleSize}, freshCandidates)
			if err != nil {
				report.Error = err.Error()
				return err
			}
			report.SessionID = session.ID
			currentSession = session

			if backfilled {
				// See design spec §11.3: a session created before migration
				// 0024's candidate table existed has no recorded frozen set,
				// so StartOrResume reconstructed one best-effort from the
				// session's own stored flags — a real but different set from
				// whatever the original run actually reviewed.
				cmd.PrintErrf("legacy session %s: no recorded candidate set — recomputed from the current corpus; membership may differ from the original run\n", session.ID)
			}

			if resumed {
				cmd.Printf("resuming session %s\n", session.ID)
				// A freshly-started session trivially has
				// resolvable == TotalCandidates (nothing has had a
				// chance to be purged yet), so this check — and its
				// extra CountResolvable query — only runs on the
				// resume path. This mirrors BuildPlan's dry-run drift
				// computation (plan.go) so a real --resume tells the
				// operator the same thing --dry-run already does,
				// with byte-identical wording for the same condition.
				resolvable, err := cycleDeps.Candidates.CountResolvable(cmd.Context(), session.ID)
				if err != nil {
					err = fmt.Errorf("editorial cycle: count resolvable candidates: %w", err)
					report.Error = err.Error()
					return err
				}
				if resolvable < session.TotalCandidates {
					cmd.PrintErrf("warning: %d of %d originally-frozen candidate(s) are no longer in the corpus\n",
						session.TotalCandidates-resolvable, session.TotalCandidates)
				}
			} else {
				cmd.Printf("started session %s\n", session.ID)
			}

			// Phase 3, review. What this run reviews comes from the
			// session's frozen set, never from a fresh selection:
			// ListPending is the frozen set minus anything already
			// decided in this session (its own SQL anti-join) and minus
			// anything purged from the corpus since (its join against
			// articles). --redecide opts in to re-presenting the whole
			// frozen set, already-decided candidates included.
			var rows []out.EditorialCycleSessionCandidate
			if redecide {
				rows, err = sessionCandidates.ListFrozen(cmd.Context(), session.ID)
			} else {
				rows, err = sessionCandidates.ListPending(cmd.Context(), session.ID)
			}
			if err != nil {
				report.Error = err.Error()
				return err
			}
			// The frozen set persists only each candidate's article ID
			// and score (see editorialcycle.CandidatesFromFrozen), so the
			// rest of every article — title, source, cluster — is fetched
			// back here for the review screen to show.
			pending := editorialcycle.CandidatesFromFrozen(rows)
			for i, row := range rows {
				a, findErr := articles.FindByID(cmd.Context(), row.ArticleID)
				if findErr != nil {
					findErr = fmt.Errorf("editorial cycle: hydrate article %s: %w", row.ArticleID, findErr)
					report.Error = findErr.Error()
					return findErr
				}
				pending[i].Article = a
			}
			manualPending, err := editorialcycle.ExcludeSourceBypasses(cmd.Context(), cycleDeps, session.ID, pending)
			if err != nil {
				report.Error = err.Error()
				return err
			}
			if excluded := len(pending) - len(manualPending); excluded > 0 {
				cmd.Printf("excluded %d source-policy article(s) from manual review\n", excluded)
			}
			pending = manualPending
			report.ArticlesReviewed = len(pending)

			if nonInteractive {
				if len(pending) > 0 {
					cmd.Println("--non-interactive: review requires a human decision for each article; skipping the review step")
					// Leaving early here without a status transition would
					// strand the session "running" with no process driving
					// it — a false state by this branch's own state machine
					// Pause is the correct resting state: the
					// session's frozen set is untouched and --resume picks
					// it right back up.
					if pauseErr := editorialcycle.Pause(cmd.Context(), cycleDeps, session, "non-interactive with pending candidates"); pauseErr != nil {
						report.Error = pauseErr.Error()
						return pauseErr
					}
					return nil
				}
				cmd.Println("--non-interactive: no pending candidates to review")
			}

			// SIGINT/SIGTERM (and any cancellation of the parent context)
			// cancels this context, which scanLine below races against
			// the blocking stdin read so an interrupt does not have to
			// wait for the reviewer to press Enter (design spec §8.1).
			reviewCtx, stopSignals := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stopSignals()

			scanner := bufio.NewScanner(cmd.InOrStdin())
			savedAndExited := false
			for i := 0; i < len(pending); i++ {
				c := pending[i]
				printReviewScreen(cmd, articles, labels, i, len(pending), c)

				line, ok := scanLine(reviewCtx, scanner)
				if !ok {
					break
				}
				action := strings.TrimSpace(line)
				switch action {
				case "w":
					savedAndExited = true
				case "b":
					if i > 0 {
						i -= 2 // re-show the previous one next loop iteration
					} else {
						i--
					}
					continue
				default:
					resolved, mapErr := mapKeyToAction(action)
					if mapErr != nil {
						cmd.Println(mapErr.Error())
						i--
						continue
					}
					if err := editorialcycle.ApplyDecision(cmd.Context(), cycleDeps, session.ID, c.Article.ID, resolved, ""); err != nil {
						report.Error = err.Error()
						return err
					}
				}
				if savedAndExited {
					break
				}
			}

			// Phase 4, closure: exactly one guarded status transition, in
			// the priority order of design spec §8's matrix. An
			// interrupt outranks everything (it is why the loop stopped);
			// then an explicit save-and-exit; then a genuine stdin I/O
			// error, which is a failure rather than a benign pause.
			// Note that scanner.Err() is deliberately not consulted on
			// the interrupted path: scanLine abandons the in-flight read
			// rather than erroring it, so it would be nil anyway.
			switch {
			case reviewCtx.Err() != nil:
				// reviewCtx is cancelled either by a signal (cmd.Context()
				// itself is still live) or by cmd.Context() being cancelled
				// by whoever is supervising this run. In the second case
				// issuing the pause write on cmd.Context() would fail it
				// with context.Canceled and leave the session stuck
				// "running" — precisely the stuck state this branch exists
				// to prevent. The status flip is one small, bounded,
				// must-happen UPDATE, so it runs on a non-cancellable copy
				// of the context.
				closeCtx := context.WithoutCancel(cmd.Context())
				if pauseErr := editorialcycle.Pause(closeCtx, cycleDeps, session, "interrupted"); pauseErr != nil {
					report.Error = pauseErr.Error()
					return pauseErr
				}
				cmd.Println("interrupted — session paused, resume with --resume")
				return nil
			case savedAndExited:
				if pauseErr := editorialcycle.Pause(cmd.Context(), cycleDeps, session, "save-and-exit"); pauseErr != nil {
					report.Error = pauseErr.Error()
					return pauseErr
				}
				cmd.Println("session saved — resume with --resume")
				return nil
			case scanner.Err() != nil:
				failErr := fmt.Errorf("editorial cycle: reading review input: %w", scanner.Err())
				// A failed status flip here must not mask the read error
				// that caused it — the session simply stays resumable — but
				// it must not be invisible either.
				if statusErr := editorialcycle.Fail(cmd.Context(), cycleDeps, session, failErr); statusErr != nil {
					cmd.PrintErrf("editorial cycle: failed to mark session failed: %v\n", statusErr)
				}
				report.Error = failErr.Error()
				return failErr
			}

			if completeErr := editorialcycle.Complete(cmd.Context(), cycleDeps, session); completeErr != nil {
				// Complete refuses for two very different reasons and reports
				// them identically: its pending guard (a clean EOF partway
				// through the list — benign, and the reason a second bug
				// once existed) or a genuine infrastructure failure (the
				// pending query itself, or the status write). The pause
				// fallback below is right for the first and harmless for the
				// second, but it returns nil either way, so the real cause is
				// recorded in the audit report unconditionally rather than
				// being swallowed by a reassuring "paused" message.
				report.Error = fmt.Sprintf("complete: %v", completeErr)
				if pauseErr := editorialcycle.Pause(cmd.Context(), cycleDeps, session, "eof with pending"); pauseErr != nil {
					report.Error = fmt.Sprintf("complete: %v; pause: %v", completeErr, pauseErr)
					return pauseErr
				}
				cmd.Println("review incomplete — session paused, resume with --resume")
				return nil
			}
			cmd.Println("review complete")

			allDecisions, err := reviewDecisions.ListBySession(cmd.Context(), session.ID)
			if err != nil {
				report.Error = err.Error()
				return err
			}
			for _, d := range allDecisions {
				switch d.Label {
				case "publish":
					report.PublishCount++
				case "reject":
					report.RejectCount++
				}
			}

			report.EntitiesVerification = entitiesVerification
			if entityReason != "" {
				report.EntitiesSkipped = true
				report.EntitiesSkipReason = entityReason
				if !skipEntities {
					cmd.Printf("entities: step disabled (%s)\n", entityReason)
				}
			} else {
				res, eerr := toolsentities.RunCycleStep(cmd, cfg, db, session.ID, scanner, toolsentities.CycleOptions{Verification: entitiesVerification})
				// Counters first: a failure after some approvals must still be audited.
				report.EntitiesArticles, report.EntitiesProposed = res.Articles, res.Proposed
				report.EntitiesApproved, report.EntitiesRejected = res.Approved, res.Rejected
				report.EntitiesDiscoveryFailed = res.DiscoveryFailed
				if eerr != nil {
					eerr = fmt.Errorf("editorial cycle: entities: %w", eerr)
					report.Error = eerr.Error()
					return eerr
				}
				if res.Paused {
					cmd.Println("editorial cycle stopped — entity proposals saved as pending; finish them with `" + shared.ProgramName + " entities review`, then run the dictionary update, training and deployment again (the review session is already completed, so --resume does not re-enter this step)")
					return nil
				}
			}
			// Event rules step (spec 2026-10-09-event-rules-discovery): after
			// the entity step, before the dictionary step. It writes the
			// generated rules file but never activates it.
			var eventRulesDeployPaths []string
			if eventRulesReason != "" {
				report.EventRulesSkipped = true
				report.EventRulesSkipReason = eventRulesReason
				if !skipEventRules {
					cmd.Printf("event rules: step disabled (%s)\n", eventRulesReason)
				}
			} else {
				res, rerr := toolsevents.RunCycleStep(cmd, cfg, db, scanner, toolsevents.CycleOptions{DatasetPath: eventRulesDataset})
				// Counters first: a failure after some approvals must still be audited.
				report.EventRulesSampled, report.EventRulesProposed = res.Sampled, res.Proposed
				report.EventRulesApproved, report.EventRulesApplied = res.Approved, res.Applied
				report.EventRulesDiscoveryFailed, report.EventRulesGateBlocked = res.DiscoveryFailed, res.GateBlocked
				report.EventRulesGateNotExercised = res.GateRan && res.GateAffected == 0
				if rerr != nil {
					rerr = fmt.Errorf("editorial cycle: event rules: %w", rerr)
					report.Error = rerr.Error()
					return rerr
				}
				if res.Paused {
					cmd.Println("editorial cycle stopped — event rule proposals saved; finish them with `" + shared.ProgramName + " events review`, then run the dictionary update, training and deployment again (the review session is already completed, so --resume does not re-enter this step)")
					return nil
				}
				eventRulesDeployPaths = toolsevents.DeployPaths(cfg, res)
			}
			if !skipDictionary {
				approvedCount, paused, err := toolsdictionary.RunUpdateFlow(cmd, cfg, db, session.ID, false, scanner)
				if err != nil {
					err = fmt.Errorf("editorial cycle: dictionary update: %w", err)
					report.Error = err.Error()
					return err
				}
				report.DictionaryTermsAdded = approvedCount
				if paused {
					cmd.Println("editorial cycle stopped — dictionary progress saved; training and deployment deferred")
					return nil
				}
			}
			if !skipTrain {
				trustedSourceIDs := make(map[string]bool, len(cfg.Classification.TrustedSourceIDs))
				for _, id := range cfg.Classification.TrustedSourceIDs {
					trustedSourceIDs[id] = true
				}
				gateCfg := editorialtrain.GateConfig{
					MinPrecision: cfg.EditorialGates.MinPrecision, MaxRecallRegression: cfg.EditorialGates.MaxRecallRegression,
					MinRegressionBaselineExamples: cfg.EditorialGates.MinRegressionBaselineExamples,
					MinPRAUC:                      cfg.EditorialGates.MinPRAUC, MaxFalsePositiveRate: cfg.EditorialGates.MaxFalsePositiveRate,
					MinPerSourcePrecision: cfg.EditorialGates.MinPerSourcePrecision, MinSourceExamples: cfg.EditorialGates.MinSourceExamples,
					ExcludedSources:      cfg.EditorialGates.ExcludedSources,
					MaxArtifactSizeBytes: cfg.EditorialGates.MaxArtifactSizeBytes, MaxInferenceNanos: cfg.EditorialGates.MaxInferenceNanos,
				}
				trainCfg, err := shared.EditorialTrainConfig(cfg, fmt.Sprintf("fs-%d", time.Now().UTC().UnixNano()))
				if err != nil {
					err = fmt.Errorf("editorial cycle: training config: %w", err)
					report.Error = err.Error()
					return err
				}
				trainResult, err := editorialcycle.RunTraining(cmd.Context(),
					editorialcycle.TrainingDeps{Labels: labels, Registry: sqliteadapter.NewModelRegistryRepository(db)},
					editorialcycle.TrainingConfig{
						TrustedSourceIDs:  trustedSourceIDs,
						ExcludedSourceIDs: excludedSourceIDs,
						DatasetPath:       filepath.Join(filepath.Dir(cfg.SQLite.Path), "editorial_cycle_dataset.yml"),
						OutDir:            filepath.Join(filepath.Dir(cfg.Model.Path), "candidates"),
						TrainCfg:          trainCfg,
						GateCfg:           gateCfg,
						PromotedModelPath: cfg.Model.Path,
					})
				if err != nil {
					err = fmt.Errorf("editorial cycle: training: %w", err)
					report.Error = err.Error()
					return err
				}
				report.TrainingCandidates = len(trainResult.Results)

				if !trainResult.HasWinner {
					cmd.Println("no candidate passed gates")
				} else {
					w := trainResult.Winner
					report.Winner = w.Version
					cmd.Printf("winner: %s (version %s) precision=%.3f recall=%.3f f1=%.3f prauc=%.3f\n",
						w.Candidate.Model.Algorithm, w.Version, w.Candidate.TestReport.Overall.Precision(),
						w.Candidate.TestReport.Overall.Recall(), w.Candidate.TestReport.Overall.F1(), w.Candidate.TestReport.PRAUC)

					shouldPromote := force
					if !shouldPromote && !nonInteractive {
						cmd.Print("Promote this candidate to production? [y/N] ")
						promoteScanner := scanner
						if promoteScanner.Scan() && strings.TrimSpace(promoteScanner.Text()) == "y" {
							shouldPromote = true
						}
					}
					report.WinnerDeclined = !shouldPromote
					if shouldPromote {
						if err := modelpromotion.Promote(cmd.Context(),
							modelpromotion.Deps{Registry: sqliteadapter.NewModelRegistryRepository(db), UOW: uow},
							w.Dir, "", cfg.Model.Path); err != nil {
							err = fmt.Errorf("editorial cycle: promote: %w", err)
							report.Error = err.Error()
							return err
						}
						cmd.Printf("promoted %s to %s\n", w.Version, cfg.Model.Path)
					}
				}
			}

			if !skipDeploy {
				ops, err := shared.GetRemoteOps()
				if err != nil {
					err = fmt.Errorf("editorial cycle: deploy: %w", err)
					report.Error = err.Error()
					return err
				}
				client := ops.NewDeploy("scripts/deploy.sh", "scripts/healthcheck.sh", "scripts/rollback.sh", keyPath)
				result, err := deployment.Run(cmd.Context(),
					deployment.Deps{Runner: shellrunner.Real{}, Deploy: client, Health: client, Rollback: client},
					deployment.Request{
						Host: remote, KeyPath: keyPath, Force: force, CITimeout: 10 * time.Minute,
						AllowedDirtyPaths: append([]string{
							cfg.Dictionary.Path, cfg.Model.Path,
							cfg.Clustering.EntityCatalog.ManualPath, cfg.Clustering.EntityCatalog.GeneratedPath,
							filepath.Join(filepath.Dir(cfg.Model.Path), "candidates"),
							filepath.Join(filepath.Dir(cfg.Model.Path), "previous"),
						}, eventRulesDeployPaths...),
						BinaryPath: filepath.Join("build", shared.ServiceBinaryName), ToolsBinaryPath: filepath.Join("build", shared.ProgramName),
						ConfigDir: "configs", ModelDir: cfg.Model.Path, DictionaryPath: cfg.Dictionary.Path,
					})
				report.CIPassed, report.Built, report.Deployed, report.Healthy, report.RolledBack =
					result.CIPassed, result.Built, result.Deployed, result.Healthy, result.RolledBack
				cmd.Printf("ci=%v build=%v deployed=%v healthy=%v rolled_back=%v\n", result.CIPassed, result.Built, result.Deployed, result.Healthy, result.RolledBack)
				if err != nil {
					err = fmt.Errorf("editorial cycle: deploy: %w", err)
					report.Error = err.Error()
					return err
				}
			}
			return nil
		},
	}
	cycleCmd.Flags().BoolVar(&dryRun, "dry-run", false, "show the review plan (candidates, whether a session would be started or resumed, pending migrations) without writing a session, decision, label, audit report, or any other artifact — opens the database read-only when combined with --skip-sync")
	cycleCmd.Flags().BoolVar(&resume, "resume", false, "resume the most recent paused/failed/running session (session lookup is automatic; this flag documents intent). Combine with --redecide to re-present already-decided candidates")
	cycleCmd.Flags().BoolVar(&redecide, "redecide", false, "re-present every candidate in the resumed session's frozen set, even ones already decided — the explicit opt-in for re-reviewing confirmed decisions (see --resume)")
	cycleCmd.Flags().BoolVar(&nonInteractive, "non-interactive", false, "never prompt for a human decision; skips the review step entirely")
	cycleCmd.Flags().IntVar(&sampleSize, "sample-size", 0, "cap on candidates surfaced this pass (0 = use configs/settings.yml's editorial_cycle.sample_size)")
	cycleCmd.Flags().BoolVar(&skipSync, "skip-sync", false, "skip pulling the remote host's SQLite database before this run")
	cycleCmd.Flags().StringVar(&remote, "remote", "", "SSH target for sync/deploy, e.g. deploy@host.example.com (required unless --skip-sync and --skip-deploy are both set)")
	cycleCmd.Flags().StringVar(&keyPath, "key-path", "", "path to the SSH private key")
	// --skip-deploy was registered ahead of the CI/build/deploy/verify/
	// rollback tail it gates (now wired above) because the --remote
	// required-flag check earlier in RunE already consumed it: without
	// this registration, --skip-deploy would have been an unrecognized
	// flag and every caller that needs --skip-sync + --skip-deploy
	// together to bypass --remote would have failed with "unknown flag"
	// instead.
	cycleCmd.Flags().BoolVar(&skipDeploy, "skip-deploy", false, "skip the deploy step at the end of this run (required, together with --skip-sync, to omit --remote entirely)")
	cycleCmd.Flags().BoolVar(&skipEntities, "skip-entities", false, "skip mining entities from this session's articles (also skips the roster sync)")
	cycleCmd.Flags().BoolVar(&skipRosterSync, "skip-roster-sync", false, "do not download the nflverse roster before the review; the entity step uses the existing file")
	cycleCmd.Flags().StringVar(&entitiesVerification, "entities-verification", toolsentities.CycleVerificationLocal, "verification depth of the entity step: local (noise, teams, roster, Wikidata), full (adds LLM + web search) or none")
	cycleCmd.Flags().BoolVar(&skipEventRules, "skip-event-rules", false, "skip discovering and reviewing event rule trigger phrases from generic articles")
	cycleCmd.Flags().StringVar(&eventRulesDataset, "event-rules-dataset", toolsevents.DefaultDatasetPath, "gold clustering dataset the event rules apply gate scores")
	cycleCmd.Flags().BoolVar(&skipDictionary, "skip-dictionary", false, "skip mining dictionary candidates from this session's review decisions")
	cycleCmd.Flags().BoolVar(&skipTrain, "skip-train", false, "skip training and promotion entirely")
	cycleCmd.Flags().BoolVar(&skipTelegramImport, "skip-telegram-import", false, "explicitly run without importing Telegram channel history first (strict by default: missing telegram.api_id/api_hash/channel_id/phone-or-session otherwise fails before any database write). Prints a warning and is recorded in this run's audit report — publications reconstructed from this session will be missing this source")
	cycleCmd.Flags().BoolVar(&force, "force", false, "skip the promotion confirmation prompt and auto-promote a gate-passing winner (also skips automatic rollback, once wired)")
	return cycleCmd
}

// scanLine reads one line from scanner without blocking past ctx's
// cancellation — bufio.Scanner has no context awareness, so a blocked
// Scan() call runs in a goroutine raced against ctx.Done(). See the
// design spec's §8.1. ok mirrors scanner.Scan()'s own return; when ctx
// is cancelled first, ok is false and the caller must not reach for
// scanner.Err() to explain why (it will be nil — the underlying read is
// still in flight, abandoned rather than failed). The abandoned
// goroutine exits once stdin is closed or the process does; the buffered
// channel keeps it from leaking a blocked send in the meantime.
func scanLine(ctx context.Context, scanner *bufio.Scanner) (line string, ok bool) {
	done := make(chan bool, 1)
	go func() { done <- scanner.Scan() }()
	select {
	case ok := <-done:
		return scanner.Text(), ok
	case <-ctx.Done():
		return "", false
	}
}

// mapKeyToAction converts a single-letter review key into a
// reviewdecision.Action, or an error listing the valid keys.
func mapKeyToAction(key string) (reviewdecision.Action, error) {
	switch key {
	case "p":
		return reviewdecision.ActionPublish, nil
	case "r":
		return reviewdecision.ActionReject, nil
	case "a":
		return reviewdecision.ActionAmbiguous, nil
	case "d":
		return reviewdecision.ActionDuplicate, nil
	case "s":
		return reviewdecision.ActionSameNews, nil
	case "u":
		return reviewdecision.ActionUpdate, nil
	case "e":
		return reviewdecision.ActionEditCategory, nil
	case "k":
		return reviewdecision.ActionSkip, nil
	default:
		return "", fmt.Errorf("unknown action %q — use one of [p r a d s u e k b w]", key)
	}
}

// printReviewScreen renders one candidate's full detail block, matching
// the plain-terminal style every other tools command uses
// (no TUI dependency).
func printReviewScreen(
	cmd *cobra.Command,
	articles *sqliteadapter.ArticleRepository,
	labels *sqliteadapter.HumanLabelRepository,
	index, total int,
	c activelearning.Candidate,
) {
	a := c.Article
	cmd.Printf("================================================================\n")
	cmd.Printf(">>> %s [%d/%d] %s\n", a.ID, index+1, total, a.Title)
	cmd.Printf("----------------------------------------------------------------\n")
	cmd.Printf("ID:           %s\n", a.ID)
	cmd.Printf("Source:       %s (%s)   Published: %s\n", a.Source.Name, a.Source.Type, a.PublishedAt.Format("2006-01-02 15:04 MST"))
	cmd.Printf("URL:          %s\n", a.OriginalURL)
	cmd.Printf("Cluster:      %s (role: %s)\n", a.ClusterID, a.ClusterMemberRole)
	cmd.Printf("Summary:      %s\n", a.Summary)
	cmd.Printf("Score:        %.2f (ambiguity signal %.2f)\n", c.Score, c.Signals.EventScore)

	if history, err := labels.ListByArticle(cmd.Context(), a.ID); err == nil && len(history) > 0 {
		cmd.Printf("Prior label:  %s (by %s)\n", history[len(history)-1].Label, history[len(history)-1].LabeledBy)
	} else {
		cmd.Printf("Prior label:  (none)\n")
	}

	if similar, err := articles.ListByClusterID(cmd.Context(), a.ClusterID, a.ID, 5); err == nil {
		cmd.Printf("Similar:      %d other article(s) in this cluster\n", len(similar))
	}

	cmd.Printf("----------------------------------------------------------------\n")
	cmd.Printf("[p]ublish [r]eject [a]mbiguous [d]uplicate [s]ame-news [u]pdate [e]dit-category\n")
	cmd.Printf("[b]ack [k]skip [w]save-and-exit\n> ")
}
