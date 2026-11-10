import type { ComponentChildren } from "preact";
import { useCallback, useEffect, useRef, useState } from "preact/hooks";
import { ApiError, runAction } from "../api";
import { ReviewScreen, type Notice, type ReviewDecision } from "../components/ReviewScreen";
import { LocalOnly } from "../components/LocalOnly";
import { safeHref } from "../links";

/** DatasetReviewItem is one pending audit row of `train.review_dataset_queue`. */
export interface DatasetReviewItem {
  article_id: string;
  audit_reason: string;
  title: string;
  body: string;
  source_id: string;
  source_type: string;
  domain: string;
  content_type: string;
  published_at: string;
  label: string;
  label_source: string;
  /** Original URL from the operational database; "" when the article is no longer stored. */
  url: string;
}

/** DatasetPaths are the three files a dataset review works on. */
interface DatasetPaths {
  dataset: string;
  audit: string;
  out: string;
}

interface DatasetQueue {
  paths: DatasetPaths & { log: string };
  total: number;
  pending_count: number;
  items: DatasetReviewItem[] | null;
}

type Decision = "publish" | "reject" | "confirm";

const keyOf = (t: DatasetReviewItem) => t.article_id;

function errorText(e: unknown): string {
  if (e instanceof ApiError) return `${e.message} (${e.code})`;
  return e instanceof Error ? e.message : String(e);
}

function when(iso: string): string {
  if (!iso) return "";
  const d = new Date(iso);
  return isNaN(d.getTime()) ? iso : d.toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" });
}

