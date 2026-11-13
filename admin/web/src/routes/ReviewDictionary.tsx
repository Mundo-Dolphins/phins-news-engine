import { useCallback, useEffect, useRef, useState } from "preact/hooks";
import { listActions, runAction, type Result } from "../api";
import { DestructiveConfirm } from "../components/ConfirmDialog";
import { LocalOnly } from "../components/LocalOnly";
import { ProposalEditor } from "../components/ProposalEditor";
import { ReviewScreen, type Notice, type ReviewDecision } from "../components/ReviewScreen";
import { errorText, untilJobEnd, useShortcutIsolation, when } from "../components/reviewParts";
import { safeHref } from "../links";

// The dictionary update review runs on actions the host registers; the
// engine has none. Their presence in the catalog turns the screen on.
const SESSION = "dictionary.update_session";
const START = "dictionary.update_start";
const DECIDE = "dictionary.update_decide";
const DECIDE_TYPE = "dictionary.update_decide_type";
const PREVIEW = "dictionary.update_preview";
const APPLY = "dictionary.update_apply";
const DISCARD = "dictionary.update_discard";

/** hostHasDictionaryUpdate reports whether the host registered the dictionary update review actions. */
export async function hostHasDictionaryUpdate(): Promise<boolean> {
  return (await listActions()).some((a) => a.name === SESSION);
}

interface SessionInfo {
  id: string;
  status: string;
  started_at: string;
  snapshot_refs: string[] | null;
  dictionary_version: string;
}

interface CurrentEntry {
  canonical_name: string;
  aliases: string[] | null;
  type: string;
  roster_status: string;
  role: string;
  position: string;
  number: string;
  source: string;
  confidence: number;
  notes: string;
}

interface Observed {
  display_name: string;
  canonical_key: string;
  kind: string;
  role: string;
  position: string;
  number: string;
  department: string;
  profile_url: string;
  aliases: { value: string; class: string }[] | null;
  roster_status: string;
  source: string;
  observed_at: string;
}

interface Decision {
  action: "accept" | "reject" | "skip" | "edit";
  decided_at: string;
  edited?: { category: string; detail: string };
}

/** Proposal is one proposal of `dictionary.update_session`. */
interface Proposal {
  index: number;
  kind: string;
  canonical_key: string;
  category: string;
  detail: string;
  conflict_with: string[] | null;
  current_entry: CurrentEntry | null;
  observed: Observed | null;
  alias: { value: string; class: string } | null;
  decision: Decision | null;
}

interface SessionView {
  session: SessionInfo | null;
  proposals: Proposal[];
  undecided: number;
  /** stale is set when the session can no longer be continued (its dictionary changed); stale_reason says why. */
  stale: boolean;
  stale_reason: string;
}

interface Regression {
  total_cases: number;
  flip_count: number;
  flips: { case_name: string; direction: string; score_before: number; score_after: number; rule_before: string; rule_after: string }[] | null;
}

interface Preview {
  undecided: number;
  changes: number;
  diff: string;
  regression: Regression | null;
  /** regression_error says why the regression check could not run, when regression is null. */
  regression_error?: string;
}

interface Applied {
  summary: string;
  from: string;
  to: string;
  changes: number;
  unchanged: boolean;
  diff: string;
  regression: Regression | null;
  regression_error?: string;
}

type JobState = { kind: "idle" } | { kind: "running"; id: string } | { kind: "done"; summary: string } | { kind: "error"; text: string };

/** Pending is the confirmation dialog on screen, if any. */
type Pending = null | { kind: "apply" } | { kind: "discard"; id: string } | { kind: "type"; t: Proposal; accept: boolean };

const keyOf = (p: Proposal) => String(p.index);
const plural = (n: number, word: string) => `${n} ${word}${n === 1 ? "" : "s"}`;
const kindLabel = (kind: string) => kind.replace(/_/g, " ");
const titleOf = (p: Proposal) => p.observed?.display_name || p.current_entry?.canonical_name || p.canonical_key;

