import type { ComponentChildren } from "preact";
import { useCallback, useEffect, useState } from "preact/hooks";
import { ApiError, runAction } from "../api";
import { ReviewScreen, type Notice, type ReviewDecision } from "../components/ReviewScreen";
import { LocalOnly } from "../components/LocalOnly";
import { MergePicker } from "../components/MergePicker";
import { errorText } from "../components/reviewParts";
import { safeHref } from "../links";

/** EntityProposalItem is one pending proposal of `entities.review_queue`. */
export interface EntityProposalItem {
  id: string;
  name: string;
  type: string;
  kind: string;
  target_entity_id: string;
  confidence: number;
  occurrences: number;
  score: number;
  aliases: string[] | null;
  verification: { position: string; role: string; team: string; league: string };
  /** "FREE AGENT", "UNKNOWN" or "" (the CLI's affiliation warning). */
  affiliation: string;
  suggestion: { entity: string; team: string; basis: string; candidates: string };
  metadata: Record<string, string> | null;
  /** Evidence articles; url is "" when the article is no longer in the operational database. */
  evidence: { article_id: string; title: string; url: string }[] | null;
  web_sources: { domain: string; url: string; title: string }[] | null;
  first_seen: string;
  last_seen: string;
}

const QUEUE_LIMIT = 100;
const DUPLICATE = "possible_duplicate";

const KIND_LABELS: Record<string, string> = {
  new_entity: "New entity",
  new_alias: "New alias",
  [DUPLICATE]: "Possible duplicate",
};

const keyOf = (t: EntityProposalItem) => t.id;
const isDuplicate = (t: EntityProposalItem) => t.kind === DUPLICATE;
const num = (v: number, digits = 2) => (typeof v === "number" ? v.toFixed(digits) : "");

function KindBadge({ kind }: { kind: string }) {
  return <span class={"badge entity-kind entity-kind-" + kind}>{KIND_LABELS[kind] ?? kind}</span>;
}

function Fact({ label, children }: { label: string; children: ComponentChildren }) {
  return (
    <div class="fact">
      <dt>{label}</dt>
      <dd>{children}</dd>
    </div>
  );
}

function Suggestion({ s }: { s: EntityProposalItem["suggestion"] }) {
  if (s.entity) {
    return (
      <p class="entity-suggestion">
        Suggested match: <strong>{s.entity}</strong>
        {s.team && <> ({s.team})</>}
        {s.basis && <span class="muted"> · basis {s.basis}</span>}
      </p>
    );
  }
  if (s.candidates) {
    return (
      <p class="entity-suggestion">
        Candidates: <strong>{s.candidates}</strong>
        {s.basis && <span class="muted"> · basis {s.basis}</span>}
      </p>
    );
  }
  return <p class="muted">No suggested match.</p>;
}

/** Evidence articles listed before the rest are folded away. */
const EVIDENCE_SHOWN = 10;

type EvidenceRow = NonNullable<EntityProposalItem["evidence"]>[number];

function EvidenceList({ rows, offset, primary }: { rows: EvidenceRow[]; offset: number; primary: number }) {
  return (
    <ul class="entity-links">
      {rows.map((e, j) => {
        const i = offset + j;
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
            <span class="muted">
              {!e.url
                ? "· article no longer stored"
                : !href && (
                    <>
                      · not a web link: <code>{e.url}</code>
                    </>
                  )}
            </span>
          </li>
        );
      })}
    </ul>
  );
}

/**
 * Evidence lists every evidence article, the first EVIDENCE_SHOWN at once and
 * the rest in a disclosure; the first one with a web link is the one O opens.
 */
function Evidence({ evidence }: { evidence: EvidenceRow[] }) {
  if (evidence.length === 0) return <p class="muted">No evidence articles.</p>;
  const primary = evidence.findIndex((e) => safeHref(e.url) !== null);
  const rest = evidence.slice(EVIDENCE_SHOWN);
  return (
    <>
      <EvidenceList rows={evidence.slice(0, EVIDENCE_SHOWN)} offset={0} primary={primary} />
      {rest.length > 0 && (
        <details class="entity-more">
          <summary>
            {rest.length} more evidence article{rest.length === 1 ? "" : "s"}
          </summary>
          <EvidenceList rows={rest} offset={EVIDENCE_SHOWN} primary={primary} />
        </details>
      )}
    </>
  );
}

