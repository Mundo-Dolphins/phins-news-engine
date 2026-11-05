import { useState } from "preact/hooks";
import type { Result } from "../api";

function cell(v: unknown): string {
  if (v === null || v === undefined) return "";
  return typeof v === "object" ? JSON.stringify(v) : String(v);
}

function compare(a: unknown, b: unknown): number {
  if (typeof a === "number" && typeof b === "number") return a - b;
  return cell(a).localeCompare(cell(b), undefined, { numeric: true });
}

function Table({ columns, rows }: { columns: { key: string; label: string }[]; rows: Record<string, unknown>[] }) {
  const [sort, setSort] = useState<{ key: string; dir: 1 | -1 } | null>(null);
  const shown = sort ? [...rows].sort((x, y) => sort.dir * compare(x[sort.key], y[sort.key])) : rows;
  const toggle = (key: string) => setSort(sort?.key === key && sort.dir === 1 ? { key, dir: -1 } : { key, dir: 1 });
  return (
    <div class="table-wrap">
      <table>
        <thead>
          <tr>
            {columns.map((c) => (
              <th key={c.key} role="columnheader" onClick={() => toggle(c.key)} class="sortable">
                {c.label}
                {sort?.key === c.key ? (sort.dir === 1 ? " ▲" : " ▼") : ""}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {shown.map((r, i) => (
            <tr key={i}>
              {columns.map((c) => (
                <td key={c.key}>{cell(r[c.key])}</td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
      {shown.length === 0 && <p class="muted">No rows.</p>}
    </div>
  );
}

function Tree({ name, value, open }: { name?: string; value: unknown; open?: boolean }) {
  if (value === null || typeof value !== "object") {
    return (
      <div class="tree-leaf">
        {name !== undefined && <span class="tree-key">{name}: </span>}
        <span class="tree-val">{JSON.stringify(value)}</span>
      </div>
    );
  }
  const entries = Array.isArray(value) ? value.map((v, i) => [String(i), v] as const) : Object.entries(value);
  return (
    <details open={open}>
      <summary>
        {name !== undefined && <span class="tree-key">{name}: </span>}
        <span class="muted">{Array.isArray(value) ? `[${entries.length}]` : `{${entries.length}}`}</span>
      </summary>
      <div class="tree-children">
        {entries.map(([k, v]) => (
          <Tree key={k} name={k} value={v} />
        ))}
      </div>
    </details>
  );
}

/** ResultView renders an action result: summary, then table, object tree or text. */
export function ResultView({ result }: { result: Result }) {
  return (
    <div class="result">
      {result.summary && <p class="summary">{result.summary}</p>}
      {result.render === "table" && <Table columns={result.columns ?? []} rows={result.rows ?? []} />}
      {result.render === "object" && <Tree value={result.object} open />}
      {result.render === "text" && <pre>{result.text}</pre>}
    </div>
  );
}
