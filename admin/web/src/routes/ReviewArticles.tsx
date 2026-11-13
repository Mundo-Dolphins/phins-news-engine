import { useCallback, useEffect, useRef, useState } from "preact/hooks";
import { ApiError, runAction } from "../api";
import { LocalOnly } from "../components/LocalOnly";
import { ReviewScreen, type Notice, type ReviewDecision } from "../components/ReviewScreen";
import { cycleStepView, errorText, when } from "../components/reviewParts";
import { safeHref } from "../links";

const STEP = "article_review";

/** ClusterMate is another article of the reviewed article's cluster. */
export interface ClusterMate {
  article_id: string;
  title: string;
  source: string;
  url: string;
  published_at: string;
}

/** ArticleReviewItem is one article of `cycle.review_queue`. */
export interface ArticleReviewItem {
  article_id: string;
  /** 1-based position in the review set, as the CLI's [i/n]. */
  index: number;
  title: string;
  /** The full summary, never truncated. */
  summary: string;
  url: string;
  source: { name: string; type: string };
  published_at: string;
  score: number;
  /** Always 0, as the CLI shows it for the frozen set. */
  ambiguity: number;
  prior_label: { label: string; by: string } | null;
  cluster: { id: string; role: string; others: ClusterMate[] | null };
  decision: { action: string; notes: string } | null;
}

interface Queue {
  items: ArticleReviewItem[];
  total: number;
  undecided: number;
}

interface Finished {
  articles_reviewed: number;
  publish: number;
  reject: number;
}

/** The CLI's review keys, in its order; K (skip) is the screen's skip key. */
const ACTIONS: { key: string; action: string; label: string }[] = [
  { key: "p", action: "publish", label: "Publish" },
  { key: "r", action: "reject", label: "Reject" },
  { key: "a", action: "ambiguous", label: "Ambiguous" },
  { key: "d", action: "duplicate", label: "Duplicate" },
  { key: "s", action: "same_news", label: "Same news" },
  { key: "u", action: "update", label: "Update" },
  { key: "e", action: "edit_category", label: "Edit category" },
];
const SKIP = "skip";

const LABELS: Record<string, string> = Object.fromEntries([...ACTIONS.map((a) => [a.action, a.label]), [SKIP, "Skip"]]);
const labelOf = (action: string) => LABELS[action] ?? action;

const keyOf = (t: ArticleReviewItem) => t.article_id;
const fixed = (v: number) => (typeof v === "number" ? v.toFixed(2) : "");

function DecisionBadge({ action }: { action: string }) {
  return <span class={"badge cycle-decision cycle-decision-" + action}>{labelOf(action)}</span>;
}

function Original({ url }: { url: string }) {
  if (!url) return <p class="muted cycle-original">No original URL.</p>;
  const href = safeHref(url);
  return (
    <p class="cycle-original">
      Original:{" "}
      {href ? (
        <a href={href} target="_blank" rel="noopener noreferrer" data-review-open>
          {url}
        </a>
      ) : (
        <>
          <code>{url}</code> <span class="muted">(not a web link)</span>
        </>
      )}
    </p>
  );
}

function Cluster({ cluster }: { cluster: ArticleReviewItem["cluster"] }) {
  const others = cluster.others ?? [];
  return (
    <>
      <h3>Same cluster ({others.length})</h3>
      {others.length === 0 ? (
        <p class="muted">{cluster.id ? "No other article in this cluster." : "Not in a cluster."}</p>
      ) : (
        <ul class="entity-links cycle-cluster">
          {others.map((o) => {
            const href = safeHref(o.url);
            const title = o.title || o.article_id;
            return (
              <li key={o.article_id}>
                {href ? (
                  <a href={href} target="_blank" rel="noopener noreferrer">
                    {title}
                  </a>
                ) : (
                  <span>{title}</span>
                )}{" "}
                <span class="muted">{[o.source, when(o.published_at)].filter(Boolean).join(" · ")}</span>
              </li>
            );
          })}
        </ul>
      )}
    </>
  );
}

function ArticleCard({ t }: { t: ArticleReviewItem }) {
  const source = t.source.type ? `${t.source.name} (${t.source.type})` : t.source.name;
  const prior = t.prior_label ? `prior label: ${t.prior_label.label}${t.prior_label.by ? ` (by ${t.prior_label.by})` : ""}` : "no prior label";
  return (
    <article class="review-article cycle-article">
      <header>
        <h2 class="cycle-headline">{t.title || t.article_id}</h2>
        {t.decision && (
          <p class="cycle-decided">
            Decided: <DecisionBadge action={t.decision.action} />
          </p>
        )}
      </header>
      {t.summary ? <p class="cycle-summary">{t.summary}</p> : <p class="muted">No summary.</p>}
      <Original url={t.url} />
      <Cluster cluster={t.cluster} />
      <p class="review-meta cycle-facts">
        <span>{source}</span>
        {t.published_at && <span>{when(t.published_at)}</span>}
        <span>{"score " + fixed(t.score)}</span>
        <span>{"ambiguity signal " + fixed(t.ambiguity)}</span>
        <span>{prior}</span>
        {t.cluster.id && <span>{`cluster ${t.cluster.id}${t.cluster.role ? " · " + t.cluster.role : ""}`}</span>}
      </p>
      <p class="muted review-ids">
        article <code>{t.article_id}</code>
      </p>
    </article>
  );
}

