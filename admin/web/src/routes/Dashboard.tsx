import { useEffect, useState } from "preact/hooks";
import { getHealth, listJobs, runAction, type Health, type Job } from "../api";
import { JobTable } from "./Jobs";

export function Dashboard() {
  const [health, setHealth] = useState<Health | null>(null);
  const [failed, setFailed] = useState<string>("…");
  const [jobs, setJobs] = useState<Job[]>([]);
  useEffect(() => {
    getHealth().then(setHealth).catch(() => {});
    runAction("claims.list", { state: "failed" }, false)
      .then((o) => setFailed("result" in o ? String(o.result.rows?.length ?? 0) : "?"))
      .catch(() => setFailed("?"));
    listJobs().then((j) => setJobs(j.slice(0, 10))).catch(() => {});
  }, []);
  return (
    <>
      <h1>Dashboard</h1>
      <div class="cards">
        <div class="card"><span class="muted">Mode</span><strong>{health?.mode ?? "…"}</strong></div>
        <div class="card"><span class="muted">Schema</span><strong>{health?.schema ?? "…"}</strong></div>
        <div class="card"><span class="muted">Failed claims</span><strong>{failed}</strong></div>
      </div>
      <h2>Recent jobs</h2>
      <JobTable jobs={jobs} />
    </>
  );
}
