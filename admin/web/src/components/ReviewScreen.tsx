import type { JSX } from "preact";
import { useEffect, useLayoutEffect, useRef, useState } from "preact/hooks";

/** Notice is a one-line message shown above the review. */
export interface Notice {
  kind: "info" | "error";
  text: string;
}

/** ReviewDecision is one decision button with its keyboard shortcut. */
export interface ReviewDecision<T> {
  /** Single-letter shortcut, matched case-insensitively. */
  key: string;
  label: string;
  /** Applies the decision; a returned notice is shown, a thrown error is shown as an error notice. */
  onDecide(t: T): Promise<Notice | void>;
}

export interface ReviewScreenProps<T> {
  items: T[];
  itemKey(t: T): string;
  renderItem(t: T): JSX.Element;
  renderListEntry(t: T): JSX.Element;
  decisions: ReviewDecision<T>[];
  onSkip(t: T): void;
  onRefresh(): void;
  notice?: Notice;
  emptyText: string;
}

function inField(target: EventTarget | null): boolean {
  const el = target as Element | null;
  return !!el?.closest?.("input, textarea, select, [contenteditable]:not([contenteditable='false'])");
}

function NoticeLine({ notice }: { notice: Notice }) {
  return (
    <p class={"notice notice-" + notice.kind} role={notice.kind === "error" ? "alert" : "status"}>
      {notice.text}
    </p>
  );
}

/**
 * ReviewScreen is the shared review layout: the queue on the left, the
 * selected item in the centre and a decision bar under it. It owns the
 * selection, the keyboard shortcuts (J/K or arrows to move, S skip, O open
 * original, plus each decision's key), the refresh when the tab becomes
 * visible again and the empty state. Removing decided or skipped items is
 * the caller's job: when the selected item leaves `items`, the item now at
 * its position is selected.
 *
 * O clicks the first `a[data-review-open]` inside the rendered item, so a
 * renderer marks its link to the original with that attribute.
 */
export function ReviewScreen<T>(props: ReviewScreenProps<T>) {
  const { items, itemKey, decisions } = props;
  const [selected, setSelected] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  // busyRef guards against a second key press landing before the re-render.
  const busyRef = useRef(false);
  const [local, setLocal] = useState<Notice | null>(null);
  const lastIndex = useRef(0);
  const panel = useRef<HTMLElement>(null);
  const queue = useRef<HTMLOListElement>(null);

  let index = selected === null ? -1 : items.findIndex((t) => itemKey(t) === selected);
  if (index < 0) index = Math.min(lastIndex.current, items.length - 1);
  lastIndex.current = Math.max(index, 0);
  const current = index >= 0 ? items[index] : undefined;
  const currentKey = current === undefined ? null : itemKey(current);

  useEffect(() => {
    queue.current?.querySelector(".review-entry.selected")?.scrollIntoView?.({ block: "nearest" });
  }, [currentKey]);

  const select = (i: number) => {
    if (items.length === 0) return;
    const next = Math.max(0, Math.min(items.length - 1, i));
    lastIndex.current = next;
    setSelected(itemKey(items[next]));
  };

  const decide = async (d: ReviewDecision<T>) => {
    if (current === undefined || busyRef.current) return;
    busyRef.current = true;
    setBusy(true);
    setLocal(null);
    try {
      setLocal((await d.onDecide(current)) || null);
    } catch (e) {
      setLocal({ kind: "error", text: e instanceof Error ? e.message : String(e) });
    } finally {
      busyRef.current = false;
      setBusy(false);
    }
  };

  const skip = () => {
    if (current === undefined || busyRef.current) return;
    setLocal(null);
    props.onSkip(current);
  };

  const onKey = (e: KeyboardEvent) => {
    if (e.ctrlKey || e.metaKey || e.altKey || e.defaultPrevented || inField(e.target)) return;
    const key = e.key.toLowerCase();
    if (key === "j" || key === "arrowdown") select(index + 1);
    else if (key === "k" || key === "arrowup") select(index - 1);
    else if (key === "s") skip();
    else if (key === "o") panel.current?.querySelector<HTMLAnchorElement>("a[data-review-open]")?.click();
    else {
      const d = decisions.find((x) => x.key.toLowerCase() === key);
      if (!d) return;
      decide(d);
    }
    e.preventDefault();
  };
  const keyRef = useRef(onKey);
  keyRef.current = onKey;
  const refreshRef = useRef(props.onRefresh);
  refreshRef.current = props.onRefresh;

  // Layout effect: the shortcuts work as soon as the screen is painted.
  useLayoutEffect(() => {
    const keydown = (e: KeyboardEvent) => keyRef.current(e);
    const visible = () => {
      if (document.visibilityState === "visible") refreshRef.current();
    };
    window.addEventListener("keydown", keydown);
    document.addEventListener("visibilitychange", visible);
    return () => {
      window.removeEventListener("keydown", keydown);
      document.removeEventListener("visibilitychange", visible);
    };
  }, []);

  return (
    <div class="review">
      {props.notice && <NoticeLine notice={props.notice} />}
      {local && <NoticeLine notice={local} />}
      {current === undefined ? (
        <p class="review-empty">{props.emptyText}</p>
      ) : (
        <div class="review-grid">
          <ol class="review-queue" ref={queue} aria-label="Queue">
            {items.map((t, i) => (
              <li key={itemKey(t)}>
                <button
                  type="button"
                  class={"review-entry" + (i === index ? " selected" : "")}
                  aria-current={i === index ? "true" : undefined}
                  onClick={() => select(i)}
                >
                  {props.renderListEntry(t)}
                </button>
              </li>
            ))}
          </ol>
          <section class="review-item" ref={panel} aria-label="Selected item">
            <div class="review-item-body">{props.renderItem(current)}</div>
            <div class="review-bar">
              {decisions.map((d, i) => (
                <button
                  key={d.key}
                  type="button"
                  class={"btn" + (i === 0 ? " primary" : "")}
                  disabled={busy}
                  onClick={() => decide(d)}
                >
                  {d.label} <kbd>{d.key.toUpperCase()}</kbd>
                </button>
              ))}
              <button type="button" class="btn" disabled={busy} onClick={skip}>
                Skip <kbd>S</kbd>
              </button>
              <span class="spacer" />
              <span class="muted review-keys">
                <kbd>J</kbd>/<kbd>K</kbd> move · <kbd>O</kbd> open original · {index + 1} of {items.length}
              </span>
            </div>
          </section>
        </div>
      )}
    </div>
  );
}