function WebSources({ sources }: { sources: NonNullable<EntityProposalItem["web_sources"]> }) {
  if (sources.length === 0) return <p class="muted">No web sources.</p>;
  return (
    <ul class="entity-links">
      {sources.map((w, i) => {
        const href = safeHref(w.url);
        const label = w.title || w.url || w.domain;
        return (
          <li key={i}>
            {w.domain && <strong class="review-domain">{w.domain}</strong>}
            {href ? (
              <a href={href} target="_blank" rel="noopener noreferrer">
                {label}
              </a>
            ) : (
              <span>{label}</span>
            )}
          </li>
        );
      })}
    </ul>
  );
}

function EntityItem({ t }: { t: EntityProposalItem }) {
  const v = t.verification;
  const aliases = t.aliases ?? [];
  const metadata = Object.entries(t.metadata ?? {}).sort(([a], [b]) => a.localeCompare(b));
  return (
    <article class="review-article entity-card">
      <header>
        <h2>{t.name}</h2>
        <p class="review-meta">
          <KindBadge kind={t.kind} />
          <span>{t.type}</span>
          <span>confidence {num(t.confidence)}</span>
          <span>
            {t.occurrences} occurrence{t.occurrences === 1 ? "" : "s"}
          </span>
          <span>score {num(t.score)}</span>
          <span>
            seen {t.first_seen}
            {t.last_seen && t.last_seen !== t.first_seen && <> – {t.last_seen}</>}
          </span>
        </p>
      </header>
      {isDuplicate(t) && (
        <p class="entity-note" role="note">
          Possibly the same as an existing entity: approve it as new (<kbd>N</kbd>) or merge it into an existing
          entity as an alias (<kbd>M</kbd>).
        </p>
      )}
      {t.kind === "new_alias" && t.target_entity_id && (
        <p class="entity-note" role="note">
          New alias of <code>{t.target_entity_id}</code>.
        </p>
      )}
      {t.affiliation && (
        <p class="review-reason entity-affiliation" role="note">
          Affiliation: {t.affiliation}
        </p>
      )}
      <h3>Verification</h3>
      <dl class="facts">
        <Fact label="Position">{v.position || "—"}</Fact>
        <Fact label="Role">{v.role || "—"}</Fact>
        <Fact label="Team">{v.team || "—"}</Fact>
        <Fact label="League">{v.league || "—"}</Fact>
      </dl>
      <h3>Suggestion</h3>
      <Suggestion s={t.suggestion} />
      <h3>Aliases</h3>
      {aliases.length === 0 ? (
        <p class="muted">None proposed.</p>
      ) : (
        <ul class="entity-aliases">
          {aliases.map((a) => (
            <li key={a}>{a}</li>
          ))}
        </ul>
      )}
      <h3>Web sources</h3>
      <WebSources sources={t.web_sources ?? []} />
      <h3>Evidence ({(t.evidence ?? []).length})</h3>
      <Evidence evidence={t.evidence ?? []} />
      {metadata.length > 0 && (
        <details class="review-body">
          <summary>Metadata</summary>
          <dl class="entity-metadata">
            {metadata.map(([k, val]) => (
              <div key={k}>
                <dt>{k}</dt>
                <dd>{val}</dd>
              </div>
            ))}
          </dl>
        </details>
      )}
      <p class="muted review-ids">
        proposal <code>{t.id}</code>
      </p>
    </article>
  );
}

function EntityEntry({ t }: { t: EntityProposalItem }) {
  return (
    <>
      <span class="entry-title">{t.name}</span>
      <span class="entry-meta">
        <KindBadge kind={t.kind} /> {t.type} · {num(t.score)}
      </span>
    </>
  );
}

/** ReviewEntities is the web form of `entities review`, plus merging a possible duplicate from a catalog search. */
export function ReviewEntities() {
  return (
    <LocalOnly>
      <ReviewEntitiesScreen />
    </LocalOnly>
  );
}

/** Picking is the open merge search: the proposal and how its M decision settles. */
interface Picking {
  t: EntityProposalItem;
  settle(outcome: Promise<Notice | void> | void): void;
}

/** alreadyDecided matches the conflict for a proposal that is no longer pending. */
const alreadyDecided = (e: ApiError) => e.code === "conflict" && /^proposal \S+ is already /.test(e.message);

/** ReviewEntitiesProps configures the entity review screen. */
export interface ReviewEntitiesProps {
  /**
   * sessionId limits the queue to the proposals sourced from the articles
   * that editorial cycle session published or rejected (its entity step).
   */
  sessionId?: string;
}

