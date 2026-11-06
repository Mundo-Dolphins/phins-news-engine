import { useEffect, useState } from "preact/hooks";
import { listJobs, type Job } from "../api";

export function fmt(t?: string): string {
  return t ? new Date(t).toLocaleString() : "";
}

export function JobTable({ jobs }: { jobs: Job[] }) {
  if (jobs.length === 0) return <p class="muted">No jobs yet.</p>;
  return (
    <div class="table-wrap">
      <table>
        <thead><tr><th>Action</th><th>Status</th><th>Actor</th><th>Created</th></tr></thead>
        <tbody>
          {jobs.map((j) => (
            <tr key={j.id}>
              <td><a href={"/jobs/" + j.id}>{j.action}</a></td>
              <td><span class={"badge status-" + j.status}>{j.status}</span></td>
              <td>{j.actor}</td>
              <td>{fmt(j.created_at)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

export function Jobs() {
  const [jobs, setJobs] = useState<Job[]>([]);
  const [err, setErr] = useState("");
  useEffect(() => {
    listJobs().then(setJobs).catch((e) => setErr(e.message));
  }, []);
  return (
    <>
      <h1>Jobs</h1>
      {err && <p class="error">{err}</p>}
      <JobTable jobs={jobs} />
    </>
  );
}
