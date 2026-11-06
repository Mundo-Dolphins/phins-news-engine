import { useEffect, useState } from "preact/hooks";
import { listAudit, type AuditEntry } from "../api";
import { fmt } from "./Jobs";

export function Audit() {
  const [rows, setRows] = useState<AuditEntry[]>([]);
  const [err, setErr] = useState("");
  useEffect(() => {
    listAudit().then(setRows).catch((e) => setErr(e.message));
  }, []);
  return (
    <>
      <h1>Audit log</h1>
      {err && <p class="error">{err}</p>}
      <div class="table-wrap">
        <table>
          <thead><tr><th>When</th><th>Actor</th><th>Action</th><th>Kind</th><th>Outcome</th><th>Error</th></tr></thead>
          <tbody>
            {rows.map((r) => (
              <tr key={r.id}>
                <td>{fmt(r.at)}</td><td>{r.actor}</td><td>{r.action}</td><td>{r.kind}</td><td>{r.outcome}</td><td>{r.error}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </>
  );
}