function ArticleEntry({ t }: { t: ArticleReviewItem }) {
  return (
    <>
      <span class="entry-title">{t.title || t.article_id}</span>
      <span class="entry-meta">
        #{t.index} {t.decision ? <DecisionBadge action={t.decision.action} /> : <span class="muted">undecided</span>}
      </span>
    </>
  );
}

/** ReviewArticlesProps are the props of the article review screens. */
export interface ReviewArticlesProps {
  /** Called once the review is finished (`cycle.review_finish` succeeded); the wizard moves on with it. */
  onDone?(): void;
}

/** ReviewArticles is the editorial cycle's article review at `/review/articles`, local mode only. */
export function ReviewArticles(props: ReviewArticlesProps) {
  return (
    <LocalOnly>
      <ReviewArticlesScreen {...props} />
    </LocalOnly>
  );
}

type Phase =
  | { kind: "loading" }
  | { kind: "elsewhere" }
  | { kind: "finished"; result?: Finished }
  | { kind: "error"; text: string }
  | { kind: "open"; sessionId: string };

/**
 * ReviewArticlesScreen is the article review of the editorial cycle, the
 * web form of the CLI's review loop: it opens (or resumes) the cycle's
 * review session whenever it is shown and pauses it when it is left.
 * Decided articles stay listed and can be decided again; "Finish review"
 * is offered once every article has a decision.
 */
