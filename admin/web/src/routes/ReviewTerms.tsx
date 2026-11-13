import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "preact/hooks";
import { runAction } from "../api";
import { DestructiveConfirm } from "../components/ConfirmDialog";
import { LocalOnly } from "../components/LocalOnly";
import { ReviewScreen, type Notice, type ReviewDecision } from "../components/ReviewScreen";
import { cycleStepView, errorText, useShortcutIsolation } from "../components/reviewParts";
import { safeHref } from "../links";

const STEP = "mined_terms";
const FINISH = "cycle.terms_finish";

/** MinedTermItem is one offered term of `cycle.terms_queue`. */
export interface MinedTermItem {
  term: string;
  support: number;
  first_seen_article: { id: string; title: string; url: string };
  category_guess: string;
  decision: { status: "approved" | "rejected" | string; category: string } | null;
}

interface Queue {
  items: MinedTermItem[];
  categories: string[];
  undecided: number;
  warnings: string[];
}

interface Written {
  added: number;
  entries: number;
  from: string;
  to: string;
}

const keyOf = (t: MinedTermItem) => t.term;
const plural = (n: number, word: string) => `${n} ${word}${n === 1 ? "" : "s"}`;

function DecisionBadge({ d }: { d: NonNullable<MinedTermItem["decision"]> }) {
  const rejected = d.status === "rejected";
  return (
    <span class={"badge cycle-term-" + (rejected ? "rejected" : "approved")}>
      {rejected ? "Rejected" : d.category ? `Approved · ${d.category}` : "Approved"}
    </span>
  );
}

