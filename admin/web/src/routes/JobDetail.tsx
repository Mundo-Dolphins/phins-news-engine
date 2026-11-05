import { useEffect, useRef, useState } from "preact/hooks";
import { useRoute } from "preact-iso";
import { cancelJob, getJob, jobEvents, type Job, type LogLine } from "../api";
import { ResultView } from "../components/ResultView";
import { fmt } from "./Jobs";

const active = (s: string) => s === "queued" || s === "running";

export function JobDetail() {
  const { params } = useRoute();
  const id = params.id;
  const [job, setJob] = useState<Job | null>(null);
  const [lines, setLines] = useState<LogLine[]>([]);
  const [err, setErr] = useState("");
  const logRef = useRef<HTMLPreElement>(null);

  useEffect(() => {
    setLines([]);
    getJob(id).then(setJob).catch((e) => setErr(e.message));
    return jobEvents(
      id,
      (l) => setLines((ls) => (ls.some((x) => x.seq === l.seq) ? ls : [...ls, l])),
      setJob,
    );
  }, [id]);

  useEffect(() => {
    const el = logRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [lines]);

  if (err) return <p class="error" role="alert">{err}</p>;
  if (!job) return <p class="muted">Loading…</p>;
  return (
    <>
      <h1>{job.action}</h1>
      <p>
        <span class={"badge status-" + job.status}>{job.status}</span> by {job.actor} · created {fmt(job.created_at)}
        {job.finished_at && <> · finished {fmt(job.finished_at)}</>}
      </p>
      {active(job.status) && (
        <button class="btn danger" onClick={() => cancelJob(id).catch((e) => setErr(e.message))}>
          Cancel job
        </button>
      )}
      {job.error && <p class="error">{job.error}</p>}
      <h2>Log</h2>
      <pre class="log" ref={logRef}>{lines.map((l) => l.line).join("\n") || "(no output yet)"}</pre>
      {job.result && (
        <>
          <h2>Result</h2>
          <ResultView result={job.result} />
        </>
      )}
    </>
  );
}
