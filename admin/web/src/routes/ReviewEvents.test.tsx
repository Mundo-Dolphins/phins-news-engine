import { render, screen, fireEvent, waitFor, within } from "@testing-library/preact";
import { ApiError, getSession, jobEvents, runAction, type Job, type RunOutcome } from "../api";
import { ReviewEvents } from "./ReviewEvents";

vi.mock("../api", async (orig) => ({
  ...(await orig<typeof import("../api")>()),
  runAction: vi.fn(),
  getSession: vi.fn(),
  jobEvents: vi.fn(),
}));
const run = vi.mocked(runAction);
const events = vi.mocked(jobEvents);

const impact = (count = 3) => ({
  moved: [{ type: "sign", count }],
  examples: ["Ann Example agrees to terms with Team One"],
  matches: count,
  retyped: 0,
});

function rule(over: Record<string, unknown> = {}) {
  return {
    id: "r1",
    target_kind: "action",
    target: "sign",
    phrase: "agrees to terms",
    prompt_version: "v2",
    passes: true,
    reasons: [],
    impact: impact(),
    evidence: [
      { article_id: "a1", title: "Ann Example agrees to terms", url: "https://news.example.com/ann" },
      { article_id: "a2", title: "Purged story", url: "" },
    ],
    ...over,
  };
}

const failing = (over: Record<string, unknown> = {}) =>
  rule({
    id: "r2",
    phrase: "the",
    target: "trade",
    passes: false,
    reasons: ["phrase is a stopword", "retypes 4 typed article(s)"],
    ...over,
  });

interface QueueShape {
  items?: unknown[];
  no_evidence?: unknown[];
  approved?: unknown[];
}

const obj = (object: unknown, summary = ""): RunOutcome => ({ result: { render: "object", object, summary } });

function queueResult(q: QueueShape) {
  return obj({ sampled: 60, items: q.items ?? [], no_evidence: q.no_evidence ?? [], approved: q.approved ?? [] });
}

const job = (id: string, action: string, status = "queued", result?: unknown): Job => ({
  id,
  action,
  actor: "ana",
  status,
  created_at: "2026-11-10T10:00:00Z",
  ...(result ? { result: result as Job["result"] } : {}),
});

type Handler = (params: Record<string, unknown>) => Promise<RunOutcome> | RunOutcome;

/** serve answers runAction by action name; events.rules_queue returns the latest queue. */
function serve(q: QueueShape, more: Record<string, Handler> = {}) {
  let current = q;
  run.mockImplementation(async (name, params) => {
    if (name === "events.rules_queue") return queueResult(current);
    const h = more[name];
    if (!h) throw new Error("unexpected action " + name);
    return h(params);
  });
  return { setQueue: (next: QueueShape) => (current = next) };
}

const calls = (name: string) => run.mock.calls.filter(([n]) => n === name);
const decideCalls = () => calls("events.rules_decide");

async function open(q: QueueShape, more: Record<string, Handler> = {}) {
  const s = serve(q, more);
  render(<ReviewEvents />);
  await screen.findByRole("heading", { name: "Event rule review" });
  await waitFor(() => expect(calls("events.rules_queue").length).toBe(1));
  return s;
}

const card = () => screen.getByRole("region", { name: "Selected item" });

