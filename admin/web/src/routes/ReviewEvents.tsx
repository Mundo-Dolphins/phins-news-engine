import { useCallback, useEffect, useRef, useState } from "preact/hooks";
import { ApiError, runAction, type Job, type Result } from "../api";
import { ReviewScreen, type Notice, type ReviewDecision } from "../components/ReviewScreen";
import { LocalOnly } from "../components/LocalOnly";
import { ImpactView, PhraseEditor, Verdict, type Assessment } from "../components/PhraseEditor";
import { errorText, untilJobEnd, useShortcutIsolation, when } from "../components/reviewParts";
import { safeHref } from "../links";

/** RuleProposal is one pending proposal of `events.rules_queue`. */
export interface RuleProposal extends Assessment {
  id: string;
  /** "action" or "signal". */
  target_kind: string;
  /** The event type (action) or signal the phrase is for. */
  target: string;
  prompt_version: string;
  /** Cited articles; url is "" when the article is no longer in the operational database. */
  evidence: { article_id: string; title: string; url: string }[];
}

/** RuleRef is a proposal listed without its assessment (no evidence, approved). */
export interface RuleRef {
  id: string;
  target_kind: string;
  target: string;
  phrase: string;
  decided_at?: string;
}

interface Queue {
  sampled: number;
  items: RuleProposal[];
  no_evidence: RuleRef[];
  approved: RuleRef[];
  /** Set when nothing is reviewed, e.g. "no generic articles in the sample". */
  notice?: string;
}

interface Metrics {
  precision: number;
  recall: number;
  purity: number;
  over_grouped: number;
  fragmented: number;
}

/** ApplyResult is the result object of `events.rules_apply`. */
interface ApplyResult {
  nothing: boolean;
  blocked: boolean;
  reason: string;
  applied: RuleRef[];
  generated_rules: string;
  before?: Metrics;
  after?: Metrics;
  dataset?: string;
  affected?: number;
}

/** JobState is a long-running action started from this page. */
type JobState =
  | { kind: "idle" }
  | { kind: "running"; id: string }
  | { kind: "done"; result: Result }
  | { kind: "error"; text: string };

const keyOf = (t: { id: string }) => t.id;
const moves = (t: RuleProposal) => t.impact.moved.reduce((n, m) => n + m.count, 0);
/** gone matches the errors for a proposal that is no longer pending. */
const gone = (e: unknown) =>
  e instanceof ApiError && (e.code === "not_found" || (e.code === "conflict" && /not pending|already decided/.test(e.message)));

function Evidence({ evidence }: { evidence: RuleProposal["evidence"] }) {
  if (evidence.length === 0) return <p class="muted">No evidence articles.</p>;
  const primary = evidence.findIndex((e) => safeHref(e.url) !== null);
  return (
    <ul class="entity-links">
      {evidence.map((e, i) => {
        const href = safeHref(e.url);
        const title = e.title || e.article_id;
        return (
          <li key={e.article_id + "\u0000" + i}>
            {href ? (
              <a href={href} target="_blank" rel="noopener noreferrer" data-review-open={i === primary ? true : undefined}>
                {title}
              </a>
            ) : (
              <span>{title}</span>
            )}{" "}
            {!e.url && <span class="muted">· article no longer stored</span>}
          </li>
        );
      })}
    </ul>
  );
}

function Target({ t }: { t: Pick<RuleRef, "target_kind" | "target"> }) {
  return (
    <span class="rule-target">
      <span class={"badge rule-kind rule-kind-" + t.target_kind}>{t.target_kind}</span> {t.target}
    </span>
  );
}

function RuleItem({ t, sampled }: { t: RuleProposal; sampled: number }) {
  return (
    <article class="review-article rule-card">
      <header>
        <h2>“{t.phrase}”</h2>
        <p class="review-meta">
          <Target t={t} />
          {t.prompt_version && <span>prompt {t.prompt_version}</span>}
        </p>
      </header>
      <Verdict a={t} />
      <h3>Impact on the sample</h3>
      <ImpactView impact={t.impact} sampled={sampled} />
      <h3>Evidence ({t.evidence.length})</h3>
      <Evidence evidence={t.evidence} />
      <p class="muted review-ids">
        proposal <code>{t.id}</code>
      </p>
    </article>
  );
}