export function ReviewArticlesScreen({ onDone }: ReviewArticlesProps) {
  const [phase, setPhase] = useState<Phase>({ kind: "loading" });
  const [queue, setQueue] = useState<Queue | null>(null);
  const [notice, setNotice] = useState<Notice | undefined>();
  const [goTo, setGoTo] = useState<{ key: string } | undefined>();
  const [finishing, setFinishing] = useState(false);
  // skipping guards a second K before the re-render; skipBusy blocks the decisions meanwhile.
  const skipping = useRef(false);
  const [skipBusy, setSkipBusy] = useState(false);
  // opened: the session is open and must be paused when the screen is left (unless finished).
  const opened = useRef(false);
  const finished = useRef(false);
  const alive = useRef(true);
  const doneRef = useRef(onDone);
  doneRef.current = onDone;

  const load = useCallback(async (first = false): Promise<Queue | null> => {
    try {
      const out = await runAction("cycle.review_queue", {}, false);
      if ("job" in out) throw new Error("unexpected job for cycle.review_queue");
      const o = out.result.object as Partial<Queue> | undefined;
      const q: Queue = { items: o?.items ?? [], total: o?.total ?? 0, undecided: o?.undecided ?? 0 };
      setQueue(q);
      if (first) {
        // The review opens at its first undecided article.
        const next = q.items.find((t) => !t.decision);
        if (next) setGoTo({ key: keyOf(next) });
      }
      return q;
    } catch (e) {
      setNotice({ kind: "error", text: "Could not load the article review: " + errorText(e) });
      return null;
    }
  }, []);

  const pause = () => {
    if (!opened.current || finished.current) return;
    opened.current = false;
    runAction("cycle.review_pause", {}, false).catch(() => {});
  };

  const start = useCallback(async () => {
    setPhase({ kind: "loading" });
    try {
      const view = await cycleStepView(STEP);
      if (!alive.current) return;
      if (view === "elsewhere") return setPhase({ kind: "elsewhere" });
      if (view === "finished") {
        finished.current = true;
        return setPhase({ kind: "finished" });
      }
      const out = await runAction("cycle.review_open", {}, false);
      if ("job" in out) throw new Error("unexpected job for cycle.review_open");
      opened.current = true;
      // Left while the session was opening: pause it straight away.
      if (!alive.current) return pause();
      const o = out.result.object as { session_id?: string } | undefined;
      setPhase({ kind: "open", sessionId: o?.session_id ?? "" });
      await load(true);
    } catch (e) {
      if (alive.current) setPhase({ kind: "error", text: errorText(e) });
    }
  }, [load]);

  useEffect(() => {
    alive.current = true;
    start();
    return () => {
      alive.current = false;
      pause();
    };
  }, [start]);

  /** moveOn selects the next undecided article after t (wrapping), if any. */
  const moveOn = (t: ArticleReviewItem, q: Queue | null) => {
    const items = q?.items ?? [];
    const next = items.find((x) => x.index > t.index && !x.decision) ?? items.find((x) => !x.decision);
    if (next) setGoTo({ key: keyOf(next) });
  };

  /** decide records one decision; deciding an article again replaces its earlier decision. */
  const decide = async (t: ArticleReviewItem, action: string): Promise<Notice | void> => {
    setNotice(undefined);
    try {
      await runAction("cycle.review_decide", { article_id: t.article_id, action }, false);
    } catch (e) {
      if (e instanceof ApiError && e.code === "not_found") load();
      throw new Error(errorText(e));
    }
    moveOn(t, await load());
    return { kind: "info", text: `${t.title || t.article_id}: ${labelOf(action)}.` };
  };

  const onSkip = (t: ArticleReviewItem) => {
    if (skipping.current || finishing) return;
    skipping.current = true;
    setSkipBusy(true);
    decide(t, SKIP)
      .then((n) => setNotice(n || undefined))
      .catch((e) => setNotice({ kind: "error", text: e instanceof Error ? e.message : String(e) }))
      .finally(() => {
        skipping.current = false;
        setSkipBusy(false);
      });
  };

  const decisions: ReviewDecision<ArticleReviewItem>[] = ACTIONS.map((a) => ({
    key: a.key,
    label: a.label,
    onDecide: (t) => decide(t, a.action),
  }));

  const finish = async () => {
    if (finishing) return;
    setFinishing(true);
    setNotice(undefined);
    try {
      const out = await runAction("cycle.review_finish", {}, false);
      if ("job" in out) throw new Error("unexpected job for cycle.review_finish");
      finished.current = true;
      setPhase({ kind: "finished", result: out.result.object as Finished });
      doneRef.current?.();
    } catch (e) {
      setNotice({ kind: "error", text: "Could not finish the review: " + errorText(e) });
      load();
    } finally {
      setFinishing(false);
    }
  };

  return (
    <>
      <h1>Article review</h1>
      <p class="muted">
        The editorial cycle's article review. <kbd>P</kbd> publish, <kbd>R</kbd> reject, <kbd>A</kbd> ambiguous,{" "}
        <kbd>D</kbd> duplicate, <kbd>S</kbd> same news, <kbd>U</kbd> update, <kbd>E</kbd> edit category (withdraws the
        prior label, no category asked), <kbd>K</kbd> skip. Each decision is saved at once; decided articles stay listed
        and can be decided again.
      </p>
      {phase.kind === "loading" && <p class="muted">Loading…</p>}
      {phase.kind === "elsewhere" && (
        <p class="muted">
          The editorial cycle is not at its article review. <a href="/cycle">Open from the editorial cycle</a>.
        </p>
      )}
      {phase.kind === "error" && (
        <p class="error" role="alert">
          {phase.text}{" "}
          <button type="button" class="btn" onClick={() => start()}>
            Retry
          </button>
        </p>
      )}
      {phase.kind === "finished" && (
        <div class="rule-outcome" role="status">
          <p class="rule-outcome-head">The article review is finished.</p>
          {phase.result && (
            <p>{`${phase.result.articles_reviewed} reviewed · ${phase.result.publish} publish · ${phase.result.reject} reject`}</p>
          )}
        </div>
      )}
      {phase.kind === "open" && (
        <>
          {queue && (
            <div class="rules-zone-bar cycle-review-bar">
              <p class="dict-session-line">
                Session <code>{phase.sessionId}</code> · {`${queue.total} article${queue.total === 1 ? "" : "s"} · ${queue.undecided} undecided`}
              </p>
              <span class="spacer" />
              <button type="button" class="btn primary" disabled={queue.undecided > 0 || finishing || skipBusy} onClick={finish}>
                Finish review
              </button>
            </div>
          )}
          {queue === null ? (
            notice ? (
              <p class="error" role="alert">
                {notice.text}{" "}
                <button type="button" class="btn" onClick={() => load(true)}>
                  Retry
                </button>
              </p>
            ) : (
              <p class="muted">Loading…</p>
            )
          ) : (
            <ReviewScreen<ArticleReviewItem>
              items={queue.items}
              itemKey={keyOf}
              renderItem={(t) => <ArticleCard t={t} />}
              renderListEntry={(t) => <ArticleEntry t={t} />}
              decisions={decisions}
              onSkip={onSkip}
              onRefresh={() => load()}
              notice={notice}
              decisionsDisabled={skipBusy || finishing}
              goTo={goTo}
              skipKey="k"
              emptyText="Nothing to review: the session's review set is empty."
            />
          )}
        </>
      )}
    </>
  );
}