/** ReviewEntitiesScreen is the entity review without the local-mode guard, for embedding. */
export function ReviewEntitiesScreen({ sessionId }: ReviewEntitiesProps = {}) {
  const [items, setItems] = useState<EntityProposalItem[] | null>(null);
  // Skipped and decided proposals stay hidden for this page session, also
  // across refreshes that raced a decision.
  const [hidden, setHidden] = useState<Set<string>>(() => new Set());
  const [notice, setNotice] = useState<Notice | undefined>();
  const [picking, setPicking] = useState<Picking | null>(null);

  const load = useCallback(async () => {
    try {
      const params: Record<string, unknown> = { limit: QUEUE_LIMIT };
      if (sessionId) params.session_id = sessionId;
      const out = await runAction("entities.review_queue", params, false);
      if ("job" in out) throw new Error("unexpected job for entities.review_queue");
      const obj = out.result.object as { items?: EntityProposalItem[] } | undefined;
      setItems(obj?.items ?? []);
      setNotice(undefined);
    } catch (e) {
      setNotice({ kind: "error", text: "Could not load the entity review: " + errorText(e) });
    }
  }, [sessionId]);

  useEffect(() => {
    load();
  }, [load]);

  const hide = (t: EntityProposalItem) => setHidden((h) => new Set(h).add(keyOf(t)));

  /** decide runs one approve or reject; the item leaves the queue unless the server refused the decision. */
  const decide = async (t: EntityProposalItem, name: string, params: Record<string, unknown>): Promise<Notice | void> => {
    try {
      const out = await runAction(name, { id: t.id, ...params }, false);
      hide(t);
      if ("job" in out) return { kind: "info", text: `${t.name}: ${name} started as job ${out.job.id}.` };
      return { kind: "info", text: out.result.text || out.result.summary || `${t.name}: done` };
    } catch (e) {
      if (e instanceof ApiError && (e.code === "not_found" || alreadyDecided(e))) {
        hide(t);
        return { kind: "info", text: `${t.name} (${t.id}) is no longer pending: ${e.message}. Removed from the queue.` };
      }
      throw new Error(errorText(e));
    }
  };

  const decisions: ReviewDecision<EntityProposalItem>[] = [
    { key: "a", label: "Approve", offered: (t) => !isDuplicate(t), onDecide: (t) => decide(t, "entities.approve", {}) },
    {
      key: "n",
      label: "Approve as new",
      offered: isDuplicate,
      onDecide: (t) => decide(t, "entities.approve", { as_new: true }),
    },
    {
      key: "m",
      label: "Merge into…",
      offered: isDuplicate,
      // Settles when the search is closed (no decision) or a result is picked (the merge).
      onDecide: (t) => new Promise((resolve) => setPicking({ t, settle: resolve })),
    },
    { key: "r", label: "Reject", onDecide: (t) => decide(t, "entities.reject", {}) },
  ];

  const pick = (entityId: string) => {
    if (!picking) return;
    setPicking(null);
    picking.settle(decide(picking.t, "entities.approve", { merge_into: entityId }));
  };
  const closePicker = () => {
    if (!picking) return;
    setPicking(null);
    picking.settle();
  };

  return (
    <>
      <h1>Entity review</h1>
      <p class="muted">
        Pending entity proposals, best score first. Approve writes the proposal into the dictionary and entity catalog
        files at once; a possible duplicate is approved as a new entity or merged into an existing one as an alias.
        Skip hides the proposal until the page is reloaded.
      </p>
      {items === null ? (
        notice ? (
          <p class="error" role="alert">
            {notice.text}{" "}
            <button type="button" class="btn" onClick={() => load()}>
              Retry
            </button>
          </p>
        ) : (
          <p class="muted">Loading…</p>
        )
      ) : (
        <ReviewScreen<EntityProposalItem>
          items={items.filter((t) => !hidden.has(keyOf(t)))}
          itemKey={keyOf}
          renderItem={(t) => <EntityItem t={t} />}
          renderListEntry={(t) => <EntityEntry t={t} />}
          decisions={decisions}
          onSkip={hide}
          onRefresh={load}
          notice={notice}
          emptyText="Nothing to review: no entity proposal is pending."
        />
      )}
      <MergePicker
        open={picking !== null}
        initialQuery={picking ? picking.t.suggestion.entity || picking.t.name : ""}
        onPick={pick}
        onClose={closePicker}
      />
    </>
  );
}
