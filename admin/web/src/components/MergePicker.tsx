import { useEffect, useLayoutEffect, useRef, useState } from "preact/hooks";
import { runAction } from "../api";
import { errorText } from "./reviewParts";

/** CatalogEntity is one result of `entities.catalog_search`. */
export interface CatalogEntity {
  id: string;
  name: string;
  type: string;
  team: string;
  terms: string[];
}

export interface MergePickerProps {
  open: boolean;
  /** Search text when the dialog opens. */
  initialQuery: string;
  onPick(entityId: string): void;
  onClose(): void;
}

/** Typing pauses shorter than this coalesce into one search. */
export const SEARCH_DEBOUNCE_MS = 250;
const SEARCH_LIMIT = 20;

type Status = { kind: "idle" } | { kind: "searching" } | { kind: "done" } | { kind: "error"; text: string };

/**
 * MergePicker is the modal search over the entity catalog (manual and
 * generated) used to merge a possible duplicate into an existing entity.
 * The search field has focus; arrow keys move within the results, Enter
 * picks the highlighted one and Escape closes. While it is open no key
 * press reaches page-level shortcuts: those inside the dialog stop at it,
 * and any other (focus lost to the backdrop) is swallowed and sends focus
 * back to the search field.
 */
export function MergePicker({ open, initialQuery, onPick, onClose }: MergePickerProps) {
  if (!open) return null;
  return <PickerDialog initialQuery={initialQuery} onPick={onPick} onClose={onClose} />;
}

function PickerDialog({ initialQuery, onPick, onClose }: Omit<MergePickerProps, "open">) {
  const [query, setQuery] = useState(initialQuery);
  const [results, setResults] = useState<CatalogEntity[]>([]);
  const [active, setActive] = useState(0);
  const [status, setStatus] = useState<Status>({ kind: "idle" });
  const dialog = useRef<HTMLDivElement>(null);
  const input = useRef<HTMLInputElement>(null);
  const cancel = useRef<HTMLButtonElement>(null);
  // Only the latest search may fill the list.
  const seq = useRef(0);
  const closeRef = useRef(onClose);
  closeRef.current = onClose;

  useLayoutEffect(() => {
    input.current?.focus();
    input.current?.select();
    // Capture on window runs before any page-level keydown listener.
    const guard = (e: KeyboardEvent) => {
      if (dialog.current?.contains(e.target as Node)) return;
      e.stopPropagation();
      e.preventDefault();
      if (e.key === "Escape") closeRef.current();
      else input.current?.focus();
    };
    window.addEventListener("keydown", guard, true);
    return () => window.removeEventListener("keydown", guard, true);
  }, []);

  useEffect(() => {
    const q = query.trim();
    const mine = ++seq.current;
    if (!q) {
      setResults([]);
      setStatus({ kind: "idle" });
      return;
    }
    setStatus({ kind: "searching" });
    const timer = setTimeout(async () => {
      try {
        const out = await runAction("entities.catalog_search", { query: q, limit: SEARCH_LIMIT }, false);
        if (mine !== seq.current) return;
        if ("job" in out) throw new Error("unexpected job for entities.catalog_search");
        const obj = out.result.object as { results?: CatalogEntity[] } | undefined;
        setResults(obj?.results ?? []);
        setActive(0);
        setStatus({ kind: "done" });
      } catch (e) {
        if (mine !== seq.current) return;
        setResults([]);
        setStatus({ kind: "error", text: errorText(e) });
      }
    }, SEARCH_DEBOUNCE_MS);
    return () => clearTimeout(timer);
  }, [query]);

  const onKeyDown = (e: KeyboardEvent) => {
    // Nothing typed in the dialog may reach the review's shortcuts.
    e.stopPropagation();
    if (e.key === "Escape") {
      e.preventDefault();
      onClose();
      return;
    }
    if (e.key === "Tab") {
      // Keep focus inside: the search field and Cancel.
      e.preventDefault();
      (document.activeElement === input.current ? cancel : input).current?.focus();
      return;
    }
    if (e.target !== input.current) return;
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      const step = e.key === "ArrowDown" ? 1 : -1;
      setActive((i) => Math.max(0, Math.min(results.length - 1, i + step)));
    } else if (e.key === "Enter") {
      e.preventDefault();
      // Only a finished search's results are on screen: never pick a stale one.
      const r = status.kind === "done" ? results[active] : undefined;
      if (r) onPick(r.id);
    }
  };

  useEffect(() => {
    dialog.current?.querySelector(".merge-result.active")?.scrollIntoView?.({ block: "nearest" });
  }, [active, results]);

  const optionId = (i: number) => "merge-result-" + i;
  const hasResults = status.kind === "done" && results.length > 0;

  return (
    <div class="overlay" role="presentation" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div
        class="dialog merge-picker"
        role="dialog"
        aria-modal="true"
        aria-labelledby="merge-title"
        ref={dialog}
        onKeyDown={onKeyDown}
      >
        <h2 id="merge-title">Merge into an existing entity</h2>
        <input
          ref={input}
          type="search"
          role="combobox"
          // The suggestions are the listbox below, not a <datalist> (Preact's types ask for `list` on a combobox).
          list={undefined}
          aria-label="Search the entity catalog"
          aria-autocomplete="list"
          aria-expanded={hasResults}
          aria-controls="merge-results"
          aria-activedescendant={hasResults ? optionId(active) : undefined}
          autocomplete="off"
          spellcheck={false}
          placeholder="Name or alias"
          value={query}
          onInput={(e) => setQuery((e.currentTarget as HTMLInputElement).value)}
        />
        <ul class="merge-results" id="merge-results" role="listbox" aria-label="Catalog matches">
          {hasResults &&
            results.map((r, i) => (
              <li
                key={r.id}
                id={optionId(i)}
                role="option"
                aria-selected={i === active}
                class={"merge-result" + (i === active ? " active" : "")}
                // Keep focus in the search field when clicking a result.
                onMouseDown={(e) => e.preventDefault()}
                onMouseEnter={() => setActive(i)}
                onClick={() => onPick(r.id)}
              >
                <span class="merge-name">{r.name}</span>
                <span class="merge-meta">
                  <span class="badge">{r.type}</span>
                  {r.team && <span>{r.team}</span>}
                  <code>{r.id}</code>
                </span>
              </li>
            ))}
        </ul>
        <p class={"merge-status" + (status.kind === "error" ? " error" : " muted")} role="status">
          {status.kind === "idle" && "Type a name or alias to search the catalog."}
          {status.kind === "searching" && "Searching…"}
          {status.kind === "done" && results.length === 0 && "No catalog entity matches."}
          {status.kind === "done" &&
            results.length > 0 &&
            `${results.length} match${results.length === 1 ? "" : "es"}${results.length === SEARCH_LIMIT ? " (first " + SEARCH_LIMIT + ")" : ""}`}
          {status.kind === "error" && "Search failed: " + status.text}
        </p>
        <div class="row end">
          <span class="muted merge-keys">
            <kbd>↑</kbd>/<kbd>↓</kbd> move · <kbd>Enter</kbd> merge · <kbd>Esc</kbd> close
          </span>
          <button type="button" class="btn" ref={cancel} onClick={onClose}>
            Cancel
          </button>
        </div>
      </div>
    </div>
  );
}
