import { ApiError, getJob, jobEvents, runAction, type Job } from "../api";
import type { RefObject } from "preact";
import { useLayoutEffect, useRef } from "preact/hooks";

/** errorText renders an error for a notice, with the server's code when there is one. */
export function errorText(e: unknown): string {
  if (e instanceof ApiError) return `${e.message} (${e.code})`;
  return e instanceof Error ? e.message : String(e);
}

/** when formats an ISO timestamp for a review card; an unparsable value is shown as is. */
export function when(iso: string): string {
  if (!iso) return "";
  const d = new Date(iso);
  return isNaN(d.getTime()) ? iso : d.toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" });
}

/**
 * useShortcutIsolation keeps every key press away from page-level shortcuts
 * while the panel `root` is mounted, as `MergePicker` does: a key outside
 * the panel is swallowed (Escape calls `onEscape`, any other sends focus to
 * `focusTarget`). Keys inside the panel must be stopped by the panel's own
 * keydown handler.
 */
export function useShortcutIsolation(
  root: RefObject<HTMLElement | null>,
  focusTarget: RefObject<HTMLElement | null>,
  onEscape: () => void,
): void {
  const escapeRef = useRef(onEscape);
  escapeRef.current = onEscape;
  useLayoutEffect(() => {
    // Capture on window runs before any page-level keydown listener.
    const guard = (e: KeyboardEvent) => {
      if (root.current?.contains(e.target as Node)) return;
      e.stopPropagation();
      e.preventDefault();
      if (e.key === "Escape") escapeRef.current();
      else focusTarget.current?.focus();
    };
    window.addEventListener("keydown", guard, true);
    return () => window.removeEventListener("keydown", guard, true);
  }, []);
}

/** activeJob reports whether a job status means the job has not finished yet. */
export const activeJob = (status: string) => status === "queued" || status === "running";

/** How often a job is polled once its event stream has failed. */
const POLL_MS = 2000;

/**
 * untilJobEnd resolves with the job once it has finished, following its
 * event stream; when the stream fails it polls the job instead. The open
 * stream's closer is kept in `streams` until the job ends, so a page can
 * close it when it unmounts.
 */
export function untilJobEnd(job: Job, streams: Set<() => void>): Promise<Job> {
  if (!activeJob(job.status)) return Promise.resolve(job);
  return new Promise<Job>((resolve, reject) => {
    // The end may arrive before jobEvents returns its closer.
    let close: (() => void) | null = null;
    let ended = false;
    const finish = () => {
      ended = true;
      if (close) streams.delete(close);
    };
    const poll = async () => {
      try {
        for (;;) {
          const j = await getJob(job.id);
          if (!activeJob(j.status)) return resolve(j);
          await new Promise((r) => setTimeout(r, POLL_MS));
        }
      } catch (e) {
        reject(e);
      }
    };
    close = jobEvents(
      job.id,
      () => {},
      (j) => {
        finish();
        resolve(j);
      },
      () => {
        finish();
        poll();
      },
    );
    if (!ended) streams.add(close);
  });
}

/**
 * CycleStepView is where the editorial cycle stands for one of its review
 * steps: not there (no cycle, or the cycle is at another step), at the
 * step and open, or at the step with the step already finished.
 */
export type CycleStepView = "elsewhere" | "open" | "finished";

/** cycleStepView reads `cycle.state` and tells where the cycle stands for `step`. */
export async function cycleStepView(step: string): Promise<CycleStepView> {
  const out = await runAction("cycle.state", {}, false);
  if ("job" in out) throw new Error("unexpected job for cycle.state");
  const cycle = (out.result.object as { cycle?: { step: string; steps?: Record<string, { status: string }> } | null } | undefined)?.cycle;
  if (!cycle || cycle.step !== step) return "elsewhere";
  const status = cycle.steps?.[step]?.status;
  return status === "done" || status === "skipped" ? "finished" : "open";
}
