import { useEffect, useState } from "preact/hooks";
import { listActions, type CatalogItem } from "../api";
import { KindBadge } from "./ActionPage";

export function Catalog() {
  const [items, setItems] = useState<CatalogItem[]>([]);
  const [q, setQ] = useState("");
  const [err, setErr] = useState("");
  useEffect(() => {
    listActions().then(setItems).catch((e) => setErr(e.message));
  }, []);
  const needle = q.toLowerCase();
  const shown = items.filter((a) => `${a.name} ${a.title} ${a.description}`.toLowerCase().includes(needle));
  const groups = new Map<string, CatalogItem[]>();
  for (const a of shown) groups.set(a.group, [...(groups.get(a.group) ?? []), a]);
  return (
    <>
      <h1>Actions</h1>
      <input type="search" aria-label="Search actions" placeholder="Search actions" value={q} onInput={(e) => setQ((e.target as HTMLInputElement).value)} />
      {err && <p class="error">{err}</p>}
      {[...groups].map(([g, as]) => (
        <section key={g}>
          <h2>{g}</h2>
          <ul class="list">
            {as.map((a) => (
              <li key={a.name}>
                <a href={"/actions/" + a.name}>
                  <strong>{a.title}</strong> <code>{a.name}</code> <KindBadge kind={a.kind} />
                </a>
                <p class="muted">{a.description}</p>
              </li>
            ))}
          </ul>
        </section>
      ))}
      {!err && shown.length === 0 && <p class="muted">No matching actions.</p>}
    </>
  );
}
