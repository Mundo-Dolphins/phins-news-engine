import { render, screen, fireEvent, waitFor, within } from "@testing-library/preact";
import { ApiError, getSession, jobEvents, listJobs, runAction, type Job, type RunOutcome } from "../api";
import { Cycle } from "./Cycle";

vi.mock("../api", async (orig) => ({
  ...(await orig<typeof import("../api")>()),
  runAction: vi.fn(),
  getSession: vi.fn(),
  jobEvents: vi.fn(),
  getJob: vi.fn(),
  listJobs: vi.fn(),
}));

// The embedded review screens have their own tests; here they are stubs
// that show what the wizard handed them.
vi.mock("./ReviewArticles", () => ({
  ReviewArticlesScreen: ({ onDone }: { onDone?(): void }) => (
    <button type="button" onClick={() => onDone?.()}>
      stub article review
    </button>
  ),
}));
vi.mock("./ReviewTerms", () => ({
  ReviewTermsScreen: ({ onDone }: { onDone?(): void }) => (
    <button type="button" onClick={() => onDone?.()}>
      stub term review
    </button>
  ),
}));
vi.mock("./ReviewEntities", () => ({
  ReviewEntitiesScreen: ({ sessionId }: { sessionId?: string }) => <p>stub entity review for {sessionId}</p>,
}));
vi.mock("./ReviewEvents", () => ({
  ReviewEventsScreen: ({
    discoverAction,
    applyAction,
    onJobStart,
    onJobEnd,
  }: {
    discoverAction?: string;
    applyAction?: string;
    onJobStart?(): void;
    onJobEnd?(): void;
  }) => (
    <div>
      <p>
        stub event review with {discoverAction} and {applyAction}
      </p>
      <button type="button" onClick={() => onJobStart?.()}>
        stub job start
      </button>
      <button type="button" onClick={() => onJobEnd?.()}>
        stub job end
      </button>
    </div>
  ),
}));

const run = vi.mocked(runAction);
const events = vi.mocked(jobEvents);
const jobs = vi.mocked(listJobs);

const ORDER = ["sync", "telegram_import", "roster_sync", "article_review", "entities", "event_rules", "mined_terms", "training", "summary"];
const SKIPPABLE = ["sync", "telegram_import", "roster_sync", "entities", "event_rules", "mined_terms", "training"];

interface Step {
  status: string;
  result: unknown;
  error: string;
  updated_at: string;
}

interface StateOver {
  steps?: Record<string, Partial<Step>>;
  available?: { sync: boolean; telegram: boolean };
  disabled?: Record<string, string>;
}

const obj = (object: unknown, summary = ""): RunOutcome => ({ result: { render: "object", object, summary } });

/** state is a cycle.state result: steps before `step` are done, the rest pending. */
function state(step: string | null, over: StateOver = {}) {
  const at = step === null ? -1 : ORDER.indexOf(step);
  const steps: Record<string, Step> = {};
  ORDER.forEach((s, i) => {
    steps[s] = { status: i < at ? "done" : "pending", result: null, error: "", updated_at: "2026-11-13T09:00:00Z", ...over.steps?.[s] };
  });
  return obj({
    cycle:
      step === null
        ? null
        : {
            cycle_id: "20261113T090000-0a0b0c0d",
            started_at: "2026-11-13T09:00:00Z",
            step,
            session_id: "sess-1",
            steps,
            sample_size: 0,
            entities_verification: "local",
          },
    available: over.available ?? { sync: true, telegram: true },
    order: ORDER,
    skippable: SKIPPABLE,
    disabled: over.disabled ?? {},
  });
}

const job = (id: string, action: string, status = "running", extra: Partial<Job> = {}): Job => ({
  id,
  action,
  actor: "ana",
  status,
  created_at: "2026-11-13T09:00:00Z",
  ...extra,
});

type Handler = (params: Record<string, unknown>, confirm: boolean) => Promise<RunOutcome> | RunOutcome;

/** serve answers runAction by name; cycle.state returns the latest state. */
function serve(initial: RunOutcome, more: Record<string, Handler> = {}) {
  let current = initial;
  run.mockImplementation(async (name, params, confirm) => {
    if (name === "cycle.state") return current;
    const h = more[name];
    if (!h) throw new Error("unexpected action " + name);
    return h(params, confirm);
  });
  return { set: (next: RunOutcome) => (current = next) };
}