function RuleEntry({ t }: { t: RuleProposal }) {
  return (
    <>
      <span class="entry-title">“{t.phrase}”</span>
      <span class="entry-meta">
        {t.target_kind} {t.target} · {moves(t)} moved
        {!t.passes && <span class="badge rule-badge-fail">does not pass</span>}
      </span>
    </>
  );
}

/** ConfirmRejectFailing lists the failing proposals and asks before rejecting them all. */
function ConfirmRejectFailing({ items, onConfirm, onCancel }: { items: RuleProposal[]; onConfirm(ids: string[]): void; onCancel(): void }) {
  const dialog = useRef<HTMLDivElement>(null);
  const cancel = useRef<HTMLButtonElement>(null);
  const [sent, setSent] = useState(false);
  useShortcutIsolation(dialog, cancel, onCancel);
  useEffect(() => cancel.current?.focus(), []);
  const onKeyDown = (e: KeyboardEvent) => {
    e.stopPropagation();
    if (e.key === "Escape") onCancel();
  };
  return (
    <div class="overlay" role="presentation">
      <div class="dialog" role="dialog" aria-modal="true" aria-labelledby="reject-failing-title" ref={dialog} onKeyDown={onKeyDown}>
        <h2 id="reject-failing-title">Reject all failing proposals?</h2>
        <p>
          These {items.length} proposal{items.length === 1 ? "" : "s"} do not pass the checks. Only those still pending
          and still failing when the server checks again are rejected.
        </p>
        <ul class="rule-reject-list">
          {items.map((t) => (
            <li key={t.id}>
              “{t.phrase}” <span class="muted">({t.reasons.join("; ")})</span>
            </li>
          ))}
        </ul>
        <div class="row end">
          <button type="button" class="btn" ref={cancel} onClick={onCancel}>
            Cancel
          </button>
          <button
            type="button"
            class="btn danger"
            disabled={sent}
            onClick={() => {
              if (sent) return;
              setSent(true);
              onConfirm(items.map((t) => t.id));
            }}
          >
            Reject {items.length}
          </button>
        </div>
      </div>
    </div>
  );
}

const num = (v: number) => (typeof v === "number" ? v.toFixed(3) : "");

