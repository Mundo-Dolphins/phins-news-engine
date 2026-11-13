import type { ComponentChildren } from "preact";
import { useCallback, useEffect, useRef, useState } from "preact/hooks";
import { listJobs, runAction, type Job, type LogLine, type Result } from "../api";
import { LocalOnly } from "../components/LocalOnly";
import { DestructiveConfirm } from "../components/ConfirmDialog";
import { activeJob, errorText, untilJobEnd, useShortcutIsolation } from "../components/reviewParts";
import { ReviewArticlesScreen } from "./ReviewArticles";
import { ReviewEntitiesScreen } from "./ReviewEntities";
import { ReviewEventsScreen } from "./ReviewEvents";
import { ReviewTermsScreen } from "./ReviewTerms";

/** StepState is one step of `cycle.state`. */
interface StepState {
  status: "pending" | "running" | "done" | "skipped" | "failed";
  result: Record<string, unknown> | null;
  error: string;
  updated_at: string;
}

/** Wizard is the saved editorial cycle of `cycle.state`. */
interface Wizard {
  cycle_id: string;
  started_at: string;
  step: string;
  session_id: string;
  steps: Record<string, StepState>;
  sample_size: number;
  entities_verification: string;
}

/** CycleView is the object every `cycle.state`, `start`, `next` and `abandon` returns. */
interface CycleView {
  cycle: Wizard | null;
  available: { sync: boolean; telegram: boolean };
  order: string[];
  skippable: string[];
  disabled: Record<string, string>;
  /** Set by the `next` that finishes the cycle. */
  audit_report?: string;
}

interface StepInfo {
  label: string;
  about: string;
  /** The long-running action the step runs, and its button. */
  action?: string;
  run?: string;
}

const STEPS: Record<string, StepInfo> = {
  sync: {
    label: "Sync with the remote host",
    about: "Copies the operational database from the remote host and merges it into the local one, as the CLI does first.",
    action: "cycle.sync",
    run: "Run sync",
  },
  telegram_import: {
    label: "Telegram import",
    about: "Imports the channel history from Telegram with the session saved by the terminal login. The panel never asks for a login code.",
    action: "cycle.telegram_import",
    run: "Run Telegram import",
  },
  roster_sync: {
    label: "Roster sync",
    about: "Refreshes the league roster the entity step verifies against.",
    action: "cycle.roster_sync",
    run: "Run roster sync",
  },
  article_review: { label: "Article review", about: "Decide every article of the session's review set." },
  entities: {
    label: "Entities",
    about: "Discovers entities in the articles the session published or rejected, then reviews the proposals.",
    action: "cycle.entities_discover",
    run: "Discover entities",
  },
  event_rules: {
    label: "Event rules",
    about: "Discover event rule phrases, review them and apply the approved ones; failing proposals are flagged, never rejected for you.",
  },
  mined_terms: { label: "Mined terms", about: "Approve, reject or categorize the terms mined from the session; the dictionary is written when the step is finished." },
  training: {
    label: "Training",
    about: "Trains the candidate models; a winner waits for the promote decision.",
    action: "cycle.train",
    run: "Run training",
  },
  summary: { label: "Summary", about: "The cycle's report. Finishing writes the audit report and closes the cycle." },
};

const labelOf = (step: string) => STEPS[step]?.label ?? step;
const finished = (s?: StepState) => s?.status === "done" || s?.status === "skipped";

/**
 * The steps that can be left `running`, with the jobs that set it: the
 * wizard follows such a job after the page is reopened.
 */
const FOLLOWED: Record<string, string[]> = {
  sync: ["cycle.sync"],
  telegram_import: ["cycle.telegram_import"],
  roster_sync: ["cycle.roster_sync"],
  entities: ["cycle.entities_discover"],
  event_rules: ["cycle.event_rules_discover", "cycle.event_rules_apply"],
  training: ["cycle.train"],
};

/** Notice is a message above the step panel. */
interface Notice {
  kind: "info" | "error";
  text: string;
}

