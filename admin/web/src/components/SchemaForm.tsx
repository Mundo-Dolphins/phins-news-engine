import { useState } from "preact/hooks";
import type { Prop, Schema } from "../api";

type Values = Record<string, unknown>;

function initial(schema: Schema): Values {
  const v: Values = {};
  for (const [k, p] of Object.entries(schema.properties ?? {})) {
    if (p.type === "array") v[k] = Array.isArray(p.default) ? [...p.default] : [];
    else if (p.default !== undefined) v[k] = p.default;
    else if (p.type === "boolean") v[k] = false;
  }
  return v;
}

/** SchemaForm builds a form from an action's JSON Schema and emits typed params. */
export function SchemaForm({
  schema,
  onSubmit,
  submitLabel = "Run",
  busy = false,
}: {
  schema: Schema;
  onSubmit: (params: Values) => void;
  submitLabel?: string;
  busy?: boolean;
}) {
  const props = Object.entries(schema.properties ?? {});
  const required = new Set(schema.required ?? []);
  const [vals, setVals] = useState<Values>(() => initial(schema));
  const [errors, setErrors] = useState<Record<string, string>>({});
  const set = (k: string, v: unknown) => setVals((s) => ({ ...s, [k]: v }));

  const submit = (e: Event) => {
    e.preventDefault();
    const out: Values = {};
    const errs: Record<string, string> = {};
    for (const [k, p] of props) {
      const raw = vals[k];
      let v: unknown;
      if (p.type === "array") {
        const items = ((raw as string[]) ?? []).map((s) => s.trim()).filter((s) => s !== "");
        v = items.length ? items : undefined;
      } else if (p.type === "boolean") {
        // An untouched, defaultless checkbox is omitted so server defaults apply.
        v = raw === true || p.default !== undefined ? raw : undefined;
      } else if (p.type === "integer" || p.type === "number") {
        const s = raw === undefined || raw === null ? "" : String(raw).trim();
        if (s !== "") {
          const n = Number(s);
          if (!Number.isFinite(n) || (p.type === "integer" && !Number.isInteger(n))) errs[k] = `${k} must be ${p.type === "integer" ? "an integer" : "a number"}`;
          else v = n;
        }
      } else {
        const s = raw === undefined || raw === null ? "" : String(raw);
        v = s.trim() === "" ? undefined : s;
      }
      if (v === undefined) {
        if (required.has(k) && !errs[k]) errs[k] = `${k} is required`;
        continue;
      }
      out[k] = v;
    }
    setErrors(errs);
    if (Object.keys(errs).length === 0) onSubmit(out);
  };

  return (
    <form class="schema-form" onSubmit={submit} noValidate>
      {props.map(([k, p]) => (
        <Field key={k} name={k} prop={p} required={required.has(k)} value={vals[k]} error={errors[k]} set={(v) => set(k, v)} />
      ))}
      <button type="submit" class="btn primary" disabled={busy}>
        {submitLabel}
      </button>
    </form>
  );
}

function Field({ name, prop, required, value, error, set }: { name: string; prop: Prop; required: boolean; value: unknown; error?: string; set: (v: unknown) => void }) {
  const id = "f-" + name;
  const label = (
    <label for={id}>
      {name}
      {required && <span class="req" title="required"> *</span>}
    </label>
  );
  let control;
  if (prop.type === "array") {
    const items = (value as string[]) ?? [];
    return (
      <fieldset class="field">
        <legend>
          {name}
          {required && <span class="req"> *</span>}
        </legend>
        {items.map((it, i) => (
          <div class="row" key={i}>
            <input
              aria-label={name + " " + (i + 1)}
              type="text"
              value={it}
              onInput={(e) => set(items.map((x, j) => (j === i ? (e.target as HTMLInputElement).value : x)))}
            />
            <button type="button" class="btn" aria-label={`Remove ${name} ${i + 1}`} onClick={() => set(items.filter((_, j) => j !== i))}>
              ×
            </button>
          </div>
        ))}
        <button type="button" class="btn" onClick={() => set([...items, ""])}>
          Add {name}
        </button>
        {prop.description && <small class="hint">{prop.description}</small>}
        {error && <small class="error">{error}</small>}
      </fieldset>
    );
  }
  if (prop.enum) {
    control = (
      <select id={id} value={String(value ?? "")} onChange={(e) => set((e.target as HTMLSelectElement).value)}>
        {prop.default === undefined && <option value="">{required ? "(select)" : "(default)"}</option>}
        {prop.enum.map((o) => (
          <option key={o} value={o}>
            {o}
          </option>
        ))}
      </select>
    );
  } else if (prop.type === "boolean") {
    return (
      <div class="field check">
        <input id={id} type="checkbox" checked={value === true} onChange={(e) => set((e.target as HTMLInputElement).checked)} />
        <label for={id}>{name}</label>
        {prop.description && <small class="hint">{prop.description}</small>}
        {error && <small class="error">{error}</small>}
      </div>
    );
  } else if (prop.type === "integer" || prop.type === "number") {
    control = (
      <input id={id} type="number" step={prop.type === "integer" ? "1" : "any"} value={value === undefined ? "" : String(value)} onInput={(e) => set((e.target as HTMLInputElement).value)} />
    );
  } else {
    control = <input id={id} type="text" value={String(value ?? "")} onInput={(e) => set((e.target as HTMLInputElement).value)} />;
  }
  return (
    <div class="field">
      {label}
      {control}
      {prop.description && <small class="hint">{prop.description}</small>}
      {error && <small class="error">{error}</small>}
    </div>
  );
}
