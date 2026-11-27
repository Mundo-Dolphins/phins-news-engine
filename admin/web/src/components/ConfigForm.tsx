import { useState } from "preact/hooks";
import { isMap } from "./configTree";

interface FormProps {
  value: any;
  path: string[];
  comments: Record<string, string>;
  onSet: (path: string[], value: any) => void;
  onRemove: (path: string[]) => void;
}

/**
 * ConfigForm renders a config value as a structured form. Maps render each
 * key with its comment (the YAML rationale) and a control; containers (nested
 * maps and lists) render as collapsible <details> so a long file stays
 * navigable; scalars render as a typed input (checkbox, number, or text).
 * onSet/onRemove are addressed by key path so the screen can diff against the
 * original and send only the changed leaves.
 */
export function ConfigForm(props: FormProps) {
  if (isMap(props.value)) return <MapForm {...props} />;
  if (Array.isArray(props.value)) return <ListForm {...props} />;
  return <ScalarControl value={props.value} onSet={(v) => props.onSet(props.path, v)} />;
}

function MapForm({ value, path, comments, onSet, onRemove }: FormProps) {
  const entries = Object.entries(value);
  return (
    <div class="cfg-map">
      {entries.map(([k, v]) => {
        const p = path.concat(k);
        const comment = comments[p.join("/")];
        const container = isMap(v) || Array.isArray(v);
        return (
          <div class="cfg-field" key={k}>
            <div class="cfg-head">
              <span class="cfg-key">{k}</span>
              {container && (
                <span class="cfg-type">{Array.isArray(v) ? `list · ${v.length}` : "object"}</span>
              )}
              <button class="cfg-x" title="Remove" onClick={() => onRemove(p)}>
                ×
              </button>
            </div>
            {comment && <div class="cfg-comment">{comment}</div>}
            {container ? (
              <details class="cfg-details">
                <ConfigForm value={v} path={p} comments={comments} onSet={onSet} onRemove={onRemove} />
              </details>
            ) : (
              <ScalarControl value={v} onSet={(nv) => onSet(p, nv)} />
            )}
          </div>
        );
      })}
      <AddKeyRow path={path} onSet={onSet} />
    </div>
  );
}

function ListForm({ value, path, onSet, onRemove, comments }: FormProps) {
  return (
    <div class="cfg-list">
      {value.map((v: any, i: number) => {
        const p = path.concat(String(i));
        return (
          <div class="cfg-item" key={i}>
            <span class="cfg-idx">{i}</span>
            <div class="cfg-item-body">
              <ConfigForm value={v} path={p} comments={comments} onSet={onSet} onRemove={onRemove} />
            </div>
            <button class="cfg-x" title="Remove item" onClick={() => onRemove(p)}>
              ×
            </button>
          </div>
        );
      })}
      <button class="cfg-add" onClick={() => onSet(path.concat(String(value.length)), "")}>
        + Add item
      </button>
    </div>
  );
}

function AddKeyRow({ path, onSet }: { path: string[]; onSet: (path: string[], value: any) => void }) {
  const [k, setK] = useState("");
  const [v, setV] = useState("");
  return (
    <div class="cfg-addrow">
      <input class="cfg-addkey" placeholder="new key" value={k} onInput={(e) => setK((e.target as HTMLInputElement).value)} />
      <input class="cfg-addval" placeholder="value" value={v} onInput={(e) => setV((e.target as HTMLInputElement).value)} />
      <button
        class="cfg-add"
        disabled={!k}
        onClick={() => {
          if (k) {
            onSet(path.concat(k), v);
            setK("");
            setV("");
          }
        }}
      >
        Add
      </button>
    </div>
  );
}

function ScalarControl({ value, onSet }: { value: any; onSet: (value: any) => void }) {
  if (typeof value === "boolean") {
    return (
      <input type="checkbox" checked={value} onChange={(e) => onSet((e.target as HTMLInputElement).checked)} />
    );
  }
  if (typeof value === "number") {
    return (
      <input
        class="cfg-input cfg-number"
        type="number"
        value={String(value)}
        onChange={(e) => {
          const t = (e.target as HTMLInputElement).value;
          onSet(t === "" ? null : Number(t));
        }}
      />
    );
  }
  return (
    <input
      class="cfg-input"
      type="text"
      value={value == null ? "" : String(value)}
      onChange={(e) => onSet((e.target as HTMLInputElement).value)}
    />
  );
}
