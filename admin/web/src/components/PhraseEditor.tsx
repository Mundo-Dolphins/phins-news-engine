import { useRef, useState } from "preact/hooks";
import { errorText, useShortcutIsolation } from "./reviewParts";

/** RuleImpact is the impact of an event rule phrase on the review sample. */
export interface RuleImpact {
  /** Generic articles the phrase would move to a type, by type. */
  moved: { type: string; count: number }[];
  /** Up to five titles of the generic articles it would move. */
  examples: string[];
  /** Sample articles whose type would change. */
  matches: number;
  /** Already typed articles whose type would change. */
  retyped: number;
}

/** Assessment is the check verdict and impact of an event rule phrase (`events.rules_queue` item, `events.rules_decide` edit). */
export interface Assessment {
  phrase: string;
  passes: boolean;
  reasons: string[];
  impact: RuleImpact;
}

/** Verdict shows whether a phrase passes the checks and, when it does not, why. */
export function Verdict({ a }: { a: Pick<Assessment, "passes" | "reasons"> }) {
  if (a.passes) {
    return (
      <p class="rule-verdict rule-pass">
        <span class="badge rule-badge-pass">Passes the checks</span>
      </p>
    );
  }
  return (
    <div class="rule-verdict rule-fail" role="note">
      <span class="badge rule-badge-fail">Does not pass</span>
      {a.reasons.length > 0 && (
        <ul class="rule-reasons">
          {a.reasons.map((r, i) => (
            <li key={i}>{r}</li>
          ))}
        </ul>
      )}
    </div>
  );
}

/**
 * ImpactView draws the impact on the sample: the generic articles moved per
 * type — with a bar against the sample size when `sampled` is given — the
 * counts and the example titles.
 */
export function ImpactView({ impact, sampled }: { impact: RuleImpact; sampled?: number }) {
  const max = Math.max(1, sampled ?? 0, ...impact.moved.map((m) => m.count));
  return (
    <div class="rule-impact">
      {impact.moved.length === 0 ? (
        <p class="muted">Moves no generic article.</p>
      ) : (
        <ul class="rule-moves">
          {impact.moved.map((m) => (
            <li key={m.type}>
              <span class="rule-move-label">{`${m.count} generic → ${m.type}`}</span>
              {sampled !== undefined && (
                <span class="rule-bar" aria-hidden="true">
                  <span style={{ width: `${Math.max(2, (100 * m.count) / max)}%` }} />
                </span>
              )}
            </li>
          ))}
        </ul>
      )}
      <p class="rule-counts muted">
        {impact.matches} article{impact.matches === 1 ? "" : "s"} change type
        {sampled !== undefined && <> of {sampled} sampled</>} · {impact.retyped} already typed retyped
      </p>
      {impact.examples.length > 0 && (
        <ul class="rule-examples">
          {impact.examples.map((t, i) => (
            <li key={i}>{t}</li>
          ))}
        </ul>
      )}
    </div>
  );
}

export interface PhraseEditorProps {
  open: boolean;
  proposal: { id: string; phrase: string };
  /** Returns the verdict and impact of the phrase without saving; a thrown error is shown. */
  onPreview(phrase: string): Promise<Assessment>;
  /** Saves the phrase; a thrown error is shown and the editor stays open. */
  onSave(phrase: string): Promise<void>;
  onClose(): void;
}

/**
 * PhraseEditor is the inline editor for an event rule phrase. The field has
 * focus; Enter previews the typed phrase (verdict and impact), Ctrl+Enter or
 * Save saves it — only once the last preview passed for the text as it is
 * now — and Escape closes. While it is open no key press reaches page-level
 * shortcuts, as with `MergePicker`.
 */
export function PhraseEditor({ open, proposal, ...rest }: PhraseEditorProps) {
  if (!open) return null;
  return <EditorPanel key={proposal.id} proposal={proposal} {...rest} />;
}

type Previewed = { text: string; a: Assessment };

function EditorPanel({ proposal, onPreview, onSave, onClose }: Omit<PhraseEditorProps, "open">) {
  const [text, setText] = useState(proposal.phrase);
  const [preview, setPreview] = useState<Previewed | null>(null);
  const [busy, setBusy] = useState<"" | "preview" | "save">("");
  const [error, setError] = useState("");
  const root = useRef<HTMLDivElement>(null);
  const input = useRef<HTMLInputElement>(null);
  // Only the latest preview may fill the panel; one save at a time.
  const seq = useRef(0);
  const saving = useRef(false);

  useShortcutIsolation(root, input, onClose);
  // Focus in a ref callback, so the field has it as soon as it is mounted.
  const mount = (el: HTMLInputElement | null) => {
    if (el && input.current !== el) {
      input.current = el;
      el.focus();
      el.select();
    }
  };

  const current = preview !== null && preview.text === text ? preview : null;
  const canSave = current !== null && current.a.passes && busy === "";

  const runPreview = async () => {
    const phrase = text;
    const mine = ++seq.current;
    setBusy("preview");
    setError("");
    try {
      const a = await onPreview(phrase.trim());
      if (mine === seq.current) setPreview({ text: phrase, a });
    } catch (e) {
      if (mine === seq.current) {
        setPreview(null);
        setError(errorText(e));
      }
    } finally {
      if (mine === seq.current) setBusy("");
    }
  };

  const save = async () => {
    if (!canSave || saving.current) return;
    saving.current = true;
    setBusy("save");
    setError("");
    try {
      await onSave(text.trim());
    } catch (e) {
      setError(errorText(e));
    } finally {
      saving.current = false;
      setBusy("");
    }
  };

  const onKeyDown = (e: KeyboardEvent) => {
    // Nothing typed in the editor may reach the review's shortcuts.
    e.stopPropagation();
    if (e.key === "Escape") {
      e.preventDefault();
      onClose();
    } else if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) {
      e.preventDefault();
      save();
    } else if (e.key === "Enter" && e.target === input.current) {
      e.preventDefault();
      if (busy !== "save") runPreview();
    }
  };

  return (
    <div class="phrase-editor" ref={root} onKeyDown={onKeyDown} role="group" aria-labelledby="phrase-editor-title">
      <h3 id="phrase-editor-title">
        Edit phrase <span class="muted">“{proposal.phrase}”</span>
      </h3>
      <input
        ref={mount}
        type="text"
        aria-label="New phrase"
        autocomplete="off"
        spellcheck={false}
        value={text}
        onInput={(e) => setText((e.currentTarget as HTMLInputElement).value)}
      />
      {error && (
        <p class="error" role="alert">
          {error}
        </p>
      )}
      {busy === "preview" && <p class="muted">Previewing…</p>}
      {preview && (
        <div class={"phrase-preview" + (current ? "" : " stale")}>
          {!current && <p class="muted">The text changed: press Enter to preview it again.</p>}
          <Verdict a={preview.a} />
          <ImpactView impact={preview.a.impact} />
        </div>
      )}
      <div class="row end">
        <span class="muted merge-keys">
          <kbd>Enter</kbd> preview · <kbd>Ctrl</kbd>+<kbd>Enter</kbd> save · <kbd>Esc</kbd> cancel
        </span>
        <button type="button" class="btn" disabled={busy !== ""} onClick={runPreview}>
          Preview
        </button>
        <button type="button" class="btn primary" disabled={!canSave} onClick={save}>
          Save
        </button>
        <button type="button" class="btn" onClick={onClose}>
          Cancel
        </button>
      </div>
    </div>
  );
}