function TermCard({ t, categories }: { t: MinedTermItem; categories: string[] }) {
  const a = t.first_seen_article;
  const href = safeHref(a.url);
  const title = a.title || a.url || a.id;
  const known = categories.includes(t.category_guess);
  return (
    <article class="review-article cycle-term">
      <header>
        <h2>{t.term}</h2>
        <p class="review-meta">
          <span>{"support " + t.support}</span>
          {t.decision && <DecisionBadge d={t.decision} />}
        </p>
      </header>
      <h3>Category guess</h3>
      {t.category_guess ? (
        <p>
          <code>{t.category_guess}</code>{" "}
          {!known && <span class="muted">· not one of the dictionary's categories: approving asks for one</span>}
        </p>
      ) : (
        <p class="muted">No guess: approving asks for a category.</p>
      )}
      <h3>First seen in</h3>
      {!a.id && !a.url ? (
        <p class="muted">No article recorded.</p>
      ) : href ? (
        <p>
          <a href={href} target="_blank" rel="noopener noreferrer" data-review-open>
            {title}
          </a>
        </p>
      ) : (
        <p>
          {title} {a.url && <span class="muted">· not a web link</span>}
        </p>
      )}
    </article>
  );
}

function TermEntry({ t }: { t: MinedTermItem }) {
  return (
    <>
      <span class="entry-title">{t.term}</span>
      <span class="entry-meta">
        {t.support} · {t.decision ? <DecisionBadge d={t.decision} /> : <span class="muted">undecided</span>}
      </span>
    </>
  );
}

interface CategoryPickerProps {
  term: string;
  categories: string[];
  /** Highlighted first when it is one of the categories. */
  guess: string;
  onPick(category: string): void;
  onClose(): void;
}

/**
 * CategoryPicker is the modal choice of a dictionary category for a mined
 * term. The filter field has focus; arrows move, Enter picks, Escape
 * closes. No key press reaches the review's shortcuts while it is open, so
 * typing never decides.
 */
function CategoryPicker({ term, categories, guess, onPick, onClose }: CategoryPickerProps) {
  const [filter, setFilter] = useState("");
  const [active, setActive] = useState(() => Math.max(0, categories.indexOf(guess)));
  const dialog = useRef<HTMLDivElement>(null);
  const input = useRef<HTMLInputElement>(null);
  const cancel = useRef<HTMLButtonElement>(null);
  useShortcutIsolation(dialog, input, onClose);
  useLayoutEffect(() => input.current?.focus(), []);

  const q = filter.trim().toLowerCase();
  const shown = q ? categories.filter((c) => c.toLowerCase().includes(q)) : categories;
  const current = Math.min(active, shown.length - 1);

  const onKeyDown = (e: KeyboardEvent) => {
    // Nothing typed in the dialog may reach the review's shortcuts.
    e.stopPropagation();
    if (e.key === "Escape") {
      e.preventDefault();
      onClose();
    } else if (e.key === "Tab") {
      e.preventDefault();
      (document.activeElement === input.current ? cancel : input).current?.focus();
    } else if (e.target === input.current && (e.key === "ArrowDown" || e.key === "ArrowUp")) {
      e.preventDefault();
      const step = e.key === "ArrowDown" ? 1 : -1;
      setActive(Math.max(0, Math.min(shown.length - 1, current + step)));
    } else if (e.target === input.current && e.key === "Enter") {
      e.preventDefault();
      if (current >= 0) onPick(shown[current]);
    }
  };

  const optionId = (i: number) => "category-option-" + i;
  return (
    <div class="overlay" role="presentation" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div class="dialog merge-picker" role="dialog" aria-modal="true" aria-labelledby="category-title" ref={dialog} onKeyDown={onKeyDown}>
        <h2 id="category-title">Choose a category for “{term}”</h2>
        <input
          ref={input}
          type="search"
          role="combobox"
          list={undefined}
          aria-label="Filter categories"
          aria-autocomplete="list"
          aria-expanded={shown.length > 0}
          aria-controls="category-options"
          aria-activedescendant={current >= 0 ? optionId(current) : undefined}
          autocomplete="off"
          spellcheck={false}
          placeholder="Filter"
          value={filter}
          onInput={(e) => {
            setFilter((e.currentTarget as HTMLInputElement).value);
            setActive(0);
          }}
        />
        <ul class="merge-results" id="category-options" role="listbox" aria-label="Dictionary categories">
          {shown.map((c, i) => (
            <li
              key={c}
              id={optionId(i)}
              role="option"
              aria-selected={i === current}
              class={"merge-result" + (i === current ? " active" : "")}
              onMouseDown={(e) => e.preventDefault()}
              onMouseEnter={() => setActive(i)}
              onClick={() => onPick(c)}
            >
              {c}
            </li>
          ))}
        </ul>
        {shown.length === 0 && (
          <p class="merge-status muted" role="status">
            No category matches.
          </p>
        )}
        <div class="row end">
          <span class="muted merge-keys">
            <kbd>↑</kbd>/<kbd>↓</kbd> move · <kbd>Enter</kbd> choose · <kbd>Esc</kbd> cancel
          </span>
          <button type="button" class="btn" ref={cancel} onClick={onClose}>
            Cancel
          </button>
        </div>
      </div>
    </div>
  );
}

/** ReviewTermsProps are the props of the mined-term review screens. */
export interface ReviewTermsProps {
  /** Called once the dictionary is written (`cycle.terms_finish` succeeded); the wizard moves on with it. */
  onDone?(): void;
}

/** ReviewTerms is the editorial cycle's mined-term review at `/review/terms`, local mode only. */
export function ReviewTerms(props: ReviewTermsProps) {
  return (
    <LocalOnly>
      <ReviewTermsScreen {...props} />
    </LocalOnly>
  );
}

type Phase = { kind: "loading" } | { kind: "elsewhere" } | { kind: "finished"; written?: Written } | { kind: "error"; text: string } | { kind: "open" };

/** Picking is the open category choice: the term, the action it completes and how its decision settles. */
interface Picking {
  t: MinedTermItem;
  action: "approve" | "categorize";
  settle(outcome: Promise<Notice | void> | void): void;
}

/**
 * ReviewTermsScreen is the mined-term step of the editorial cycle: approve
 * (A), reject (R, which adds the term to the mining policy's ignored terms)
 * or categorize then approve (C) each offered term. Decisions are kept when
 * the screen is left; the dictionary is written only by "Finish and write
 * the dictionary", once every term has a decision.
 */
export function ReviewTermsScreen({ onDone }: ReviewTermsProps) {
  const [phase, setPhase] = useState<Phase>({ kind: "loading" });
  const [queue, setQueue] = useState<Queue | null>(null);
  const [notice, setNotice] = useState<Notice | undefined>();
  const [goTo, setGoTo] = useState<{ key: string } | undefined>();
  const [picking, setPicking] = useState<Picking | null>(null);
  const [confirming, setConfirming] = useState(false);
  const [writing, setWriting] = useState(false);
  // sent guards a second Confirm before the re-render: the dictionary is written once.
  const sent = useRef(false);
  const doneRef = useRef(onDone);
  doneRef.current = onDone;

  const load = useCallback(async (first = false): Promise<Queue | null> => {
    try {
      const out = await runAction("cycle.terms_queue", {}, false);
      if ("job" in out) throw new Error("unexpected job for cycle.terms_queue");
      const o = out.result.object as Partial<Queue> | undefined;
      const q: Queue = { items: o?.items ?? [], categories: o?.categories ?? [], undecided: o?.undecided ?? 0, warnings: o?.warnings ?? [] };
      setQueue(q);
      if (first) {
        const next = q.items.find((t) => !t.decision);
        if (next) setGoTo({ key: keyOf(next) });
      }
      return q;
    } catch (e) {
      setNotice({ kind: "error", text: "Could not load the mined terms: " + errorText(e) });
      return null;
    }
  }, []);

  const start = useCallback(async () => {
    setPhase({ kind: "loading" });
    try {
      const view = await cycleStepView(STEP);
      if (view === "elsewhere") return setPhase({ kind: "elsewhere" });
      if (view === "finished") return setPhase({ kind: "finished" });
      setPhase({ kind: "open" });
      await load(true);
    } catch (e) {
      setPhase({ kind: "error", text: errorText(e) });
    }
  }, [load]);

  useEffect(() => {
    start();
  }, [start]);

  const categories = queue?.categories ?? [];

  /** moveOn selects the next undecided term after t (wrapping), if any. */
  const moveOn = (t: MinedTermItem, q: Queue | null) => {
    const items = q?.items ?? [];
    const at = items.findIndex((x) => keyOf(x) === keyOf(t));
    const next = items.find((x, i) => i > at && !x.decision) ?? items.find((x) => !x.decision);
    if (next) setGoTo({ key: keyOf(next) });
  };

  const decide = async (t: MinedTermItem, action: "approve" | "reject" | "categorize", category?: string): Promise<Notice | void> => {
    setNotice(undefined);
    let text: string;
    try {
      const out = await runAction("cycle.terms_decide", { term: t.term, action, ...(category ? { category } : {}) }, false);
      if ("job" in out) throw new Error("unexpected job for cycle.terms_decide");
      const o = out.result.object as { category?: string; notices?: string[] | null } | undefined;
      const outcome = action === "reject" ? `${t.term}: rejected.` : `${t.term}: approved into ${o?.category || category || t.category_guess}.`;
      text = [outcome, ...(o?.notices ?? []).map((n) => n.trim())].join(" ");
    } catch (e) {
      // A refused decision keeps the term where it is.
      load();
      throw new Error(errorText(e));
    }
    moveOn(t, await load());
    return { kind: "info", text };
  };

  /** pick opens the category choice; it settles when a category is chosen (the decision) or the dialog closes. */
  const pick = (t: MinedTermItem, action: Picking["action"]) => new Promise<Notice | void>((resolve) => setPicking({ t, action, settle: resolve }));

  const decisions: ReviewDecision<MinedTermItem>[] = [
    {
      key: "a",
      label: "Approve",
      onDecide: (t) => (categories.includes(t.category_guess) ? decide(t, "approve") : pick(t, "approve")),
    },
    { key: "r", label: "Reject", onDecide: (t) => decide(t, "reject") },
    { key: "c", label: "Categorize", onDecide: (t) => pick(t, "categorize") },
  ];

  /** skip leaves the term undecided and moves to the next one. */
  const skip = (t: MinedTermItem) => {
    const items = queue?.items ?? [];
    const at = items.findIndex((x) => keyOf(x) === keyOf(t));
    if (items.length > 1) setGoTo({ key: keyOf(items[(at + 1) % items.length]) });
  };

  const chosen = (category: string) => {
    if (!picking) return;
    setPicking(null);
    picking.settle(decide(picking.t, picking.action, category));
  };
  const closePicker = () => {
    if (!picking) return;
    setPicking(null);
    picking.settle();
  };

  const finish = async () => {
    if (sent.current) return;
    sent.current = true;
    setConfirming(false);
    setWriting(true);
    setNotice(undefined);
    try {
      const out = await runAction(FINISH, {}, true);
      if ("job" in out) throw new Error("unexpected job for " + FINISH);
      setPhase({ kind: "finished", written: out.result.object as Written });
      doneRef.current?.();
    } catch (e) {
      sent.current = false;
      setNotice({ kind: "error", text: "Could not write the dictionary: " + errorText(e) });
      load();
    } finally {
      setWriting(false);
    }
  };

  const busy = writing || confirming || picking !== null;

  return (
    <>
      <h1>Mined term review</h1>
      <p class="muted">
        Terms mined from the cycle's reviewed articles. <kbd>A</kbd> approve (asks for a category when the guess is not
        one of the dictionary's), <kbd>R</kbd> reject (adds the term to the mining policy's ignored terms),{" "}
        <kbd>C</kbd> choose a category and approve. Decisions are kept when you leave; the dictionary is written only when
        the step is finished.
      </p>
      {phase.kind === "loading" && <p class="muted">Loading…</p>}
      {phase.kind === "elsewhere" && (
        <p class="muted">
          The editorial cycle is not at its mined terms. <a href="/cycle">Open from the editorial cycle</a>.
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
          <p class="rule-outcome-head">The mined-term review is finished.</p>
          {phase.written && (
            <p>
              {phase.written.from && phase.written.to
                ? `Dictionary ${phase.written.from} → ${phase.written.to} · ${plural(phase.written.added, "term")} added`
                : `Dictionary unchanged · ${plural(phase.written.added, "term")} added`}
            </p>
          )}
        </div>
      )}
      {phase.kind === "open" && (
        <>
          {queue && (
            <div class="rules-zone-bar cycle-review-bar">
              <p class="dict-session-line">{`${plural(queue.items.length, "term")} offered · ${queue.undecided} undecided`}</p>
              <span class="spacer" />
              <button type="button" class="btn primary" disabled={queue.undecided > 0 || busy} onClick={() => setConfirming(true)}>
                Finish and write the dictionary
              </button>
            </div>
          )}
          {queue?.warnings.map((w) => (
            <p key={w} class="review-reason" role="note">
              {w}
            </p>
          ))}
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
            <ReviewScreen<MinedTermItem>
              items={queue.items}
              itemKey={keyOf}
              renderItem={(t) => <TermCard t={t} categories={categories} />}
              renderListEntry={(t) => <TermEntry t={t} />}
              decisions={decisions}
              onSkip={skip}
              onRefresh={() => load()}
              notice={notice}
              decisionsDisabled={writing || confirming}
              goTo={goTo}
              emptyText="No mined term is offered in this cycle."
            />
          )}
        </>
      )}
      {picking && (
        <CategoryPicker
          term={picking.t.term}
          categories={categories}
          guess={picking.t.category_guess}
          onPick={chosen}
          onClose={closePicker}
        />
      )}
      {confirming && <DestructiveConfirm name={FINISH} onConfirm={finish} onCancel={() => setConfirming(false)} />}
    </>
  );
}
