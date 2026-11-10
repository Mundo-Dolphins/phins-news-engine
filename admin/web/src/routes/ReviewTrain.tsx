import type { ComponentChildren } from "preact";
import { useCallback, useEffect, useState } from "preact/hooks";
import { ApiError, runAction } from "../api";
import { ReviewScreen, type Notice, type ReviewDecision } from "../components/ReviewScreen";
import { safeHref } from "../links";

interface PriorLabel {
  label: string;
  labeled_by: string;
  labeled_at: string;
  source: string;
}

/** TrainReviewItem is one entry of `train.review_queue`. */
export interface TrainReviewItem {
  article_id: string;
  proposal_id: string;
  score: number;
  reason: string;
  nb_zone: string;
  nb_value: number;
  llm_label: string;
  llm_confidence: number;
  suggested_label: string;
  purged: boolean;
  title: string;
  summary: string;
  body: string;
  url: string;
  source_name: string;
  source_type: string;
  domain: string;
  published_at: string;
  cluster_id: string;
  prior_labels: PriorLabel[] | null;
}

const QUEUE_LIMIT = 50;

const keyOf = (t: TrainReviewItem) => t.article_id + "\u0000" + t.proposal_id;

function errorText(e: unknown): string {
  if (e instanceof ApiError) return `${e.message} (${e.code})`;
  return e instanceof Error ? e.message : String(e);
}

function when(iso: string): string {
  if (!iso) return "";
  const d = new Date(iso);
  return isNaN(d.getTime()) ? iso : d.toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" });
}

const num = (v: number, digits = 2) => (typeof v === "number" ? v.toFixed(digits) : "");

function Original({ url }: { url: string }) {
  const href = safeHref(url);
  if (href) {
    return (
      <a class="review-open" href={href} target="_blank" rel="noopener noreferrer" data-review-open>
        Open original ↗
      </a>
    );
  }
  if (!url) return <span class="muted">No original URL stored</span>;
  return (
    <span class="muted">
      Original (not a web link): <code>{url}</code>
    </span>
  );
}

function Fact({ label, children }: { label: string; children: ComponentChildren }) {
  return (
    <div class="fact">
      <dt>{label}</dt>
      <dd>{children}</dd>
    </div>
  );
}

function TrainItem({ t }: { t: TrainReviewItem }) {
  if (t.purged) {
    return (
      <p class="review-purged" role="note">
        <code>{t.article_id}</code>: the article no longer exists (purged). Publish or Discard drops it from the
        queue.
      </p>
    );
  }
  const meta = [t.source_name, t.source_type, t.domain].filter(Boolean).join(" · ");
  const prior = t.prior_labels ?? [];
  return (
    <article class="review-article">
      <header>
        <h2>{t.title || "(untitled)"}</h2>
        <p class="review-meta">
          {meta && <span>{meta}</span>}
          {t.published_at && <time dateTime={t.published_at}>{when(t.published_at)}</time>}
        </p>
        <p class="review-original">
          <Original url={t.url} />
        </p>
      </header>
      <h3>Summary</h3>
      {t.summary ? <p class="review-text">{t.summary}</p> : <p class="muted">No summary stored.</p>}
      {t.body && (
        <details class="review-body">
          <summary>Body</summary>
          <p class="review-text">{t.body}</p>
        </details>
      )}
      <h3>Verdicts</h3>
      <dl class="facts">
        <Fact label="Suggested label">
          <strong>{t.suggested_label || "—"}</strong>
        </Fact>
        <Fact label="LLM">
          {t.llm_label || "—"} <span class="muted">confidence {num(t.llm_confidence)}</span>
        </Fact>
        <Fact label="Naive Bayes">
          {t.nb_zone || "—"} <span class="muted">value {num(t.nb_value)}</span>
        </Fact>
        <Fact label="Priority">
          {num(t.score)} {t.reason && <span class="muted">({t.reason})</span>}
        </Fact>
      </dl>
      <h3>Prior human labels</h3>
      {prior.length === 0 ? (
        <p class="muted">None.</p>
      ) : (
        <ul class="list">
          {prior.map((l, i) => (
            <li key={i}>
              <strong>{l.label}</strong> by {l.labeled_by || "?"}{" "}
              <span class="muted">
                {when(l.labeled_at)} · {l.source}
              </span>
            </li>
          ))}
        </ul>
      )}
      <p class="muted review-ids">
        article <code>{t.article_id}</code> · proposal <code>{t.proposal_id}</code>
        {t.cluster_id && (
          <>
            {" "}
            · cluster <code>{t.cluster_id}</code>
          </>
        )}
      </p>
    </article>
  );
}