const calls = (name: string) => run.mock.calls.filter(([n]) => n === name);
const button = (name: string | RegExp) => screen.getByRole("button", { name }) as HTMLButtonElement;
const rail = () => screen.getByRole("list", { name: "Cycle steps" });
const panel = () => screen.getByRole("region", { name: /^Step \d/ });

async function open(initial: RunOutcome, more: Record<string, Handler> = {}) {
  const s = serve(initial, more);
  render(<Cycle />);
  await screen.findByRole("heading", { name: "Editorial cycle" });
  await waitFor(() => expect(calls("cycle.state").length).toBe(1));
  return s;
}

describe("Cycle", () => {
  beforeEach(() => {
    run.mockReset();
    events.mockReset();
    jobs.mockReset();
    jobs.mockResolvedValue([]);
    vi.mocked(getSession).mockResolvedValue({ username: "ana", mode: "local" });
  });

  it("offers the start form when no cycle exists and starts one with its options", async () => {
    const s = await open(state(null), {
      "cycle.start": () => {
        s.set(state("sync"));
        return state("sync");
      },
    });
    fireEvent.input(await screen.findByLabelText("Sample size"), { target: { value: "25" } });
    fireEvent.change(screen.getByLabelText("Entity verification"), { target: { value: "full" } });
    fireEvent.click(button("Start cycle"));
    await waitFor(() => expect(run).toHaveBeenCalledWith("cycle.start", { sample_size: 25, entities_verification: "full" }, false));
    expect(await screen.findByRole("heading", { name: /Sync with the remote host/ })).toBeTruthy();
  });

  it("reopens at the saved step without running the earlier steps", async () => {
    await open(state("event_rules", { steps: { sync: { status: "skipped", result: { skip_reason: "skipped from the admin panel" } } } }));
    const current = await within(rail()).findByText("Event rules");
    expect(current.closest("li")?.getAttribute("aria-current")).toBe("step");
    expect(within(rail()).getAllByRole("listitem")).toHaveLength(9);
    expect(within(rail()).getByText("Sync with the remote host").closest("li")?.textContent).toMatch(/skipped/);
    expect(within(rail()).getByText("Article review").closest("li")?.textContent).toMatch(/done/);
    expect(within(panel()).getByText("stub event review with cycle.event_rules_discover and cycle.event_rules_apply")).toBeTruthy();
    // Only the state was read: nothing earlier ran again.
    expect(run.mock.calls.map(([n]) => n)).toEqual(["cycle.state"]);
  });

  it("keeps Next disabled until the step is done, then moves on", async () => {
    const s = await open(state("roster_sync"), {
      "cycle.roster_sync": () => ({ job: job("j1", "cycle.roster_sync") }),
      "cycle.next": () => {
        s.set(state("article_review"));
        return state("article_review");
      },
    });
    expect(button("Next").disabled).toBe(true);
    events.mockImplementation((_id, onLine, onEnd) => {
      onLine({ seq: 1, at: "2026-11-13T09:01:00Z", line: "roster: 1234 players written" });
      s.set(state("roster_sync", { steps: { roster_sync: { status: "done", result: { players: 1234, path: "configs/roster.json" } } } }));
      onEnd(job("j1", "cycle.roster_sync", "succeeded", { result: { render: "object", summary: "roster synced" } }));
      return () => {};
    });
    fireEvent.click(button("Run roster sync"));
    expect(await screen.findByText(/roster: 1234 players written/)).toBeTruthy();
    await waitFor(() => expect(button("Next").disabled).toBe(false));
    expect(within(panel()).getByText("1234")).toBeTruthy();
    fireEvent.click(button("Next"));
    await waitFor(() => expect(run).toHaveBeenCalledWith("cycle.next", { skip: false, step: "roster_sync" }, false));
    expect(await screen.findByText("stub article review")).toBeTruthy();
  });

  it("offers Skip only on skippable steps", async () => {
    const s = await open(state("article_review"), {
      "cycle.next": (p) => {
        expect(p).toEqual({ skip: true, step: "entities" });
        s.set(state("event_rules"));
        return state("event_rules");
      },
    });
    await screen.findByText("stub article review");
    expect(screen.queryByRole("button", { name: "Skip step" })).toBeNull();
    s.set(state("entities"));
    fireEvent.click(screen.getByText("stub article review"));
    // onDone reloads the state: the cycle is now at a skippable step.
    expect(await screen.findByRole("button", { name: "Skip step" })).toBeTruthy();
    fireEvent.click(button("Skip step"));
    await waitFor(() => expect(run).toHaveBeenCalledWith("cycle.next", { skip: true, step: "entities" }, false));
  });

  it("shows a failed Telegram import's terminal-login message as is, and retries it", async () => {
    const msg = "log in to Telegram from the terminal first (AUTH_KEY_UNREGISTERED)";
    await open(state("telegram_import", { steps: { telegram_import: { status: "failed", error: msg } } }), {
      "cycle.telegram_import": () => ({ job: job("j2", "cycle.telegram_import") }),
    });
    expect(await within(panel()).findByText(msg)).toBeTruthy();
    expect(button("Next").disabled).toBe(true);
    expect(button("Skip step").disabled).toBe(false);
    events.mockImplementation(() => () => {});
    fireEvent.click(button("Retry"));
    await waitFor(() => expect(run).toHaveBeenCalledWith("cycle.telegram_import", {}, false));
  });

  it("says when sync and Telegram are not available on this host and offers only Skip", async () => {
    await open(state("sync", { available: { sync: false, telegram: false }, disabled: { sync: "sync is not available on this host" } }));
    expect(await within(panel()).findByText("Not available on this host")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Run sync" })).toBeNull();
    expect(button("Skip step")).toBeTruthy();
  });

  it("shows a sync refused because other actions are running", async () => {
    await open(state("sync"), {
      "cycle.sync": () => {
        throw new ApiError(409, "conflict", "other actions are running; try again when they finish");
      },
    });
    fireEvent.click(await screen.findByRole("button", { name: "Run sync" }));
    expect(await screen.findByText(/other actions are running; try again when they finish/)).toBeTruthy();
    expect(button("Run sync").disabled).toBe(false);
  });

  it("reloads the state after a sync and shows a database sync in progress clearly", async () => {
    let reads = 0;
    run.mockImplementation(async (name) => {
      if (name === "cycle.state") {
        reads++;
        if (reads === 2) throw new ApiError(409, "conflict", "database sync in progress");
        return reads === 1 ? state("sync") : state("sync", { steps: { sync: { status: "done", result: { source_host: "remote", size_bytes: 42 } } } });
      }
      if (name === "cycle.sync") return { job: job("j3", "cycle.sync") };
      throw new Error("unexpected action " + name);
    });
    events.mockImplementation((_id, _line, onEnd) => {
      onEnd(job("j3", "cycle.sync", "succeeded", { result: { render: "object", summary: "sync complete" } }));
      return () => {};
    });
    render(<Cycle />);
    fireEvent.click(await screen.findByRole("button", { name: "Run sync" }));
    expect(await screen.findByText(/database sync in progress/)).toBeTruthy();
    fireEvent.click(button("Reload"));
    await waitFor(() => expect(button("Next").disabled).toBe(false));
    expect(reads).toBe(3);
  });

  it("follows a step job still running after the page was reopened", async () => {
    jobs.mockResolvedValue([job("j9", "cycle.train", "running")]);
    events.mockImplementation(() => () => {});
    await open(state("training", { steps: { training: { status: "running" } } }));
    await waitFor(() => expect(events).toHaveBeenCalledWith("j9", expect.any(Function), expect.any(Function), expect.any(Function)));
    expect(await screen.findByRole("link", { name: "j9" })).toBeTruthy();
  });

  it("asks before promoting the winner and promotes once", async () => {
    const winner = { version: "v7", algorithm: "logreg", precision: 0.91, recall: 0.8, f1: 0.85, prauc: 0.9, dir: "models/candidates/run-7" };
    const waiting = state("training", { steps: { training: { status: "pending", result: { candidates: 2, winner, no_winner: false, promotion: "" } } } });
    const s = await open(waiting, {
      "cycle.promote": () => {
        s.set(state("training", { steps: { training: { status: "done", result: { candidates: 2, winner, promotion: "promoted", promoted_to: "models/current" } } } }));
        return obj({});
      },
    });
    expect(await within(panel()).findByText(/v7/)).toBeTruthy();
    fireEvent.click(button("Promote"));
    const dialog = screen.getByRole("dialog");
    const confirm = within(dialog).getByRole("button", { name: "Confirm" }) as HTMLButtonElement;
    expect(confirm.disabled).toBe(true);
    fireEvent.input(within(dialog).getByLabelText("Action name"), { target: { value: "cycle.promote" } });
    fireEvent.click(confirm);
    fireEvent.click(confirm);
    await waitFor(() => expect(calls("cycle.promote")).toEqual([["cycle.promote", { promote: true }, true]]));
    expect(await within(panel()).findByText(/promoted/)).toBeTruthy();
  });

  it("asks before declining the winner", async () => {
    const winner = { version: "v7", algorithm: "logreg", precision: 0.91, recall: 0.8, f1: 0.85, prauc: 0.9, dir: "d" };
    await open(state("training", { steps: { training: { status: "pending", result: { candidates: 1, winner, promotion: "" } } } }), {
      "cycle.promote": () => obj({}),
    });
    fireEvent.click(await screen.findByRole("button", { name: "Don't promote" }));
    const dialog = screen.getByRole("dialog");
    expect(calls("cycle.promote")).toHaveLength(0);
    fireEvent.click(within(dialog).getByRole("button", { name: "Don't promote" }));
    await waitFor(() => expect(calls("cycle.promote")).toEqual([["cycle.promote", { promote: false }, true]]));
  });

  it("abandons the cycle only after the typed confirmation", async () => {
    const s = await open(state("entities"), {
      "cycle.abandon": () => {
        s.set(state(null));
        return state(null);
      },
    });
    fireEvent.click(await screen.findByRole("button", { name: "Abandon cycle" }));
    const dialog = screen.getByRole("dialog");
    fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(calls("cycle.abandon")).toHaveLength(0);
    fireEvent.click(button("Abandon cycle"));
    fireEvent.input(within(screen.getByRole("dialog")).getByLabelText("Action name"), { target: { value: "cycle.abandon" } });
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Confirm" }));
    await waitFor(() => expect(calls("cycle.abandon")).toEqual([["cycle.abandon", {}, true]]));
    expect(await screen.findByRole("button", { name: "Start cycle" })).toBeTruthy();
  });

  it("runs the entity discovery first, then embeds the session's entity review and finishes it", async () => {
    const s = await open(state("entities"), {
      "cycle.entities_discover": () => ({ job: job("j4", "cycle.entities_discover") }),
      "cycle.entities_finish": () => {
        s.set(state("entities", { steps: { entities: { status: "done", result: { articles: 3, approved: 1, rejected: 0, discovered: true } } } }));
        return obj({}, "Entity review complete. approved: 1, rejected: 0");
      },
    });
    expect(screen.queryByText(/stub entity review/)).toBeNull();
    events.mockImplementation((_id, _line, onEnd) => {
      s.set(state("entities", { steps: { entities: { status: "pending", result: { articles: 3, proposed: 2, discovered: true } } } }));
      onEnd(job("j4", "cycle.entities_discover", "succeeded", { result: { render: "object", summary: "entities: 3 articles" } }));
      return () => {};
    });
    fireEvent.click(await screen.findByRole("button", { name: "Discover entities" }));
    expect(await screen.findByText("stub entity review for sess-1")).toBeTruthy();
    fireEvent.click(button("Finish entity review"));
    await waitFor(() => expect(calls("cycle.entities_finish")).toHaveLength(1));
    await waitFor(() => expect(button("Next").disabled).toBe(false));
  });

  it("finishes the event rules step", async () => {
    await open(state("event_rules"), { "cycle.event_rules_finish": () => obj({}) });
    fireEvent.click(await screen.findByRole("button", { name: "Finish event rules review" }));
    await waitFor(() => expect(run).toHaveBeenCalledWith("cycle.event_rules_finish", {}, false));
  });

  it("prepares the mined terms before showing their review", async () => {
    const s = await open(state("mined_terms"), {
      "cycle.terms_prepare": () => {
        s.set(state("mined_terms", { steps: { mined_terms: { result: { prepared: true } } } }));
        return obj({}, "session sess-1: 4 candidate term(s) mined");
      },
    });
    expect(await screen.findByText("stub term review")).toBeTruthy();
    const names = run.mock.calls.map(([n]) => n);
    expect(names.indexOf("cycle.terms_prepare")).toBeGreaterThan(-1);
    expect(calls("cycle.terms_prepare")).toHaveLength(1);
  });

  it("shows the summary with the deploy note and finishes the cycle", async () => {
    const s = await open(state("summary", { steps: { summary: { status: "done" } } }), {
      "cycle.summary": () =>
        ({ result: { render: "object", object: { report: {}, markdown: "## Cycle report\n12 articles reviewed", deploy: "run the deploy from the terminal" } } }),
      "cycle.next": () => {
        s.set(state(null));
        return obj({ ...(state(null) as { result: { object: object } }).result.object, audit_report: "data/audit/cycle.json" });
      },
    });
    expect(await screen.findByText(/12 articles reviewed/)).toBeTruthy();
    expect(screen.getByText(/Deploy from the terminal/)).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Skip step" })).toBeNull();
    fireEvent.click(button("Finish cycle"));
    await waitFor(() => expect(run).toHaveBeenCalledWith("cycle.next", { skip: false, step: "summary" }, false));
    expect(await screen.findByText(/data\/audit\/cycle.json/)).toBeTruthy();
    expect(screen.getByRole("button", { name: "Start cycle" })).toBeTruthy();
  });

  it("offers Retry for a step left running by an earlier panel process, with Abandon reachable", async () => {
    await open(state("roster_sync", { steps: { roster_sync: { status: "running" } } }), {
      "cycle.roster_sync": () => ({ job: job("j6", "cycle.roster_sync") }),
    });
    expect(await screen.findByText(/left running by an earlier panel process/)).toBeTruthy();
    expect(button("Next").disabled).toBe(true);
    expect(button("Skip step").disabled).toBe(false);
    expect(button("Abandon cycle").disabled).toBe(false);
    events.mockImplementation(() => () => {});
    fireEvent.click(button("Retry"));
    await waitFor(() => expect(run).toHaveBeenCalledWith("cycle.roster_sync", {}, false));
  });

  it("is not stuck when reopened at event rules left running with no job", async () => {
    await open(state("event_rules", { steps: { event_rules: { status: "running" } } }));
    expect(await screen.findByText(/left running by an earlier panel process/)).toBeTruthy();
    expect(button("Skip step").disabled).toBe(false);
    expect(button("Abandon cycle").disabled).toBe(false);
    expect(button("Finish event rules review").disabled).toBe(false);
  });

  it("follows an event rules job still running after a reopen and reloads the state when it ends", async () => {
    jobs.mockResolvedValue([job("j7", "cycle.event_rules_apply", "running")]);
    let end: (j: Job) => void = () => {};
    events.mockImplementation((_id, _line, onEnd) => {
      end = onEnd;
      return () => {};
    });
    const s = await open(state("event_rules", { steps: { event_rules: { status: "running" } } }));
    await waitFor(() => expect(events).toHaveBeenCalledWith("j7", expect.any(Function), expect.any(Function), expect.any(Function)));
    expect(button("Abandon cycle").disabled).toBe(true);
    expect(button("Finish event rules review").disabled).toBe(true);
    s.set(state("event_rules", { steps: { event_rules: { status: "pending", result: { applied: 2 } } } }));
    end(job("j7", "cycle.event_rules_apply", "succeeded", { result: { render: "object", summary: "PASS: 2 rule(s) applied" } }));
    await waitFor(() => expect(calls("cycle.state").length).toBe(2));
    await waitFor(() => expect(button("Finish event rules review").disabled).toBe(false));
  });

  it("disables Finish while the embedded event rule job runs and reloads the state when it ends", async () => {
    await open(state("event_rules"));
    fireEvent.click(await screen.findByRole("button", { name: "stub job start" }));
    await waitFor(() => expect(button("Finish event rules review").disabled).toBe(true));
    expect(button("Next").disabled).toBe(true);
    fireEvent.click(button("stub job end"));
    await waitFor(() => expect(calls("cycle.state").length).toBe(2));
    expect(button("Finish event rules review").disabled).toBe(false);
  });

  it("shows a failed event rules apply", async () => {
    await open(state("event_rules", { steps: { event_rules: { status: "failed", error: "event rules: clustering gate dataset not found" } } }));
    expect(await within(panel()).findByText("event rules: clustering gate dataset not found")).toBeTruthy();
    expect(button("Finish event rules review")).toBeTruthy();
  });

  it("hides Skip while the training winner waits and blocks a second decision while one is in flight", async () => {
    const winner = { version: "v7", algorithm: "logreg", precision: 0.91, recall: 0.8, f1: 0.85, prauc: 0.9, dir: "d" };
    let answer: (o: RunOutcome) => void = () => {};
    await open(state("training", { steps: { training: { status: "pending", result: { candidates: 1, winner, promotion: "" } } } }), {
      "cycle.promote": () => new Promise<RunOutcome>((r) => (answer = r)),
    });
    await screen.findByRole("button", { name: "Promote" });
    expect(screen.queryByRole("button", { name: "Skip step" })).toBeNull();
    fireEvent.click(button("Don't promote"));
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Don't promote" }));
    await waitFor(() => expect(calls("cycle.promote")).toHaveLength(1));
    expect(button("Promote").disabled).toBe(true);
    expect(button("Don't promote").disabled).toBe(true);
    answer(obj({}));
  });
});
