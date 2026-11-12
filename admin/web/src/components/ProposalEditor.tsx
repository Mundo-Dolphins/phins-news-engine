import { useRef, useState } from "preact/hooks";
import { errorText, useShortcutIsolation } from "./reviewParts";

/** EditableProposal is what the editor shows of a dictionary update proposal. */
export interface EditableProposal {
  index: number;
  canonical_key: string;
  category: string;
  detail: string;
}

export interface ProposalEditorProps {
  open: boolean;
  proposal: EditableProposal;
  /** Saves the edit; a thrown error is shown and the editor stays open. */
  onSave(category: string, note: string): Promise<void>;
  onClose(): void;
}

/** editedDetail is the detail an edit records: the note is appended to it, as the terminal review does. */
export function editedDetail(detail: string, note: string): string {
  const n = note.trim();
  if (!n) return detail;
  return detail ? `${detail}; ${n}` : n;
}

/**
 * ProposalEditor is the inline editor of a dictionary update proposal: an
 * edit accepts the proposal with its category replaced (blank keeps it) and
 * a note appended to its detail. The category field has focus; Enter or
 * Ctrl+Enter saves and Escape closes. While it is open no key press reaches
 * page-level shortcuts, as with `PhraseEditor`.
 */
export function ProposalEditor({ open, proposal, ...rest }: ProposalEditorProps) {
  if (!open) return null;
  return <EditorPanel key={proposal.index} proposal={proposal} {...rest} />;
}

function EditorPanel({ proposal, onSave, onClose }: Omit<ProposalEditorProps, "open">) {
  const [category, setCategory] = useState(proposal.category);
  const [note, setNote] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const root = useRef<HTMLDivElement>(null);
  const first = useRef<HTMLInputElement>(null);
  const saving = useRef(false);

  useShortcutIsolation(root, first, onClose);
  // Focus in a ref callback, so the field has it as soon as it is mounted.
  const mount = (el: HTMLInputElement | null) => {
    if (el && first.current !== el) {
      first.current = el;
      el.focus();
      el.select();
    }
  };

  const save = async () => {
    if (saving.current) return;
    saving.current = true;
    setBusy(true);
    setError("");
    try {
      await onSave(category.trim(), note.trim());
    } catch (e) {
      setError(errorText(e));
    } finally {
      saving.current = false;
      setBusy(false);
    }
  };

  const onKeyDown = (e: KeyboardEvent) => {
    // Nothing typed in the editor may reach the review's shortcuts.
    e.stopPropagation();
    if (e.key === "Escape") {
      e.preventDefault();
      onClose();
    } else if (e.key === "Enter" && (e.target as Element).tagName === "INPUT") {
      e.preventDefault();
      save();
    }
  };

  const detail = editedDetail(proposal.detail, note);

  return (
    <div class="phrase-editor proposal-editor" ref={root} onKeyDown={onKeyDown} role="group" aria-labelledby="proposal-editor-title">
      <h3 id="proposal-editor-title">
        Edit proposal <span class="muted">{proposal.canonical_key}</span>
      </h3>
      <div class="proposal-editor-fields">
        <label>
          <span>Category</span>
          <input
            ref={mount}
            type="text"
            aria-label="Category"
            autocomplete="off"
            spellcheck={false}
            value={category}
            onInput={(e) => setCategory((e.currentTarget as HTMLInputElement).value)}
          />
        </label>
        <label>
          <span>Note</span>
          <input
            type="text"
            aria-label="Note"
            autocomplete="off"
            placeholder="appended to the detail"
            value={note}
            onInput={(e) => setNote((e.currentTarget as HTMLInputElement).value)}
          />
        </label>
      </div>
      <p class="proposal-editor-detail">
        <span class="muted">Detail after the edit: </span>
        {detail ? <span>{detail}</span> : <span class="muted">(none)</span>}
      </p>
      {error && (
        <p class="error" role="alert">
          {error}
        </p>
      )}
      <div class="row end">
        <span class="muted merge-keys">
          <kbd>Enter</kbd> save · <kbd>Esc</kbd> cancel
        </span>
        <button type="button" class="btn primary" disabled={busy} onClick={save}>
          Save
        </button>
        <button type="button" class="btn" onClick={onClose}>
          Cancel
        </button>
      </div>
    </div>
  );
}