function MetricsTable({ before, after }: { before: Metrics; after: Metrics }) {
  const rows: [string, (m: Metrics) => string][] = [
    ["Precision", (m) => num(m.precision)],
    ["Recall", (m) => num(m.recall)],
    ["Purity", (m) => num(m.purity)],
    ["Over-grouped clusters", (m) => String(m.over_grouped)],
    ["Fragmented clusters", (m) => String(m.fragmented)],
  ];
  return (
    <div class="table-wrap">
      <table class="rule-metrics">
        <thead>
          <tr>
            <th>Metric</th>
            <th>Before</th>
            <th>After</th>
          </tr>
        </thead>
        <tbody>
          {rows.map(([label, f]) => (
            <tr key={label}>
              <td>{label}</td>
              <td>{f(before)}</td>
              <td>{f(after)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function ApplyOutcome({ result }: { result: Result }) {
  const r = result.object as ApplyResult | undefined;
  // The editorial cycle's apply returns the step counters, not the gate's detail: show its summary.
  if (r && !Array.isArray(r.applied)) {
    const blocked = (result.summary ?? "").startsWith("BLOCKED");
    return (
      <p class={"notice " + (blocked ? "notice-error" : "notice-info")} role="status">
        {result.summary}
      </p>
    );
  }
  if (!r) return <p>{result.summary}</p>;
  if (r.nothing) return <p class="notice notice-info">Nothing to apply.</p>;
  return (
    <div class={"rule-outcome " + (r.blocked ? "rule-blocked" : "rule-passed")} role="status">
      <p class="rule-outcome-head">
        <span class={"badge " + (r.blocked ? "rule-badge-fail" : "rule-badge-pass")}>{r.blocked ? "BLOCKED" : "PASS"}</span>{" "}
        {r.blocked ? (
          <span>{r.reason}</span>
        ) : (
          <span>
            {r.applied.length} rule{r.applied.length === 1 ? "" : "s"} written to <code>{r.generated_rules}</code>
          </span>
        )}
      </p>
      {r.blocked && <p class="muted">The rules stay approved; reject the ones to drop and run the gate again.</p>}
      {!r.blocked && r.applied.length > 0 && (
        <ul class="rule-applied">
          {r.applied.map((a) => (
            <li key={a.id}>
              “{a.phrase}” <Target t={a} />
            </li>
          ))}
        </ul>
      )}
      {r.dataset && <p class="muted">{r.dataset}</p>}
      {r.before && r.after && <MetricsTable before={r.before} after={r.after} />}
    </div>
  );
}

function JobLine({ state, idle }: { state: JobState; idle?: string }) {
  if (state.kind === "running") {
    return (
      <p class="muted" role="status">
        Running as job <a href={"/jobs/" + state.id}>{state.id}</a>…
      </p>
    );
  }
  if (state.kind === "error") {
    return (
      <p class="notice notice-error" role="alert">
        {state.text}
      </p>
    );
  }
  return idle ? <p class="muted">{idle}</p> : null;
}

/** ReviewEvents is the web form of `events review`: discover, review and apply event rule phrases. */
export function ReviewEvents() {
  return (
    <LocalOnly>
      <ReviewEventsScreen />
    </LocalOnly>
  );
}

/** Editing is the open phrase editor: the proposal and how its E decision settles. */
interface Editing {
  t: RuleProposal;
  settle(outcome: Notice | void): void;
}

/** ReviewEventsProps configures the event rule review screen. */
export interface ReviewEventsProps {
  /** discoverAction is the long-running action Discover runs (default `events.rules_discover`). */
  discoverAction?: string;
  /** applyAction is the long-running action Apply runs (default `events.rules_apply`). */
  applyAction?: string;
  /** onJobStart is called when Discover or Apply starts. */
  onJobStart?(): void;
  /** onJobEnd is called once that run has ended, whatever its outcome. */
  onJobEnd?(): void;
}

/**
 * ReviewEventsScreen is the event rule review without the local-mode guard,
 * for embedding; the editorial cycle passes its own discover and apply
 * actions so the step counts them.
 */
export function ReviewEventsScreen({
  discoverAction = "events.rules_discover",
  applyAction = "events.rules_apply",
  onJobStart,
  onJobEnd,
}: ReviewEventsProps = {}) {
  const jobHooks = useRef({ onJobStart, onJobEnd });
  jobHooks.current = { onJobStart, onJobEnd };
  const [queue, setQueue] = useState<Queue | null>(null);
  const [loadError, setLoadError] = useState<string>("");
  // Decided and skipped proposals stay hidden for this page session, also
  // across refreshes that raced a decision; approved rows rejected here too.
  const [hidden, setHidden] = useState<Set<string>>(() => new Set());
  const [hiddenApproved, setHiddenApproved] = useState<Set<string>>(() => new Set());
  const [reviewNotice, setReviewNotice] = useState<Notice | undefined>();
  const [editing, setEditing] = useState<Editing | null>(null);
  const [confirming, setConfirming] = useState(false);
  const [discover, setDiscover] = useState<JobState>({ kind: "idle" });
  const [apply, setApply] = useState<JobState>({ kind: "idle" });
  const [applyNotice, setApplyNotice] = useState<Notice | undefined>();
  const streams = useRef<Set<() => void>>(new Set());

  useEffect(() => () => streams.current.forEach((close) => close()), []);

  const load = useCallback(async () => {
    try {
      const out = await runAction("events.rules_queue", {}, false);
      if ("job" in out) throw new Error("unexpected job for events.rules_queue");
      const obj = out.result.object as Partial<Queue> | undefined;
      setQueue({
        sampled: obj?.sampled ?? 0,
        items: obj?.items ?? [],
        no_evidence: obj?.no_evidence ?? [],
        approved: obj?.approved ?? [],
        notice: obj?.notice || undefined,
      });
      setLoadError("");
      setReviewNotice(undefined);
    } catch (e) {
      setLoadError("Could not load the event rule review: " + errorText(e));
    }
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  const untilEnd = (job: Job) => untilJobEnd(job, streams.current);

  /** startJob runs a long-running action and reports its end to set. */
  const startJob = async (name: string, set: (s: JobState) => void) => {
    jobHooks.current.onJobStart?.();
    try {
      const out = await runAction(name, {}, false);
      let result: Result | undefined;
      if ("job" in out) {
        set({ kind: "running", id: out.job.id });
        const j = await untilEnd(out.job);
        if (j.status !== "succeeded" || !j.result) {
          set({ kind: "error", text: `Job ${j.id} ${j.status}${j.error ? ": " + j.error : ""}` });
          return;
        }
        result = j.result;
      } else {
        result = out.result;
      }
      set({ kind: "done", result });
    } catch (e) {
      set({ kind: "error", text: errorText(e) });
    } finally {
      load();
      jobHooks.current.onJobEnd?.();
    }
  };

  const busyJob = discover.kind === "running" || apply.kind === "running";
  // A decision taken while the gate runs could change what it writes.
  const applying = apply.kind === "running";

  const hide = (t: { id: string }) => setHidden((h) => new Set(h).add(keyOf(t)));

  /** addApproved lists an accepted proposal in the apply zone without reloading the queue. */
  const addApproved = (t: Pick<RuleRef, "id" | "target_kind" | "target" | "phrase">) =>
    setQueue((q) =>
      q && !q.approved.some((r) => r.id === t.id)
        ? {
            ...q,
            approved: [
              ...q.approved,
              { id: t.id, target_kind: t.target_kind, target: t.target, phrase: t.phrase, decided_at: new Date().toISOString() },
            ],
          }
        : q,
    );

  /** decide posts one decision; the item leaves the queue unless the server refused it. */
  const decide = async (t: RuleProposal, decision: "accept" | "reject"): Promise<Notice | void> => {
    setReviewNotice(undefined);
    try {
      const out = await runAction("events.rules_decide", { id: t.id, decision }, false);
      hide(t);
      if (decision === "accept") addApproved(t);
      if ("job" in out) return { kind: "info", text: `“${t.phrase}”: started as job ${out.job.id}.` };
      return { kind: "info", text: out.result.summary || `“${t.phrase}”: ${decision} done` };
    } catch (e) {
      if (gone(e)) {
        hide(t);
        return { kind: "info", text: `“${t.phrase}” (${t.id}) is no longer pending: ${(e as Error).message}. Removed from the queue.` };
      }
      // A refused accept (it fails the checks now): reload its verdict.
      load();
      throw new Error(errorText(e));
    }
  };

  const edit = async (t: RuleProposal, phrase: string, preview: boolean) => {
    const out = await runAction("events.rules_decide", { id: t.id, decision: "edit", phrase, preview }, false);
    if ("job" in out) throw new Error("unexpected job for events.rules_decide");
    return out.result;
  };

  const decisions: ReviewDecision<RuleProposal>[] = [
    { key: "a", label: "Accept", offered: (t) => t.passes, onDecide: (t) => decide(t, "accept") },
    { key: "r", label: "Reject", onDecide: (t) => decide(t, "reject") },
    {
      key: "e",
      label: "Edit phrase",
      // Settles when the editor closes (no decision) or the edit is saved.
      onDecide: (t) => {
        setReviewNotice(undefined);
        return new Promise((resolve) => setEditing({ t, settle: resolve }));
      },
    },
  ];

  const closeEditor = () => {
    if (!editing) return;
    setEditing(null);
    editing.settle();
  };
  const saveEdit = async (phrase: string) => {
    if (!editing) return;
    const result = await edit(editing.t, phrase, false);
    hide(editing.t);
    const saved = result.object as Partial<RuleRef> | undefined;
    if (saved?.id) addApproved({ id: saved.id, target_kind: editing.t.target_kind, target: editing.t.target, phrase: saved.phrase ?? phrase });
    setEditing(null);
    editing.settle({ kind: "info", text: result.summary || `“${editing.t.phrase}” replaced by “${phrase}”.` });
  };

  const visible = (queue?.items ?? []).filter((t) => !hidden.has(keyOf(t)));
  const failingItems = visible.filter((t) => !t.passes);
  const approved = (queue?.approved ?? []).filter((t) => !hiddenApproved.has(keyOf(t)));

  const rejectFailing = async (ids: string[]) => {
    try {
      const out = await runAction("events.rules_reject_failing", { ids }, false);
      if ("job" in out) throw new Error("unexpected job for events.rules_reject_failing");
      const rejected = (out.result.object as { rejected?: RuleRef[] } | undefined)?.rejected ?? [];
      setHidden((h) => {
        const next = new Set(h);
        rejected.forEach((r) => next.add(r.id));
        return next;
      });
      const phrases = rejected.map((r) => `“${r.phrase}”`).join(", ");
      setReviewNotice({ kind: "info", text: (out.result.summary || `${rejected.length} rejected.`) + (phrases ? " " + phrases : "") });
    } catch (e) {
      setReviewNotice({ kind: "error", text: "Could not reject the failing proposals: " + errorText(e) });
    } finally {
      setConfirming(false);
    }
  };

  const rejectApproved = async (r: RuleRef) => {
    setApplyNotice(undefined);
    try {
      const out = await runAction("events.rules_decide", { id: r.id, decision: "reject", from: "approved" }, false);
      setHiddenApproved((h) => new Set(h).add(r.id));
      if (!("job" in out)) setApplyNotice({ kind: "info", text: out.result.summary || `“${r.phrase}” rejected.` });
    } catch (e) {
      if (gone(e)) {
        setHiddenApproved((h) => new Set(h).add(r.id));
        setApplyNotice({ kind: "info", text: `“${r.phrase}” is no longer approved: ${(e as Error).message}.` });
      } else {
        setApplyNotice({ kind: "error", text: errorText(e) });
      }
    }
  };

  const discoverSummary = discover.kind === "done" ? discover.result.summary ?? "" : "";

  return (
    <>
      <h1>Event rule review</h1>
      <p class="muted">
        The web form of <code>events review</code>: discover event rule phrases with the LLM, review them against the
        same sample as the CLI, then gate and apply the approved ones. A proposal that fails the checks is flagged, never
        rejected for you.
      </p>

      <section class="rules-zone" aria-labelledby="zone-discover">
        <h2 id="zone-discover">
          <span class="zone-step">1</span> Discover
        </h2>
        <div class="rules-zone-bar">
          <button
            type="button"
            class="btn"
            disabled={busyJob}
            onClick={() => {
              setDiscover({ kind: "idle" });
              startJob(discoverAction, setDiscover);
            }}
          >
            {discover.kind === "running" ? "Discovering…" : "Discover phrases"}
          </button>
          <span class="muted">
            Asks the offline-training LLM for phrases over recent generic articles. Optional: the queue below shows what
            is already pending.
          </span>
        </div>
        <JobLine state={discover} />
        {discoverSummary && (
          <p class={"notice " + (discoverSummary.startsWith("WARNING:") ? "notice-error" : "notice-info")} role="status">
            {discoverSummary}
          </p>
        )}
      </section>

      <section class="rules-zone" aria-labelledby="zone-review">
        <h2 id="zone-review">
          <span class="zone-step">2</span> Review
          {queue && <span class="muted zone-count"> · sample of {queue.sampled} articles</span>}
        </h2>
        {failingItems.length > 0 && (
          <div class="rules-zone-bar">
            <button type="button" class="btn danger" disabled={editing !== null || applying} onClick={() => setConfirming(true)}>
              Reject all failing ({failingItems.length})
            </button>
            <span class="muted">Failing proposals are flagged “does not pass” and offer no Accept.</span>
          </div>
        )}
        <PhraseEditor
          open={editing !== null}
          proposal={editing ? { id: editing.t.id, phrase: editing.t.phrase } : { id: "", phrase: "" }}
          onPreview={async (phrase) => (await edit(editing!.t, phrase, true)).object as Assessment}
          onSave={saveEdit}
          onClose={closeEditor}
        />
        {queue === null ? (
          loadError ? (
            <p class="error" role="alert">
              {loadError}{" "}
              <button type="button" class="btn" onClick={() => load()}>
                Retry
              </button>
            </p>
          ) : (
            <p class="muted">Loading…</p>
          )
        ) : (
          <ReviewScreen<RuleProposal>
            items={visible}
            itemKey={keyOf}
            renderItem={(t) => <RuleItem t={t} sampled={queue.sampled} />}
            renderListEntry={(t) => <RuleEntry t={t} />}
            decisions={decisions}
            onSkip={hide}
            onRefresh={load}
            notice={loadError ? { kind: "error", text: loadError } : reviewNotice}
            decisionsDisabled={applying}
            emptyText={
              queue.notice
                ? `Nothing to review: ${queue.notice}.`
                : "Nothing to review: no event rule proposal with evidence in the sample is pending."
            }
          />
        )}
        {queue && queue.no_evidence.length > 0 && (
          <details class="rules-no-evidence">
            <summary>
              {queue.no_evidence.length} proposal{queue.no_evidence.length === 1 ? "" : "s"} without evidence in the
              sample (left pending)
            </summary>
            <ul>
              {queue.no_evidence.map((r) => (
                <li key={r.id}>
                  “{r.phrase}” <Target t={r} />
                </li>
              ))}
            </ul>
          </details>
        )}
      </section>

      <section class="rules-zone" aria-labelledby="zone-apply">
        <h2 id="zone-apply">
          <span class="zone-step">3</span> Apply
        </h2>
        <p class="muted">
          Approved rules not yet applied, from this and earlier reviews. The gate scores the generated rules plus all of
          them; reject the ones you do not want first.
        </p>
        {applyNotice && (
          <p class={"notice notice-" + applyNotice.kind} role={applyNotice.kind === "error" ? "alert" : "status"}>
            {applyNotice.text}
          </p>
        )}
        {queue &&
          (approved.length === 0 ? (
            <p class="muted">No approved rule waiting.</p>
          ) : (
            <ul class="rules-approved">
              {approved.map((r) => (
                <li key={r.id}>
                  <span class="rules-approved-phrase">“{r.phrase}”</span> <Target t={r} />
                  {r.decided_at && <span class="muted"> · approved {when(r.decided_at)}</span>}
                  <span class="spacer" />
                  <button type="button" class="btn" disabled={applying} onClick={() => rejectApproved(r)}>
                    Reject
                  </button>
                </li>
              ))}
            </ul>
          ))}
        <div class="rules-zone-bar">
          <button
            type="button"
            class="btn primary"
            disabled={busyJob}
            onClick={() => {
              setApply({ kind: "idle" });
              setApplyNotice(undefined);
              startJob(applyAction, setApply);
            }}
          >
            Run gate and apply
          </button>
        </div>
        <JobLine state={apply} />
        {apply.kind === "done" && <ApplyOutcome result={apply.result} />}
      </section>

      {confirming && (
        <ConfirmRejectFailing items={failingItems} onConfirm={rejectFailing} onCancel={() => setConfirming(false)} />
      )}
    </>
  );
}