/** StepJob is the job the current step runs, with its log. */
interface StepJob {
  id: string;
  lines: LogLine[];
  status: string;
  error?: string;
  result?: Result;
}

async function call(name: string, params: Record<string, unknown> = {}, confirm = false): Promise<Result> {
  const out = await runAction(name, params, confirm);
  if ("job" in out) throw new Error("unexpected job for " + name);
  return out.result;
}

/** Cycle is the web form of `editorial cycle`: every step in the CLI's order, each waiting for Next. */
export function Cycle() {
  return (
    <LocalOnly>
      <CycleScreen />
    </LocalOnly>
  );
}

function CycleScreen() {
  const [view, setView] = useState<CycleView | null>(null);
  const [loadError, setLoadError] = useState("");
  // The notice, job and stale mark belong to the step they were set at
  // (keyed by cycle and step), so a new step starts without them.
  const [keyedNotice, setKeyedNotice] = useState<(Notice & { key: string }) | null>(null);
  const [keyedJob, setKeyedJob] = useState<(StepJob & { key: string }) | null>(null);
  const [staleKey, setStaleKey] = useState("");
  // Set while a job started by an embedded review screen runs.
  const [embeddedKey, setEmbeddedKey] = useState("");
  const [moving, setMoving] = useState(false);
  const [abandoning, setAbandoning] = useState(false);
  const [report, setReport] = useState("");
  const streams = useRef<Set<() => void>>(new Set());
  const alive = useRef(true);

  useEffect(
    () => () => {
      alive.current = false;
      streams.current.forEach((close) => close());
    },
    [],
  );

  const load = useCallback(async () => {
    try {
      const v = (await call("cycle.state")).object as CycleView;
      if (!alive.current) return;
      setView(v);
      setLoadError("");
    } catch (e) {
      if (alive.current) setLoadError(errorText(e));
    }
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  const cycle = view?.cycle ?? null;
  const step = cycle?.step ?? "";
  const st = cycle?.steps[step];
  const stepKey = cycle ? cycle.cycle_id + "/" + step : "";
  const keyRef = useRef(stepKey);
  keyRef.current = stepKey;

  const notice = keyedNotice && keyedNotice.key === stepKey ? keyedNotice : null;
  const job = keyedJob && keyedJob.key === stepKey ? keyedJob : null;
  const stale = staleKey !== "" && staleKey === stepKey;
  const embeddedRunning = embeddedKey !== "" && embeddedKey === stepKey;
  const setNotice = (n: Notice | null) => setKeyedNotice(n && { ...n, key: keyRef.current });

  /** follow shows a step job's log until it ends, then reloads the state. */
  const follow = async (j: Job) => {
    setKeyedJob({ id: j.id, lines: [], status: j.status, key: keyRef.current });
    type Cur = (StepJob & { key: string }) | null;
    const update = (f: (cur: StepJob & { key: string }) => Cur) => setKeyedJob((cur: Cur) => (cur && cur.id === j.id ? f(cur) : cur));
    const append = (l: LogLine) => update((cur) => (cur.lines.some((x) => x.seq === l.seq) ? cur : { ...cur, lines: [...cur.lines, l] }));
    try {
      const end = await untilJobEnd(j, streams.current, append);
      if (!alive.current) return;
      update((cur) => ({ ...cur, status: end.status, error: end.error, result: end.result }));
    } catch (e) {
      if (alive.current) setNotice({ kind: "error", text: "Lost track of job " + j.id + ": " + errorText(e) });
    }
    // The step's outcome is in the wizard state (admin store), read again.
    await load();
  };

  /** runStep runs a step action; a long one is followed as a job. */
  const runStep = async (name: string) => {
    setNotice(null);
    setStaleKey("");
    setKeyedJob(null);
    try {
      const out = await runAction(name, {}, false);
      if ("job" in out) return await follow(out.job);
      if (out.result.summary) setNotice({ kind: "info", text: out.result.summary });
    } catch (e) {
      setNotice({ kind: "error", text: "Could not run the step: " + errorText(e) });
    }
    await load();
  };

  // Reopened while a step job runs: follow it; left running by an earlier
  // panel process: offer to run it again.
  const status = st?.status;
  useEffect(() => {
    const names = FOLLOWED[step];
    if (status !== "running" || !names || job || embeddedRunning) return;
    let cancelled = false;
    listJobs()
      .then((all) => {
        if (cancelled) return;
        const j = all.find((x) => names.includes(x.action) && activeJob(x.status));
        if (j) follow(j);
        else setStaleKey(keyRef.current);
      })
      .catch(() => !cancelled && setStaleKey(keyRef.current));
    return () => {
      cancelled = true;
    };
  }, [cycle?.cycle_id, step, status, embeddedRunning]);

  const jobRunning = job !== null && activeJob(job.status);
  // A job is known to run, or the wizard is still checking for one.
  const working = jobRunning || embeddedRunning || moving;
  const busy = working || (status === "running" && !stale && FOLLOWED[step] !== undefined);
  // A training winner waiting for its decision is declined through its own confirmation, not skipped.
  const winnerWaiting = step === "training" && !!st?.result?.winner && st.result.promotion === "";

  const setEmbeddedJob = (running: boolean) => {
    if (running) {
      setStaleKey("");
      setEmbeddedKey(keyRef.current);
      return;
    }
    setEmbeddedKey("");
    load();
  };

  const next = async (skip: boolean) => {
    if (moving) return;
    setMoving(true);
    setNotice(null);
    try {
      const v = (await call("cycle.next", { skip })).object as CycleView;
      setView(v);
      if (v.audit_report) setReport(v.audit_report);
    } catch (e) {
      setNotice({ kind: "error", text: errorText(e) });
      await load();
    } finally {
      setMoving(false);
    }
  };

  const abandonSent = useRef(false);
  const abandon = async () => {
    setAbandoning(false);
    // The confirmation's button can be clicked again before it closes.
    if (abandonSent.current) return;
    abandonSent.current = true;
    setNotice(null);
    try {
      setView((await call("cycle.abandon", {}, true)).object as CycleView);
      setReport("");
    } catch (e) {
      setNotice({ kind: "error", text: "Could not abandon the cycle: " + errorText(e) });
    } finally {
      abandonSent.current = false;
    }
  };

  return (
    <>
      <h1>Editorial cycle</h1>
      <p class="muted">
        The web form of <code>editorial cycle</code>: the same steps in the same order, each showing its result and
        waiting for Next. The cycle is saved, so it can be left and resumed at the same step. Deploy stays in the
        terminal.
      </p>
      {loadError && (
        <p class="notice notice-error" role="alert">
          {loadError}{" "}
          {/database sync in progress/.test(loadError) && <span>A database sync is running: reload once it has finished. </span>}
          <button type="button" class="btn" onClick={() => load()}>
            Reload
          </button>
        </p>
      )}
      {view === null ? (
        !loadError && <p class="muted">Loading…</p>
      ) : cycle === null ? (
        <>
          {report && (
            <p class="notice notice-info" role="status">
              The cycle is finished. Audit report: <code>{report}</code>
            </p>
          )}
          {notice && <NoticeLine n={notice} />}
          <StartForm
            onStarted={(v) => {
              setReport("");
              setView(v);
            }}
          />
        </>
      ) : (
        <div class="cycle-layout">
          <StepRail view={view} cycle={cycle} />
          <div class="cycle-main">
            <section class="cycle-panel" aria-labelledby="cycle-step-title">
              <h2 id="cycle-step-title">
                Step {view.order.indexOf(step) + 1} · {labelOf(step)}{" "}
                <span class={"badge status-" + (st?.status ?? "pending")}>{st?.status ?? "pending"}</span>
              </h2>
              <p class="muted">{STEPS[step]?.about}</p>
              {notice && <NoticeLine n={notice} />}
              <StepPanel
                view={view}
                cycle={cycle}
                step={step}
                st={st}
                job={job}
                stale={stale}
                busy={busy}
                runStep={runStep}
                reload={load}
                setNotice={setNotice}
                setEmbeddedJob={setEmbeddedJob}
              />
            </section>
            <div class="rules-zone-bar cycle-actions">
              <button type="button" class="btn primary" disabled={!finished(st) || busy} onClick={() => next(false)}>
                {step === "summary" ? "Finish cycle" : "Next"}
              </button>
              {view.skippable.includes(step) && !finished(st) && !winnerWaiting && (
                <button type="button" class="btn" disabled={busy} onClick={() => next(true)}>
                  Skip step
                </button>
              )}
              <span class="spacer" />
              <button type="button" class="btn danger" disabled={working} onClick={() => setAbandoning(true)}>
                Abandon cycle
              </button>
            </div>
          </div>
        </div>
      )}
      {abandoning && <DestructiveConfirm name="cycle.abandon" onConfirm={abandon} onCancel={() => setAbandoning(false)} />}
    </>
  );
}

function NoticeLine({ n }: { n: Notice }) {
  return (
    <p class={"notice notice-" + n.kind} role={n.kind === "error" ? "alert" : "status"}>
      {n.text}
    </p>
  );
}

function StartForm({ onStarted }: { onStarted(v: CycleView): void }) {
  const [sample, setSample] = useState("0");
  const [verification, setVerification] = useState("local");
  const [error, setError] = useState("");
  const [sending, setSending] = useState(false);
  const start = async (e: Event) => {
    e.preventDefault();
    if (sending) return;
    const n = Number(sample);
    if (!Number.isInteger(n) || n < 0) return setError("The sample size must be a whole number, 0 or more.");
    setSending(true);
    setError("");
    try {
      onStarted((await call("cycle.start", { sample_size: n, entities_verification: verification })).object as CycleView);
    } catch (e) {
      setError("Could not start the cycle: " + errorText(e));
    } finally {
      setSending(false);
    }
  };
  return (
    <form class="schema-form cycle-start" onSubmit={start}>
      <p>No editorial cycle is in progress.</p>
      <div class="field">
        <label for="cycle-sample">Sample size</label>
        <input
          id="cycle-sample"
          type="number"
          min={0}
          step={1}
          value={sample}
          onInput={(e) => setSample((e.target as HTMLInputElement).value)}
        />
        <span class="hint">Cap on the articles to review; 0 uses the configured sample size.</span>
      </div>
      <div class="field">
        <label for="cycle-verification">Entity verification</label>
        <select id="cycle-verification" value={verification} onChange={(e) => setVerification((e.target as HTMLSelectElement).value)}>
          <option value="local">local</option>
          <option value="full">full</option>
          <option value="none">none</option>
        </select>
        <span class="hint">How deep the entity step verifies its proposals.</span>
      </div>
      {error && (
        <p class="error" role="alert">
          {error}
        </p>
      )}
      <div>
        <button type="submit" class="btn primary" disabled={sending}>
          Start cycle
        </button>
      </div>
    </form>
  );
}

function StepRail({ view, cycle }: { view: CycleView; cycle: Wizard }) {
  return (
    <ol class="cycle-rail" aria-label="Cycle steps">
      {view.order.map((s) => {
        const status = cycle.steps[s]?.status ?? "pending";
        const current = s === cycle.step;
        return (
          <li key={s} class={current ? "current" : undefined} aria-current={current ? "step" : undefined}>
            <span class="cycle-rail-label">{labelOf(s)}</span>
            <span class={"badge status-" + status}>{status}</span>
          </li>
        );
      })}
    </ol>
  );
}

interface PanelProps {
  view: CycleView;
  cycle: Wizard;
  step: string;
  st?: StepState;
  job: StepJob | null;
  stale: boolean;
  busy: boolean;
  runStep(name: string): Promise<void>;
  reload(): Promise<void>;
  setNotice(n: Notice | null): void;
  /** setEmbeddedJob reports a job started by an embedded review screen starting or ending. */
  setEmbeddedJob(running: boolean): void;
}

function StepPanel(p: PanelProps) {
  switch (p.step) {
    case "sync":
    case "telegram_import":
    case "roster_sync":
      return <JobStep {...p} />;
    case "article_review":
      return finished(p.st) ? <StepResult st={p.st} /> : <ReviewArticlesScreen onDone={() => p.reload()} />;
    case "entities":
      return <EntitiesStep {...p} />;
    case "event_rules":
      return <EventRulesStep {...p} />;
    case "mined_terms":
      return <TermsStep {...p} />;
    case "training":
      return <TrainingStep {...p} />;
    case "summary":
      return <SummaryStep />;
    default:
      return <p class="muted">Unknown step.</p>;
  }
}

/** unavailable reports a step this host cannot run at all (no remote or Telegram hooks). */
const unavailable = (view: CycleView, step: string) =>
  (step === "sync" && !view.available.sync) || (step === "telegram_import" && !view.available.telegram);

/** JobStep is a step that runs one long action: Run, its log, then its result or error and Retry. */
function JobStep({ view, step, st, job, stale, busy, runStep }: PanelProps) {
  const info = STEPS[step];
  const reason = view.disabled[step];
  if (unavailable(view, step) && !finished(st)) {
    return (
      <div class="cycle-unavailable" role="note">
        <p>
          <strong>Not available on this host</strong>
        </p>
        {reason && <p class="muted">{reason}</p>}
        <p class="muted">Skip the step to go on without it.</p>
      </div>
    );
  }
  return (
    <>
      {reason && !finished(st) && (
        <p class="review-reason" role="note">
          {reason}
        </p>
      )}
      <RunControls st={st} job={job} stale={stale} busy={busy} label={info.run!} onRun={() => runStep(info.action!)} />
      <JobBox job={job} stepError={st?.error} />
      {finished(st) && <StepResult st={st} />}
      {step === "sync" && st?.status === "done" && (
        <p class="notice notice-info">
          The local operational database was replaced by the synced copy: the articles, proposals and labels the next
          steps show come from it. The cycle's own progress is kept in the admin state.
        </p>
      )}
    </>
  );
}

/** RunControls shows the step error and the Run or Retry button of a job step. */
function RunControls({ st, job, stale, busy, label, onRun }: { st?: StepState; job: StepJob | null; stale: boolean; busy: boolean; label: string; onRun(): void }) {
  if (finished(st)) return null;
  const failed = st?.status === "failed";
  return (
    <>
      {failed && st?.error && (
        <p class="notice notice-error cycle-step-error" role="alert">
          {st.error}
        </p>
      )}
      {stale && (
        <p class="notice notice-error" role="alert">
          The step was left running by an earlier panel process; run it again.
        </p>
      )}
      {(st?.status !== "running" || stale) && (
        <div class="rules-zone-bar">
          <button type="button" class="btn primary" disabled={busy && !stale} onClick={onRun}>
            {failed || stale ? "Retry" : label}
          </button>
          {job && activeJob(job.status) && <span class="muted">Running…</span>}
        </div>
      )}
    </>
  );
}

/** JobBox shows the step job's status, log and outcome. */
function JobBox({ job, stepError }: { job: StepJob | null; stepError?: string }) {
  const logRef = useRef<HTMLPreElement>(null);
  useEffect(() => {
    const el = logRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [job?.lines.length]);
  if (!job) return null;
  return (
    <div class="cycle-job">
      <p class="muted" role="status">
        Job <a href={"/jobs/" + job.id}>{job.id}</a> <span class={"badge status-" + job.status}>{job.status}</span>
      </p>
      <pre class="log" ref={logRef}>
        {job.lines.map((l) => l.line).join("\n") || "(no output yet)"}
      </pre>
      {job.error && job.error !== stepError && (
        <p class="notice notice-error" role="alert">
          {job.error}
        </p>
      )}
      {job.result?.summary && <p class="summary">{job.result.summary}</p>}
    </div>
  );
}

/** Result keys shown as notes or by their step, not as facts. */
const NOT_FACTS = new Set(["skip_reason", "warning", "warnings", "offered", "categories", "winner", "discovered", "prepared"]);

const factLabel = (k: string) => k.replace(/_/g, " ");

function factValue(v: unknown): string | null {
  if (v === null || v === undefined || v === "") return null;
  if (typeof v === "boolean") return v ? "yes" : "no";
  if (Array.isArray(v)) return v.length === 0 ? null : v.map(String).join(", ");
  if (typeof v === "object") return null;
  return String(v);
}

/** StepResult shows a step's recorded result: why it was skipped, its warnings, then its counters. */
function StepResult({ st, children }: { st?: StepState; children?: ComponentChildren }) {
  const r = st?.result ?? {};
  const warnings = [r.warning, ...(Array.isArray(r.warnings) ? r.warnings : [])].filter((w): w is string => typeof w === "string" && w !== "");
  const facts = Object.entries(r)
    .filter(([k]) => !NOT_FACTS.has(k))
    .map(([k, v]) => [k, factValue(v)] as const)
    .filter((e): e is readonly [string, string] => e[1] !== null);
  return (
    <div class="cycle-result">
      {typeof r.skip_reason === "string" && r.skip_reason && (
        <p class="notice notice-info" role="status">
          Skipped: {r.skip_reason}
        </p>
      )}
      {warnings.map((w, i) => (
        <p key={i} class="notice notice-error" role="note">
          {w}
        </p>
      ))}
      {children}
      {facts.length > 0 && (
        <dl class="facts">
          {facts.map(([k, v]) => (
            <div class="fact" key={k}>
              <dt>{factLabel(k)}</dt>
              <dd>{v}</dd>
            </div>
          ))}
        </dl>
      )}
    </div>
  );
}

/** Disabled shows why a review step cannot run on this configuration. */
function Disabled({ reason }: { reason: string }) {
  return (
    <div class="cycle-unavailable" role="note">
      <p>
        <strong>The step is disabled</strong>
      </p>
      <p class="muted">{reason}</p>
      <p class="muted">Skip the step to go on without it.</p>
    </div>
  );
}

/** finishStep runs a review step's finish action and reloads the state. */
async function finishStep(p: PanelProps, name: string) {
  p.setNotice(null);
  try {
    const r = await call(name);
    if (r.summary) p.setNotice({ kind: "info", text: r.summary });
  } catch (e) {
    p.setNotice({ kind: "error", text: errorText(e) });
  }
  await p.reload();
}

function EntitiesStep(p: PanelProps) {
  const { view, cycle, st, job, stale, busy } = p;
  const [finishing, setFinishing] = useState(false);
  if (finished(st)) return <StepResult st={st} />;
  const reason = view.disabled.entities;
  if (reason) return <Disabled reason={reason} />;
  const discovered = st?.result?.discovered === true;
  return (
    <>
      {!discovered && (
        <RunControls st={st} job={job} stale={stale} busy={busy} label="Discover entities" onRun={() => p.runStep("cycle.entities_discover")} />
      )}
      <JobBox job={job} stepError={st?.error} />
      {discovered && (
        <>
          <StepResult st={st} />
          <div class="rules-zone-bar">
            <button type="button" class="btn" disabled={busy} onClick={() => p.runStep("cycle.entities_discover")}>
              Run discovery again
            </button>
            <span class="spacer" />
            <button
              type="button"
              class="btn primary"
              disabled={busy || finishing}
              onClick={async () => {
                setFinishing(true);
                await finishStep(p, "cycle.entities_finish");
                setFinishing(false);
              }}
            >
              Finish entity review
            </button>
          </div>
          {!busy && <ReviewEntitiesScreen sessionId={cycle.session_id} />}
        </>
      )}
    </>
  );
}

function EventRulesStep(p: PanelProps) {
  const { view, st, job, stale, busy } = p;
  const [finishing, setFinishing] = useState(false);
  if (finished(st)) return <StepResult st={st} />;
  const reason = view.disabled.event_rules;
  if (reason) return <Disabled reason={reason} />;
  return (
    <>
      {st?.status === "failed" && st.error && (
        <p class="notice notice-error" role="alert">
          {st.error}
        </p>
      )}
      {stale && (
        <p class="notice notice-error" role="alert">
          The step was left running by an earlier panel process: run Discover or Apply again below, or finish the step.
        </p>
      )}
      <JobBox job={job} stepError={st?.error} />
      <ReviewEventsScreen
        discoverAction="cycle.event_rules_discover"
        applyAction="cycle.event_rules_apply"
        onJobStart={() => p.setEmbeddedJob(true)}
        onJobEnd={() => p.setEmbeddedJob(false)}
      />
      <div class="rules-zone-bar">
        <span class="spacer" />
        <button
          type="button"
          class="btn primary"
          disabled={finishing || busy}
          onClick={async () => {
            setFinishing(true);
            await finishStep(p, "cycle.event_rules_finish");
            setFinishing(false);
          }}
        >
          Finish event rules review
        </button>
      </div>
    </>
  );
}

/** TermsStep mines the terms first (`cycle.terms_prepare`), then embeds their review. */
function TermsStep(p: PanelProps) {
  const { cycle, st } = p;
  const prepared = st?.result?.prepared === true;
  const [preparing, setPreparing] = useState(false);
  const [error, setError] = useState("");
  const tried = useRef("");

  const prepare = async () => {
    setPreparing(true);
    setError("");
    try {
      const r = await call("cycle.terms_prepare");
      if (r.summary) p.setNotice({ kind: "info", text: r.summary });
      await p.reload();
    } catch (e) {
      setError("Could not mine the terms: " + errorText(e));
    } finally {
      setPreparing(false);
    }
  };

  useEffect(() => {
    if (finished(st) || prepared || tried.current === cycle.cycle_id) return;
    tried.current = cycle.cycle_id;
    prepare();
  }, [cycle.cycle_id, prepared]);

  if (finished(st)) return <StepResult st={st} />;
  if (error) {
    return (
      <p class="error" role="alert">
        {error}{" "}
        <button type="button" class="btn" onClick={() => prepare()}>
          Retry
        </button>
      </p>
    );
  }
  if (!prepared || preparing) return <p class="muted">Mining the session's terms…</p>;
  return <ReviewTermsScreen onDone={() => p.reload()} />;
}

interface Winner {
  version: string;
  algorithm: string;
  precision: number;
  recall: number;
  f1: number;
  prauc: number;
  dir: string;
}

const metric = (v: number) => (typeof v === "number" ? v.toFixed(3) : "");

/** TrainingStep runs the training; a winner waits for Promote or Don't promote. */
function TrainingStep(p: PanelProps) {
  const { st, job, stale, busy } = p;
  const r = st?.result ?? null;
  const winner = (r?.winner as Winner | null | undefined) ?? null;
  const waiting = winner !== null && r?.promotion === "";
  const [asking, setAsking] = useState<"promote" | "decline" | null>(null);
  const [deciding, setDeciding] = useState(false);
  const sent = useRef(false);

  const decide = async (promote: boolean) => {
    setAsking(null);
    if (sent.current) return;
    sent.current = true;
    setDeciding(true);
    p.setNotice(null);
    try {
      const out = await call("cycle.promote", { promote }, true);
      if (out.summary) p.setNotice({ kind: "info", text: out.summary });
    } catch (e) {
      p.setNotice({ kind: "error", text: errorText(e) });
      sent.current = false;
    }
    await p.reload();
    setDeciding(false);
  };

  return (
    <>
      {!waiting && <RunControls st={st} job={job} stale={stale} busy={busy} label="Run training" onRun={() => p.runStep("cycle.train")} />}
      <JobBox job={job} stepError={st?.error} />
      {r && st?.status !== "failed" && (
        <div class="cycle-result">
          {typeof r.skip_reason === "string" && r.skip_reason && <p class="notice notice-info">Skipped: {r.skip_reason}</p>}
          {typeof r.candidates === "number" && <p>{`${r.candidates} candidate${r.candidates === 1 ? "" : "s"} trained.`}</p>}
          {winner ? (
            <div class="fact cycle-winner">
              <p class="rule-outcome-head">
                Winner <strong>{winner.version}</strong> <span class="muted">{winner.algorithm}</span>
              </p>
              <p class="muted">
                {`precision ${metric(winner.precision)} · recall ${metric(winner.recall)} · F1 ${metric(winner.f1)} · PR-AUC ${metric(winner.prauc)}`}
              </p>
              <p class="muted">
                <code>{winner.dir}</code>
              </p>
            </div>
          ) : (
            r.no_winner === true && <p>No candidate passed the gates: there is nothing to promote.</p>
          )}
          {r.promotion === "promoted" && (
            <p class="notice notice-info">{`The winner was promoted to ${String(r.promoted_to ?? "the model path")}.`}</p>
          )}
          {r.promotion === "declined" && <p class="notice notice-info">The winner was declined; the current model stays.</p>}
        </div>
      )}
      {waiting && st?.status !== "done" && (
        <div class="rules-zone-bar">
          <button type="button" class="btn danger" disabled={deciding} onClick={() => setAsking("promote")}>
            Promote
          </button>
          <button type="button" class="btn" disabled={deciding} onClick={() => setAsking("decline")}>
            Don't promote
          </button>
        </div>
      )}
      {asking === "promote" && <DestructiveConfirm name="cycle.promote" onConfirm={() => decide(true)} onCancel={() => setAsking(null)} />}
      {asking === "decline" && <DeclineDialog onConfirm={() => decide(false)} onCancel={() => setAsking(null)} />}
    </>
  );
}

/** DeclineDialog asks before declining the training winner. */
function DeclineDialog({ onConfirm, onCancel }: { onConfirm(): void; onCancel(): void }) {
  const root = useRef<HTMLDivElement>(null);
  const cancel = useRef<HTMLButtonElement>(null);
  useShortcutIsolation(root, cancel, onCancel);
  useEffect(() => cancel.current?.focus(), []);
  return (
    <div class="overlay" role="presentation">
      <div
        class="dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby="decline-title"
        ref={root}
        onKeyDown={(e) => {
          e.stopPropagation();
          if (e.key === "Escape") onCancel();
        }}
      >
        <h2 id="decline-title">Decline the winner?</h2>
        <p>The current model stays in place. The decision is final for this cycle.</p>
        <div class="row end">
          <button type="button" class="btn" ref={cancel} onClick={onCancel}>
            Cancel
          </button>
          <button type="button" class="btn danger" onClick={onConfirm}>
            Don't promote
          </button>
        </div>
      </div>
    </div>
  );
}

/** SummaryStep shows the cycle's report; the deploy stays in the terminal. */
function SummaryStep() {
  const [summary, setSummary] = useState<{ markdown: string; deploy: string } | null>(null);
  const [error, setError] = useState("");
  const load = async () => {
    setError("");
    try {
      const o = (await call("cycle.summary")).object as { markdown?: string; deploy?: string } | undefined;
      setSummary({ markdown: o?.markdown ?? "", deploy: o?.deploy ?? "" });
    } catch (e) {
      setError("Could not load the summary: " + errorText(e));
    }
  };
  useEffect(() => {
    load();
  }, []);
  return (
    <>
      <p class="notice notice-info cycle-deploy" role="note">
        <strong>Deploy from the terminal.</strong> The panel does not deploy
        {summary?.deploy ? <>: {summary.deploy}.</> : "."}
      </p>
      {error && (
        <p class="error" role="alert">
          {error}{" "}
          <button type="button" class="btn" onClick={() => load()}>
            Retry
          </button>
        </p>
      )}
      {summary === null && !error && <p class="muted">Loading…</p>}
      {summary && <pre class="cycle-report">{summary.markdown}</pre>}
    </>
  );
}