describe("ReviewEvents", () => {
  beforeEach(() => {
    run.mockReset();
    events.mockReset();
    vi.mocked(getSession).mockResolvedValue({ username: "ana", mode: "local" });
  });

  it("renders a passing proposal: target, phrase, impact, verdict and evidence", async () => {
    await open({ items: [rule()] });
    expect(await screen.findByRole("heading", { name: /agrees to terms/ })).toBeTruthy();
    const c = card();
    expect(within(c).getByText("Passes the checks")).toBeTruthy();
    expect(within(c).getByText(/generic → sign/)).toBeTruthy();
    expect(within(c).getByText("Ann Example agrees to terms with Team One")).toBeTruthy();
    const ev = within(c).getByRole("link", { name: "Ann Example agrees to terms" });
    expect(ev.getAttribute("href")).toBe("https://news.example.com/ann");
    expect(ev.hasAttribute("data-review-open")).toBe(true);
    expect(within(c).getByText("Purged story")).toBeTruthy();
    expect(within(c).queryByRole("link", { name: "Purged story" })).toBeNull();
    expect(within(c).getByRole("button", { name: "Accept A" })).toBeTruthy();
  });

  it("accepts a passing proposal with A and reloads the queue", async () => {
    const s = await open({ items: [rule()] }, { "events.rules_decide": () => obj({ id: "r1", status: "approved" }, 'Event rule "agrees to terms" approved.') });
    await screen.findByRole("heading", { name: /agrees to terms/ });
    s.setQueue({ approved: [{ id: "r1", target_kind: "action", target: "sign", phrase: "agrees to terms", decided_at: "2026-11-10T10:00:00Z" }] });
    fireEvent.keyDown(window, { key: "a" });
    await waitFor(() => expect(run).toHaveBeenCalledWith("events.rules_decide", { id: "r1", decision: "accept" }, false));
    expect(await screen.findByText('Event rule "agrees to terms" approved.')).toBeTruthy();
    await waitFor(() => expect(calls("events.rules_queue").length).toBe(2));
    const apply = screen.getByRole("region", { name: /Apply/ });
    expect(await within(apply).findByText(/agrees to terms/)).toBeTruthy();
  });

  it("flags a failing proposal with its reasons and offers no Accept; A does nothing", async () => {
    await open({ items: [failing()] });
    await screen.findByRole("heading", { name: /“the”/ });
    const c = card();
    expect(within(c).getByText("Does not pass")).toBeTruthy();
    expect(within(c).getByText("phrase is a stopword")).toBeTruthy();
    expect(within(c).getByText("retypes 4 typed article(s)")).toBeTruthy();
    expect(within(c).queryByRole("button", { name: /Accept/ })).toBeNull();
    fireEvent.keyDown(window, { key: "a" });
    await new Promise((r) => setTimeout(r, 20));
    expect(decideCalls()).toEqual([]);
    expect(screen.getByRole("list", { name: "Queue" }).textContent).toContain("does not pass");
  });

  it("rejects with R", async () => {
    await open({ items: [failing(), rule()] }, { "events.rules_decide": () => obj({ id: "r2", status: "rejected" }, 'Event rule "the" rejected.') });
    await screen.findByRole("heading", { name: /“the”/ });
    fireEvent.keyDown(window, { key: "r" });
    await waitFor(() => expect(run).toHaveBeenCalledWith("events.rules_decide", { id: "r2", decision: "reject" }, false));
    expect(await screen.findByRole("heading", { name: /agrees to terms/ })).toBeTruthy();
    expect(screen.getByText('Event rule "the" rejected.')).toBeTruthy();
  });

  it("rejects all failing proposals after confirming, posting once", async () => {
    await open(
      { items: [failing(), failing({ id: "r3", phrase: "a" }), rule()] },
      { "events.rules_reject_failing": () => obj({ rejected: [{ id: "r2", phrase: "the", reasons: ["phrase is a stopword"] }, { id: "r3", phrase: "a", reasons: ["x"] }] }, "2 failing proposal(s) rejected.") },
    );
    await screen.findByRole("heading", { name: /“the”/ });
    fireEvent.click(screen.getByRole("button", { name: "Reject all failing (2)" }));
    const dialog = screen.getByRole("dialog");
    expect(calls("events.rules_reject_failing")).toEqual([]);
    // The dialog isolates the review shortcuts.
    fireEvent.keyDown(document.body, { key: "r" });
    await new Promise((r) => setTimeout(r, 20));
    expect(decideCalls()).toEqual([]);
    const confirm = within(dialog).getByRole("button", { name: "Reject 2" });
    fireEvent.click(confirm);
    fireEvent.click(confirm);
    await waitFor(() => expect(calls("events.rules_reject_failing").length).toBe(1));
    expect(run).toHaveBeenCalledWith("events.rules_reject_failing", {}, false);
    expect(await screen.findByText(/2 failing proposal\(s\) rejected/)).toBeTruthy();
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(screen.getByRole("heading", { name: /agrees to terms/ })).toBeTruthy();
    expect(screen.queryByRole("button", { name: /Reject all failing/ })).toBeNull();
  });

  it("cancels reject all failing without posting", async () => {
    await open({ items: [failing()] });
    await screen.findByRole("heading", { name: /“the”/ });
    fireEvent.click(screen.getByRole("button", { name: "Reject all failing (1)" }));
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Cancel" }));
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(calls("events.rules_reject_failing")).toEqual([]);
  });

  it("edits with E: typing fires nothing, Enter previews, Save waits for a passing preview", async () => {
    const decide = vi.fn((p: Record<string, unknown>) => {
      if (p.preview) {
        const passes = p.phrase === "agrees to terms with";
        return obj({ id: "", phrase: p.phrase, passes, reasons: passes ? [] : ["matches 1 article(s)"], impact: impact(2), preview: true });
      }
      return obj({ id: "r9", phrase: p.phrase, passes: true, reasons: [], impact: impact(2), preview: false }, "replaced and approved");
    });
    await open({ items: [rule(), failing()] }, { "events.rules_decide": decide });
    await screen.findByRole("heading", { name: /agrees to terms/ });
    fireEvent.keyDown(window, { key: "e" });
    const input = (await screen.findByRole("textbox", { name: /New phrase/ })) as HTMLInputElement;
    expect(input.value).toBe("agrees to terms");
    expect(document.activeElement).toBe(input);
    for (const key of ["r", "a", "s"]) fireEvent.keyDown(input, { key });
    fireEvent.input(input, { target: { value: "ras" } });
    fireEvent.keyDown(document.body, { key: "r" });
    await new Promise((r) => setTimeout(r, 20));
    expect(decideCalls()).toEqual([]);
    const save = () => screen.getByRole("button", { name: /Save/ }) as HTMLButtonElement;
    expect(save().disabled).toBe(true);
    fireEvent.keyDown(input, { key: "Enter" });
    await waitFor(() =>
      expect(run).toHaveBeenCalledWith("events.rules_decide", { id: "r1", decision: "edit", phrase: "ras", preview: true }, false),
    );
    expect(await screen.findByText("matches 1 article(s)")).toBeTruthy();
    expect(save().disabled).toBe(true);
    fireEvent.input(input, { target: { value: "agrees to terms with" } });
    fireEvent.keyDown(input, { key: "Enter" });
    await waitFor(() => expect(save().disabled).toBe(false));
    fireEvent.click(save());
    await waitFor(() =>
      expect(run).toHaveBeenCalledWith(
        "events.rules_decide",
        { id: "r1", decision: "edit", phrase: "agrees to terms with", preview: false },
        false,
      ),
    );
    expect(await screen.findByText("replaced and approved")).toBeTruthy();
    expect(screen.queryByRole("textbox", { name: /New phrase/ })).toBeNull();
    expect(decideCalls().filter(([, p]) => p.decision !== "edit")).toEqual([]);
  });

  it("closes the editor with Escape without deciding", async () => {
    await open({ items: [rule()] });
    await screen.findByRole("heading", { name: /agrees to terms/ });
    fireEvent.click(screen.getByRole("button", { name: /Edit phrase/ }));
    const input = await screen.findByRole("textbox", { name: /New phrase/ });
    fireEvent.keyDown(input, { key: "Escape" });
    expect(screen.queryByRole("textbox", { name: /New phrase/ })).toBeNull();
    expect(decideCalls()).toEqual([]);
  });

  it("lists the proposals without evidence collapsed", async () => {
    await open({ items: [rule()], no_evidence: [{ id: "r5", target_kind: "signal", target: "injury", phrase: "out for season" }] });
    const summary = await screen.findByText(/1 proposal without evidence/);
    const details = summary.closest("details")!;
    expect(details.open).toBe(false);
    expect(within(details).getByText(/out for season/)).toBeTruthy();
  });

  it("rejects an approved rule from the apply list", async () => {
    const approved = [{ id: "r7", target_kind: "action", target: "sign", phrase: "inks deal", decided_at: "2026-11-09T10:00:00Z" }];
    await open({ approved }, { "events.rules_decide": () => obj({ id: "r7", status: "rejected" }, 'Event rule "inks deal" rejected.') });
    const apply = screen.getByRole("region", { name: /Apply/ });
    await within(apply).findByText(/inks deal/);
    fireEvent.click(within(apply).getByRole("button", { name: /Reject/ }));
    await waitFor(() => expect(run).toHaveBeenCalledWith("events.rules_decide", { id: "r7", decision: "reject" }, false));
    await waitFor(() => expect(within(apply).queryByRole("button", { name: "Reject" })).toBeNull());
    expect(within(apply).getByText("No approved rule waiting.")).toBeTruthy();
  });

  it("shows a blocked gate with its reason and keeps the approved list", async () => {
    const approved = [{ id: "r7", target_kind: "action", target: "sign", phrase: "inks deal", decided_at: "2026-11-09T10:00:00Z" }];
    await open({ approved }, { "events.rules_apply": () => ({ job: job("j2", "events.rules_apply") }) });
    let end: (j: Job) => void = () => {};
    events.mockImplementation((_id, _line, onEnd) => {
      end = onEnd;
      return () => {};
    });
    const apply = screen.getByRole("region", { name: /Apply/ });
    await within(apply).findByText(/inks deal/);
    fireEvent.click(within(apply).getByRole("button", { name: "Run gate and apply" }));
    await waitFor(() => expect(events).toHaveBeenCalledWith("j2", expect.any(Function), expect.any(Function)));
    end(
      job("j2", "events.rules_apply", "succeeded", {
        render: "object",
        summary: "BLOCKED: precision drops 0.0300",
        object: {
          nothing: false,
          blocked: true,
          reason: "precision drops 0.0300",
          applied: [],
          generated_rules: "configs/generated.yml",
          before: { precision: 0.91, recall: 0.8, purity: 0.95, over_grouped: 1, fragmented: 2 },
          after: { precision: 0.88, recall: 0.82, purity: 0.94, over_grouped: 2, fragmented: 2 },
          dataset: "3 dataset article(s) affected",
          affected: 3,
        },
      }),
    );
    expect(await within(apply).findByText("BLOCKED")).toBeTruthy();
    expect(within(apply).getByText(/precision drops 0.0300/)).toBeTruthy();
    expect(within(apply).getByText("0.910")).toBeTruthy();
    expect(within(apply).getByText("0.880")).toBeTruthy();
    expect(within(apply).getByText(/3 dataset article\(s\) affected/)).toBeTruthy();
    expect(within(apply).getByText(/inks deal/)).toBeTruthy();
  });

  it("shows nothing to apply", async () => {
    await open({}, { "events.rules_apply": () => ({ job: job("j3", "events.rules_apply") }) });
    events.mockImplementation((_id, _line, onEnd) => {
      onEnd(job("j3", "events.rules_apply", "succeeded", { render: "object", summary: "Nothing to apply.", object: { nothing: true, blocked: false, reason: "", applied: [], generated_rules: "g.yml" } }));
      return () => {};
    });
    fireEvent.click(screen.getByRole("button", { name: "Run gate and apply" }));
    expect(await screen.findByText("Nothing to apply.")).toBeTruthy();
  });

  it("starts discovery as a job and reloads the queue when it ends", async () => {
    const s = await open({}, { "events.rules_discover": () => ({ job: job("j1", "events.rules_discover") }) });
    expect(await screen.findByText(/Nothing to review/)).toBeTruthy();
    let end: (j: Job) => void = () => {};
    events.mockImplementation((_id, _line, onEnd) => {
      end = onEnd;
      return () => {};
    });
    fireEvent.click(screen.getByRole("button", { name: "Discover phrases" }));
    await waitFor(() => expect(run).toHaveBeenCalledWith("events.rules_discover", {}, false));
    await waitFor(() => expect(events).toHaveBeenCalledWith("j1", expect.any(Function), expect.any(Function)));
    expect(screen.getByRole("link", { name: /j1/ }).getAttribute("href")).toBe("/jobs/j1");
    expect((screen.getByRole("button", { name: /Discover phrases|Discovering/ }) as HTMLButtonElement).disabled).toBe(true);
    s.setQueue({ items: [rule()] });
    end(
      job("j1", "events.rules_discover", "succeeded", {
        render: "object",
        summary: "60 sampled, 3 calls (0 failed), 2 proposals created or updated",
        object: { sampled: 60, calls: 3, failed: 0, proposed: 2, error: "" },
      }),
    );
    expect(await screen.findByText("60 sampled, 3 calls (0 failed), 2 proposals created or updated")).toBeTruthy();
    await waitFor(() => expect(calls("events.rules_queue").length).toBe(2));
    expect(await screen.findByRole("heading", { name: /agrees to terms/ })).toBeTruthy();
  });

  it("shows why discovery cannot start", async () => {
    await open({}, {
      "events.rules_discover": () => {
        throw new ApiError(409, "conflict", "offline_training.llm is disabled");
      },
    });
    fireEvent.click(screen.getByRole("button", { name: "Discover phrases" }));
    expect(await screen.findByText(/offline_training.llm is disabled/)).toBeTruthy();
  });

  it("shows a failed job's error", async () => {
    await open({}, { "events.rules_apply": () => ({ job: job("j4", "events.rules_apply") }) });
    events.mockImplementation((_id, _line, onEnd) => {
      onEnd({ ...job("j4", "events.rules_apply", "failed"), error: "clustering gate dataset not found" });
      return () => {};
    });
    fireEvent.click(screen.getByRole("button", { name: "Run gate and apply" }));
    expect(await screen.findByText(/clustering gate dataset not found/)).toBeTruthy();
  });

  it("drops a proposal decided elsewhere with an info notice", async () => {
    await open({ items: [rule(), failing()] }, {
      "events.rules_decide": () => {
        throw new ApiError(409, "conflict", "proposal r1 is approved, not pending");
      },
    });
    await screen.findByRole("heading", { name: /agrees to terms/ });
    fireEvent.keyDown(window, { key: "r" });
    expect(await screen.findByRole("heading", { name: /“the”/ })).toBeTruthy();
    expect(screen.getByText(/no longer pending/)).toBeTruthy();
  });

  it("keeps a proposal whose accept is refused because it fails now", async () => {
    await open({ items: [rule()] }, {
      "events.rules_decide": () => {
        throw new ApiError(409, "conflict", "proposal r1 fails the checks: phrase is a stopword");
      },
    });
    await screen.findByRole("heading", { name: /agrees to terms/ });
    fireEvent.keyDown(window, { key: "a" });
    expect((await screen.findByRole("alert")).textContent).toContain("fails the checks");
    expect(screen.getByRole("heading", { name: /agrees to terms/ })).toBeTruthy();
  });
});