const DECIDED: Record<Decision["action"], string> = { accept: "accepted", reject: "rejected", skip: "skipped", edit: "edited" };

/** sameKindFrom lists the proposal and every later one of its kind: what decide_type covers. */
const sameKindFrom = (all: Proposal[], t: Proposal) => all.filter((p) => p.index >= t.index && p.kind === t.kind);

function resultObject<T>(name: string, out: Awaited<ReturnType<typeof runAction>>): { object: T; summary: string } {
  if ("job" in out) throw new Error(`unexpected job for ${name}`);
  return { object: out.result.object as T, summary: out.result.summary ?? "" };
}

/** Value shows a field value, linked when it is a safe http(s) URL. */
function Value({ text, open }: { text: string; open?: boolean }) {
  if (!text) return <span class="muted">—</span>;
  const href = safeHref(text);
  if (!href) return <span>{text}</span>;
  return (
    <a href={href} target="_blank" rel="noopener noreferrer" data-review-open={open ? true : undefined}>
      {text}
    </a>
  );
}

/** Comparison puts the dictionary entry and the source observation side by side. */
function Comparison({ p }: { p: Proposal }) {
  const c = p.current_entry;
  const o = p.observed;
  // [label, current, observed, compared, the observed value opens with O]
  const rows: [string, string, string, boolean, boolean?][] = [
    ["Name", c?.canonical_name ?? "", o?.display_name ?? "", true],
    ["Type", c?.type ?? "", o?.kind ?? "", false],
    ["Role", c?.role ?? "", o?.role ?? "", true],
    ["Position", c?.position ?? "", o?.position ?? "", true],
    ["Number", c?.number ?? "", o?.number ?? "", true],
    ["Department", "", o?.department ?? "", false],
    ["Roster status", c?.roster_status ?? "", o?.roster_status ?? "", true],
    ["Aliases", (c?.aliases ?? []).join(", "), (o?.aliases ?? []).map((a) => `${a.value} (${a.class})`).join(", "), false],
    ["Source", c?.source ?? "", o?.source ?? "", false],
    ["Confidence", c ? String(c.confidence) : "", "", false],
    ["Notes", c?.notes ?? "", "", false],
    ["Profile", "", o?.profile_url ?? "", false, true],
    ["Observed at", "", o?.observed_at ? when(o.observed_at) : "", false],
  ];
  const shown = rows.filter(([, cur, obs]) => cur || obs);
  return (
    <>
      {!c && <p class="muted">Not in the dictionary yet.</p>}
      {!o && <p class="muted">Not observed at the official sources.</p>}
      <div class="table-wrap">
        <table class="dict-compare">
          <thead>
            <tr>
              <th>Field</th>
              <th>In the dictionary</th>
              <th>Observed at the source</th>
            </tr>
          </thead>
          <tbody>
            {shown.map(([label, cur, obs, compared, open]) => (
              <tr key={label} class={compared && c && o && cur !== obs ? "dict-changed" : undefined}>
                <th scope="row">{label}</th>
                <td>
                  <Value text={cur} />
                </td>
                <td>
                  <Value text={obs} open={open} />
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </>
  );
}

function DecisionLine({ d }: { d: Decision }) {
  return (
    <div class={"dict-decision dict-decision-" + d.action}>
      <p>{`Decided: ${DECIDED[d.action] ?? d.action}${d.decided_at ? " · " + when(d.decided_at) : ""}`} — press a key to decide again.</p>
      {d.edited && (
        <dl class="dict-edited">
          <dt>Category</dt>
          <dd>{d.edited.category}</dd>
          <dt>Detail</dt>
          <dd>{d.edited.detail}</dd>
        </dl>
      )}
    </div>
  );
}

function ProposalItem({ p, total, sameKind, disabled, onType }: { p: Proposal; total: number; sameKind: number; disabled: boolean; onType(accept: boolean): void }) {
  return (
    <article class="review-article dict-card">
      <header>
        <h2>
          {titleOf(p)} <span class="muted dict-key">{p.canonical_key}</span>
        </h2>
        <p class="review-meta">
          <span class="badge dict-kind">{kindLabel(p.kind)}</span>
          <span>category {p.category || "—"}</span>
          <span>
            proposal {p.index + 1} of {total}
          </span>
        </p>
      </header>
      {p.detail && <p class="dict-detail">{p.detail}</p>}
      {p.alias && (
        <p>
          Alias <strong>{p.alias.value}</strong> <span class="muted">({p.alias.class})</span>
        </p>
      )}
      {p.conflict_with && p.conflict_with.length > 0 && (
        <p class="dict-conflict" role="note">
          Conflicts with: {p.conflict_with.join(", ")}
        </p>
      )}
      <Comparison p={p} />
      {p.decision && <DecisionLine d={p.decision} />}
      <div class="rules-zone-bar dict-type-bar">
        <button type="button" class="btn" disabled={disabled} onClick={() => onType(true)}>
          Accept all of this type
        </button>
        <button type="button" class="btn" disabled={disabled} onClick={() => onType(false)}>
          Reject all of this type
        </button>
        <span class="muted">
          {plural(sameKind, "proposal")} of this kind from here on
        </span>
      </div>
    </article>
  );
}

function ProposalEntry({ p }: { p: Proposal }) {
  return (
    <>
      <span class="entry-title">{titleOf(p)}</span>
      <span class="entry-meta">
        #{p.index + 1} · {kindLabel(p.kind)}
        {p.decision && <span class={"badge dict-decided dict-decision-" + p.decision.action}>{DECIDED[p.decision.action] ?? p.decision.action}</span>}
      </span>
    </>
  );
}

/** ConfirmType asks before accepting or rejecting the proposal and every later one of its kind. */
function ConfirmType({ t, count, accept, onConfirm, onCancel }: { t: Proposal; count: number; accept: boolean; onConfirm(): void; onCancel(): void }) {
  const dialog = useRef<HTMLDivElement>(null);
  const cancel = useRef<HTMLButtonElement>(null);
  const [sent, setSent] = useState(false);
  useShortcutIsolation(dialog, cancel, onCancel);
  useEffect(() => cancel.current?.focus(), []);
  const onKeyDown = (e: KeyboardEvent) => {
    e.stopPropagation();
    if (e.key === "Escape") onCancel();
  };
  const verb = accept ? "Accept" : "Reject";
  return (
    <div class="overlay" role="presentation">
      <div class="dialog" role="dialog" aria-modal="true" aria-labelledby="type-title" ref={dialog} onKeyDown={onKeyDown}>
        <h2 id="type-title">{verb} all of this type?</h2>
        <p>
          {`This proposal and every later proposal of kind “${kindLabel(t.kind)}” — ${plural(count, "proposal")} — will be ${accept ? "accepted" : "rejected"}, replacing any decision they already have. Earlier proposals and other kinds are left alone.`}
        </p>
        <div class="row end">
          <button type="button" class="btn" ref={cancel} onClick={onCancel}>
            Cancel
          </button>
          <button
            type="button"
            class={"btn " + (accept ? "primary" : "danger")}
            disabled={sent}
            onClick={() => {
              if (sent) return;
              setSent(true);
              onConfirm();
            }}
          >
            {verb} {count}
          </button>
        </div>
      </div>
    </div>
  );
}

function RegressionView({ r, error }: { r: Regression | null; error?: string }) {
  if (!r) {
    if (error) {
      return (
        <p class="notice notice-error" role="status">
          The regression check could not run: {error}
        </p>
      );
    }
    return <p class="muted">No regression check result.</p>;
  }
  const flips = r.flips ?? [];
  return (
    <div class="dict-regression">
      <p>{`${r.flip_count} of ${r.total_cases} regression cases flip.`}</p>
      {flips.length > 0 && (
        <ul>
          {flips.map((f, i) => (
            <li key={f.case_name + "\u0000" + i}>
              {`${f.case_name}: ${f.direction}, score ${f.score_before} → ${f.score_after}, rule ${f.rule_before || "—"} → ${f.rule_after || "—"}`}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

function Diff({ diff }: { diff: string }) {
  if (!diff) return null;
  return <pre class="dict-diff">{diff}</pre>;
}

function JobLine({ state }: { state: JobState }) {
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
  if (state.kind === "done" && state.summary) {
    return (
      <p class="notice notice-info" role="status">
        {state.summary}
      </p>
    );
  }
  return null;
}

function NoticeLine({ notice }: { notice?: Notice }) {
  if (!notice) return null;
  return (
    <p class={"notice notice-" + notice.kind} role={notice.kind === "error" ? "alert" : "status"}>
      {notice.text}
    </p>
  );
}

/**
 * ReviewDictionary is the web form of the host's dictionary update review:
 * fetch the official sources into a session, decide every proposal, then
 * apply. It shows only when the host registers the `dictionary.update_*`
 * actions.
 */
export function ReviewDictionary() {
  return (
    <LocalOnly>
      <HostGate />
    </LocalOnly>
  );
}

function HostGate() {
  const [state, setState] = useState<{ ok: boolean | null; error: string }>({ ok: null, error: "" });
  useEffect(() => {
    hostHasDictionaryUpdate()
      .then((ok) => setState({ ok, error: "" }))
      .catch((e) => setState({ ok: false, error: errorText(e) }));
  }, []);
  if (state.ok === null) return <p class="muted">Loading…</p>;
  if (state.ok) return <ReviewDictionaryScreen />;
  return (
    <>
      <h1>Dictionary update review</h1>
      {state.error ? (
        <p class="error" role="alert">
          Could not read the action catalog: {state.error}
        </p>
      ) : (
        <>
          <p>Not available on this host</p>
          <p class="muted">This review runs on actions the host registers; this host registers none.</p>
        </>
      )}
    </>
  );
}

/** Editing is the open proposal editor and how its E decision settles. */
interface Editing {
  t: Proposal;
  settle(outcome: Notice | void): void;
}

function ReviewDictionaryScreen() {
  const [view, setView] = useState<SessionView | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [sessionNotice, setSessionNotice] = useState<Notice | undefined>();
  const [reviewNotice, setReviewNotice] = useState<Notice | undefined>();
  const [applyNotice, setApplyNotice] = useState<Notice | undefined>();
  const [start, setStart] = useState<JobState>({ kind: "idle" });
  const [preview, setPreview] = useState<Preview | null>(null);
  const [previewing, setPreviewing] = useState(false);
  const [applying, setApplying] = useState(false);
  const [applied, setApplied] = useState<Applied | null>(null);
  const [pending, setPending] = useState<Pending>(null);
  const [editing, setEditing] = useState<Editing | null>(null);
  const [goTo, setGoTo] = useState<{ key: string } | undefined>();
  const streams = useRef<Set<() => void>>(new Set());
  const shownSession = useRef<string | null>(null);
  // skipping guards a second S before the re-render; skipBusy blocks A and R meanwhile.
  const skipping = useRef(false);
  const [skipBusy, setSkipBusy] = useState(false);
  // The latest preview request wins.
  const previewSeq = useRef(0);

  useEffect(() => () => streams.current.forEach((close) => close()), []);

  const loadPreview = useCallback(async (id: string) => {
    const mine = ++previewSeq.current;
    setPreviewing(true);
    try {
      const { object } = resultObject<Preview>(PREVIEW, await runAction(PREVIEW, { session_id: id }, false));
      if (mine === previewSeq.current) {
        setPreview(object);
        setApplyNotice(undefined);
      }
    } catch (e) {
      if (mine === previewSeq.current) setApplyNotice({ kind: "error", text: "Could not preview the changes: " + errorText(e) });
    } finally {
      if (mine === previewSeq.current) setPreviewing(false);
    }
  }, []);

  /** load reads the open session; the preview is refreshed once every proposal is decided. */
  const load = useCallback(async (): Promise<SessionView | null> => {
    try {
      const { object } = resultObject<Partial<SessionView>>(SESSION, await runAction(SESSION, {}, false));
      const next: SessionView = {
        session: object?.session ?? null,
        proposals: object?.proposals ?? [],
        undecided: object?.undecided ?? 0,
        stale: object?.stale ?? false,
        stale_reason: object?.stale_reason ?? "",
      };
      setView(next);
      setLoadError(null);
      previewSeq.current++;
      setPreview(null);
      setPreviewing(false);
      const id = next.stale ? null : (next.session?.id ?? null);
      if (id && id !== shownSession.current) {
        // A newly shown session opens at its first undecided proposal.
        const first = next.proposals.find((p) => !p.decision);
        if (first) setGoTo({ key: keyOf(first) });
      }
      shownSession.current = id;
      if (id && next.undecided === 0) loadPreview(id);
      return next;
    } catch (e) {
      const text = errorText(e);
      setView(null);
      shownSession.current = null;
      setLoadError(text);
      return null;
    }
  }, [loadPreview]);

  useEffect(() => {
    load();
  }, [load]);

  // A stale session is shown only to be discarded: it is neither reviewed nor applied.
  const stale = view?.stale ? view.session : null;
  const session = view?.stale ? null : (view?.session ?? null);
  const proposals = view?.proposals ?? [];
  const startRunning = start.kind === "running";
  const blocked = !session || editing !== null || pending !== null || applying || startRunning || skipBusy;

  /** moveOn selects the next undecided proposal after t (wrapping), if any. */
  const moveOn = (t: Proposal, next: SessionView | null) => {
    const ps = next?.proposals ?? [];
    const target = ps.find((p) => p.index > t.index && !p.decision) ?? ps.find((p) => !p.decision);
    if (target) setGoTo({ key: keyOf(target) });
  };

  /** decide records one decision; deciding a proposal again replaces its earlier decision. */
  const decide = async (t: Proposal, action: Decision["action"], extra: { category?: string; note?: string } = {}): Promise<Notice | void> => {
    if (!session) return;
    setReviewNotice(undefined);
    let summary: string;
    try {
      summary = resultObject(DECIDE, await runAction(DECIDE, { session_id: session.id, index: t.index, action, ...extra }, false)).summary;
    } catch (e) {
      // The session may have closed or the dictionary changed: show that.
      load();
      throw new Error(errorText(e));
    }
    moveOn(t, await load());
    return { kind: "info", text: summary || `Proposal ${t.index + 1}: ${action}.` };
  };

  const onSkip = (t: Proposal) => {
    if (blocked || skipping.current) return;
    skipping.current = true;
    setSkipBusy(true);
    decide(t, "skip")
      .then((n) => setReviewNotice(n || undefined))
      .catch((e) => setReviewNotice({ kind: "error", text: e instanceof Error ? e.message : String(e) }))
      .finally(() => {
        skipping.current = false;
        setSkipBusy(false);
      });
  };

  const decisions: ReviewDecision<Proposal>[] = [
    { key: "a", label: "Accept", onDecide: (t) => decide(t, "accept") },
    { key: "r", label: "Reject", onDecide: (t) => decide(t, "reject") },
    {
      key: "e",
      label: "Edit",
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
  const saveEdit = async (category: string, note: string) => {
    if (!editing) return;
    const notice = await decide(editing.t, "edit", { category, note });
    setEditing(null);
    editing.settle(notice);
  };

  const decideType = async (t: Proposal, accept: boolean) => {
    if (!session) return;
    try {
      const { summary } = resultObject(DECIDE_TYPE, await runAction(DECIDE_TYPE, { session_id: session.id, index: t.index, accept }, false));
      setReviewNotice({ kind: "info", text: summary || `${accept ? "Accepted" : "Rejected"} all of kind ${kindLabel(t.kind)}.` });
      moveOn(t, await load());
    } catch (e) {
      setReviewNotice({ kind: "error", text: errorText(e) });
      load();
    } finally {
      setPending(null);
    }
  };

  const startSession = async () => {
    setStart({ kind: "idle" });
    setSessionNotice(undefined);
    setApplied(null);
    try {
      const out = await runAction(START, {}, false);
      let result: Result | undefined;
      if ("job" in out) {
        setStart({ kind: "running", id: out.job.id });
        const j = await untilJobEnd(out.job, streams.current);
        if (j.status !== "succeeded" || !j.result) {
          setStart({ kind: "error", text: `Job ${j.id} ${j.status}${j.error ? ": " + j.error : ""}` });
          return;
        }
        result = j.result;
      } else {
        result = out.result;
      }
      setStart({ kind: "done", summary: result.summary ?? "" });
    } catch (e) {
      setStart({ kind: "error", text: errorText(e) });
    } finally {
      load();
    }
  };

  const discard = async (id: string) => {
    setPending(null);
    setSessionNotice(undefined);
    try {
      const { summary } = resultObject(DISCARD, await runAction(DISCARD, { session_id: id }, true));
      setSessionNotice({ kind: "info", text: summary || `Session ${id} discarded.` });
    } catch (e) {
      setSessionNotice({ kind: "error", text: "Could not discard the session: " + errorText(e) });
    } finally {
      load();
    }
  };

  const apply = async () => {
    setPending(null);
    if (!session) return;
    setApplying(true);
    setApplyNotice(undefined);
    try {
      const { object, summary } = resultObject<Omit<Applied, "summary">>(APPLY, await runAction(APPLY, { session_id: session.id }, true));
      setApplied({ ...object, summary });
    } catch (e) {
      setApplyNotice({ kind: "error", text: "Could not apply: " + errorText(e) });
    } finally {
      setApplying(false);
      load();
    }
  };

  const undecided = view?.undecided ?? 0;

  return (
    <>
      <h1>Dictionary update review</h1>
      <p class="muted">
        The web form of the host's dictionary update review: fetch the roster and staff from the configured official
        sources, decide every proposed change, then apply them to the dictionary. Each decision is saved in the session
        as soon as it is made; the session is the same one the command line resumes, so do not review it in both at the
        same time.
      </p>

      <section class="rules-zone" aria-labelledby="zone-session">
        <h2 id="zone-session">
          <span class="zone-step">1</span> Session
        </h2>
        <NoticeLine notice={sessionNotice} />
        {view === null && loadError === null && <p class="muted">Loading…</p>}
        {loadError && (
          <div class="rules-zone-bar">
            <p class="notice notice-error" role="alert">
              {loadError}
            </p>
            <button type="button" class="btn" onClick={() => load()}>
              Retry
            </button>
          </div>
        )}
        {stale && (
          <div class="rules-zone-bar">
            <p class="notice notice-error" role="alert">
              {view?.stale_reason}
            </p>
            <p class="dict-session-line">
              Session <code>{stale.id}</code> · started {when(stale.started_at)}
              {stale.dictionary_version && ` · dictionary ${stale.dictionary_version}`} · cannot be continued: discard it and
              start again.
            </p>
            <button type="button" class="btn danger" disabled={startRunning || pending !== null} onClick={() => setPending({ kind: "discard", id: stale.id })}>
              Discard session
            </button>
          </div>
        )}
        {view && !session && !stale && (
          <div class="rules-zone-bar">
            <button type="button" class="btn primary" disabled={startRunning} onClick={startSession}>
              {startRunning ? "Fetching…" : "Fetch and start"}
            </button>
            <span class="muted">No open session. Fetching reads the official sources and opens a session with one proposal per difference.</span>
          </div>
        )}
        {session && (
          <div class="rules-zone-bar">
            <p class="dict-session-line">
              Session <code>{session.id}</code> · {session.status} · started {when(session.started_at)}
              {session.dictionary_version && ` · dictionary ${session.dictionary_version}`} · {plural(proposals.length, "proposal")},{" "}
              {undecided} undecided
            </p>
            <span class="spacer" />
            <button type="button" class="btn danger" disabled={blocked} onClick={() => setPending({ kind: "discard", id: session.id })}>
              Discard session
            </button>
          </div>
        )}
        <JobLine state={start} />
      </section>

      <section class="rules-zone" aria-labelledby="zone-review">
        <h2 id="zone-review">
          <span class="zone-step">2</span> Review
        </h2>
        <p class="muted">
          <kbd>A</kbd> accept, <kbd>R</kbd> reject, <kbd>S</kbd> skip (a recorded decision), <kbd>E</kbd> edit the
          category or add a note. Decided proposals stay listed; select one to decide it again.
        </p>
        <ProposalEditor
          open={editing !== null}
          proposal={editing ? editing.t : { index: -1, canonical_key: "", category: "", detail: "" }}
          onSave={saveEdit}
          onClose={closeEditor}
        />
        {session && (
          <ReviewScreen<Proposal>
            items={proposals}
            itemKey={keyOf}
            renderItem={(t) => (
              <ProposalItem
                p={t}
                total={proposals.length}
                sameKind={sameKindFrom(proposals, t).length}
                disabled={blocked}
                onType={(accept) => setPending({ kind: "type", t, accept })}
              />
            )}
            renderListEntry={(t) => <ProposalEntry p={t} />}
            decisions={decisions}
            onSkip={onSkip}
            onRefresh={load}
            notice={reviewNotice}
            decisionsDisabled={blocked}
            goTo={goTo}
            emptyText="This session has no proposals: the dictionary already matches the official sources."
          />
        )}
        {view && !session && (
          <p class="muted">{stale ? "The session cannot be continued: nothing to review." : "Nothing to review without an open session."}</p>
        )}
      </section>

      <section class="rules-zone" aria-labelledby="zone-apply">
        <h2 id="zone-apply">
          <span class="zone-step">3</span> Apply
        </h2>
        <NoticeLine notice={applyNotice} />
        {session && (
          <>
            {undecided > 0 ? (
              <p class="dict-undecided">
                {`${plural(undecided, "proposal")} still undecided — every proposal needs a decision (skip counts) before applying.`}
              </p>
            ) : (
              <p class="muted">Every proposal has a decision.</p>
            )}
            {previewing && <p class="muted">Previewing…</p>}
            {preview && (
              <div class="dict-preview">
                <p>
                  {preview.changes === 0
                    ? "No accepted changes: applying leaves the dictionary unchanged and closes the session."
                    : `${plural(preview.changes, "change")} to the dictionary.`}
                </p>
                <Diff diff={preview.diff} />
                <RegressionView r={preview.regression} error={preview.regression_error} />
              </div>
            )}
            <div class="rules-zone-bar">
              <button type="button" class="btn" disabled={previewing || blocked} onClick={() => loadPreview(session.id)}>
                Preview changes
              </button>
              <button
                type="button"
                class="btn primary"
                disabled={undecided > 0 || blocked}
                onClick={() => setPending({ kind: "apply" })}
              >
                Apply to the dictionary
              </button>
              {applying && <span class="muted">Applying…</span>}
            </div>
          </>
        )}
        {!session && !applied && view && (
          <p class="muted">{stale ? "The session cannot be continued: nothing to apply." : "No open session to apply."}</p>
        )}
        {applied && (
          <div class="rule-outcome" role="status">
            <p class="rule-outcome-head">{applied.summary}</p>
            {!applied.unchanged && <p>{`Dictionary ${applied.from} → ${applied.to} · ${plural(applied.changes, "change")}`}</p>}
            <Diff diff={applied.diff} />
            {(applied.regression || applied.regression_error) && <RegressionView r={applied.regression} error={applied.regression_error} />}
          </div>
        )}
      </section>

      {pending?.kind === "type" && (
        <ConfirmType
          t={pending.t}
          accept={pending.accept}
          count={sameKindFrom(proposals, pending.t).length}
          onConfirm={() => decideType(pending.t, pending.accept)}
          onCancel={() => setPending(null)}
        />
      )}
      {pending?.kind === "apply" && <DestructiveConfirm name={APPLY} onConfirm={apply} onCancel={() => setPending(null)} />}
      {pending?.kind === "discard" && (
        <DestructiveConfirm name={DISCARD} onConfirm={() => discard(pending.id)} onCancel={() => setPending(null)} />
      )}
    </>
  );
}