function TrainEntry({ t }: { t: TrainReviewItem }) {
  if (t.purged) {
    return (
      <>
        <span class="entry-title muted">Purged article</span>
        <span class="entry-meta">{t.article_id}</span>
      </>
    );
  }
  return (
    <>
      <span class="entry-title">{t.title || t.article_id}</span>
      <span class="entry-meta">
        {num(t.score)} · {t.suggested_label || "?"} · {t.source_name || t.domain}
      </span>
    </>
  );
}

/** ReviewTrain is the web form of `train review`: publish or discard the LLM teacher's queued proposals. */
export function ReviewTrain() {
  const [items, setItems] = useState<TrainReviewItem[] | null>(null);
  // Skipped and decided entries stay hidden for this page session, also
  // across refreshes that raced a decision.
  const [hidden, setHidden] = useState<Set<string>>(() => new Set());
  const [notice, setNotice] = useState<Notice | undefined>();

  const load = useCallback(async () => {
    try {
      const out = await runAction("train.review_queue", { limit: QUEUE_LIMIT }, false);
      if ("job" in out) throw new Error("unexpected job for train.review_queue");
      const obj = out.result.object as { items?: TrainReviewItem[] } | undefined;
      setItems(obj?.items ?? []);
      setNotice(undefined);
    } catch (e) {
      setNotice({ kind: "error", text: "Could not load the review queue: " + errorText(e) });
    }
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  const hide = (t: TrainReviewItem) => setHidden((h) => new Set(h).add(keyOf(t)));

  const decision =
    (key: string, label: string, value: "publish" | "discard"): ReviewDecision<TrainReviewItem> => ({
      key,
      label,
      onDecide: async (t) => {
        try {
          const out = await runAction(
            "train.review_decide",
            { article_id: t.article_id, proposal_id: t.proposal_id, decision: value },
            false,
          );
          hide(t);
          if ("job" in out) return;
          const outcome = (out.result.object as { outcome?: string } | undefined)?.outcome;
          if (outcome && outcome !== "decided") return { kind: "info", text: out.result.summary || outcome };
        } catch (e) {
          if (e instanceof ApiError && e.code === "not_found") {
            hide(t);
            return { kind: "info", text: `${t.article_id} is no longer in the review queue (decided elsewhere); removed.` };
          }
          throw new Error(errorText(e));
        }
      },
    });

  return (
    <>
      <h1>Train review</h1>
      <p class="muted">
        LLM teacher proposals waiting for a human label, highest priority first. Publish and Discard write a
        human label at once; Skip hides the item until the page is reloaded.
      </p>
      {items === null ? (
        notice ? (
          <p class="error" role="alert">
            {notice.text}
          </p>
        ) : (
          <p class="muted">Loading…</p>
        )
      ) : (
        <ReviewScreen<TrainReviewItem>
          items={items.filter((t) => !hidden.has(keyOf(t)))}
          itemKey={keyOf}
          renderItem={(t) => <TrainItem t={t} />}
          renderListEntry={(t) => <TrainEntry t={t} />}
          decisions={[decision("p", "Publish", "publish"), decision("d", "Discard", "discard")]}
          onSkip={hide}
          onRefresh={load}
          notice={notice}
          emptyText="Nothing to review: the queue is empty."
        />
      )}
    </>
  );
}