function Original({ url, domain }: { url: string; domain: string }) {
  const href = safeHref(url);
  if (href) {
    return (
      <a class="review-open" href={href} target="_blank" rel="noopener noreferrer" data-review-open>
        Open original ↗
      </a>
    );
  }
  if (url) {
    return (
      <span class="muted">
        Original (not a web link): <code>{url}</code>
      </span>
    );
  }
  return (
    <span class="muted">
      {domain && <strong class="review-domain">{domain}</strong>} No link: article no longer stored or without a URL.
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

function DatasetItem({ t }: { t: DatasetReviewItem }) {
  const meta = [t.source_id, t.source_type, t.domain].filter(Boolean).join(" · ");
  return (
    <article class="review-article">
      <header>
        <h2>{t.title || "(untitled)"}</h2>
        <p class="review-meta">
          {meta && <span>{meta}</span>}
          {t.published_at && <time dateTime={t.published_at}>{when(t.published_at)}</time>}
        </p>
        <p class="review-original">
          <Original url={t.url} domain={t.domain} />
        </p>
      </header>
      <h3>Audit reason</h3>
      <p class="review-reason">{t.audit_reason || "—"}</p>
      <dl class="facts">
        <Fact label="Current label">
          <strong>{t.label || "—"}</strong> {t.label_source && <span class="muted">({t.label_source})</span>}
        </Fact>
        <Fact label="Source">
          {t.source_id || "—"} {t.source_type && <span class="muted">{t.source_type}</span>}
        </Fact>
        <Fact label="Content type">{t.content_type || "—"}</Fact>
      </dl>
      {t.body ? (
        <details class="review-body" open>
          <summary>Body</summary>
          <p class="review-text">{t.body}</p>
        </details>
      ) : (
        <p class="muted">No body stored.</p>
      )}
      <p class="muted review-ids">
        article <code>{t.article_id}</code>
      </p>
    </article>
  );
}

function DatasetEntry({ t }: { t: DatasetReviewItem }) {
  return (
    <>
      <span class="entry-title">{t.title || t.article_id}</span>
      <span class="entry-meta">
        {t.label || "?"} · {t.audit_reason || t.domain}
      </span>
    </>
  );
}

/** ReviewDataset is the web form of `train review-dataset`: relabel or confirm the audited dataset rows. */
export function ReviewDataset() {
  return (
    <LocalOnly>
      <ReviewDatasetScreen />
    </LocalOnly>
  );
}

function PathField({ label, value, onInput }: { label: string; value: string; onInput(v: string): void }) {
  return (
    <label class="field">
      <span>{label}</span>
      <input
        type="text"
        value={value}
        spellcheck={false}
        onInput={(e) => onInput((e.currentTarget as HTMLInputElement).value)}
      />
    </label>
  );
}

function ReviewDatasetScreen() {
  const [queue, setQueue] = useState<DatasetQueue | null>(null);
  // Field values; they take effect on Apply.
  const [form, setForm] = useState<DatasetPaths>({ dataset: "", audit: "", out: "" });
  // Paths of the last Apply; null means the action's defaults.
  const applied = useRef<Partial<DatasetPaths> | null>(null);
  // Skipped and decided rows stay hidden for this page session, also across
  // refreshes that raced a decision. Reset when other files are applied.
  const [hidden, setHidden] = useState<Set<string>>(() => new Set());
  const [decided, setDecided] = useState<Set<string>>(() => new Set());
  const [notice, setNotice] = useState<Notice | undefined>();

  // load re-reads the queue from the applied files. fillForm resets the path
  // fields to the files in use (first load and Apply); a refresh on returning
  // to the tab leaves edits in progress alone.
  const load = useCallback(async (fillForm = false) => {
    try {
      const out = await runAction("train.review_dataset_queue", applied.current ? { ...applied.current } : {}, false);
      if ("job" in out) throw new Error("unexpected job for train.review_dataset_queue");
      const q = out.result.object as DatasetQueue;
      // Decisions go to the files the queue came from.
      const paths = { dataset: q.paths.dataset, audit: q.paths.audit, out: q.paths.out };
      applied.current = paths;
      setQueue(q);
      if (fillForm) setForm(paths);
      setNotice(undefined);
    } catch (e) {
      setNotice({ kind: "error", text: "Could not load the dataset review: " + errorText(e) });
    }
  }, []);

  useEffect(() => {
    load(true);
  }, [load]);

  const apply = (e: Event) => {
    e.preventDefault();
    // An emptied field is left out so the action's default applies.
    applied.current = Object.fromEntries(Object.entries(form).filter(([, v]) => v.trim() !== ""));
    setQueue(null);
    setHidden(new Set());
    setDecided(new Set());
    load(true);
  };

  const hide = (t: DatasetReviewItem) => setHidden((h) => new Set(h).add(keyOf(t)));
  const markDecided = (t: DatasetReviewItem) => {
    hide(t);
    setDecided((d) => new Set(d).add(keyOf(t)));
  };

  const decision = (key: string, label: string, value: Decision): ReviewDecision<DatasetReviewItem> => ({
    key,
    label,
    onDecide: async (t) => {
      const paths = applied.current;
      if (!paths) throw new Error("the review queue is not loaded");
      try {
        const out = await runAction(
          "train.review_dataset_decide",
          { ...paths, article_id: t.article_id, decision: value },
          false,
        );
        markDecided(t);
        if ("job" in out) return;
        return { kind: "info", text: out.result.summary || `${t.article_id}: ${value}` };
      } catch (e) {
        if (e instanceof ApiError && e.code === "conflict" && e.message.includes("already reviewed")) {
          markDecided(t);
          return { kind: "info", text: `${t.article_id} was already reviewed in this copy; removed from the queue.` };
        }
        throw new Error(errorText(e));
      }
    },
  });

  const items = queue?.items ?? [];
  const pending = items.filter((t) => !decided.has(keyOf(t))).length;

  return (
    <>
      <h1>Dataset review</h1>
      <p class="muted">
        Audited rows of the training dataset. Publish and Reject relabel the row and Confirm keeps its label, in the
        reviewed copy only; the source dataset is never modified. Each decision is saved at once; Skip hides the row
        until the page is reloaded.
      </p>
      <form class="dataset-paths" onSubmit={apply}>
        <PathField label="Dataset" value={form.dataset} onInput={(v) => setForm((f) => ({ ...f, dataset: v }))} />
        <PathField label="Audit" value={form.audit} onInput={(v) => setForm((f) => ({ ...f, audit: v }))} />
        <PathField label="Reviewed copy" value={form.out} onInput={(v) => setForm((f) => ({ ...f, out: v }))} />
        <button type="submit" class="btn">
          Apply
        </button>
      </form>
      <div class="dataset-status">
        {queue && (
          <span class="dataset-count" title="pending / total audit rows">
            <strong>
              {pending} / {queue.total}
            </strong>{" "}
            <span class="muted">pending · log {queue.paths.log}</span>
          </span>
        )}
        <span class="dataset-warning" role="note">
          Do not review the same copy from the CLI at the same time.
        </span>
      </div>
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
        <ReviewScreen<DatasetReviewItem>
          items={items.filter((t) => !hidden.has(keyOf(t)))}
          itemKey={keyOf}
          renderItem={(t) => <DatasetItem t={t} />}
          renderListEntry={(t) => <DatasetEntry t={t} />}
          decisions={[
            decision("p", "Publish", "publish"),
            decision("r", "Reject", "reject"),
            decision("c", "Confirm", "confirm"),
          ]}
          onSkip={hide}
          onRefresh={() => load()}
          notice={notice}
          emptyText="Nothing to review: every audit row has a decision."
        />
      )}
    </>
  );
}
