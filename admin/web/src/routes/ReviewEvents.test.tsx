import { render, screen, fireEvent, waitFor, within } from "@testing-library/preact";
import { ApiError, getJob, getSession, jobEvents, runAction, type Job, type RunOutcome } from "../api";
import { ReviewEvents } from "./ReviewEvents";

vi.mock("../api", async (orig) => ({
  ...(await orig<typeof import("../api")>()),
  runAction: vi.fn(),
  getSession: vi.fn(),
  jobEvents: vi.fn(),
  getJob: vi.fn(),
}));
const run = vi.mocked(runAction);
const events = vi.mocked(jobEvents);
const poll = vi.mocked(getJob);

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

/** typeRow is one row of events.type_list. */
function typeRow(over: Record<string, unknown> = {}) {
  return { id: "t1", status: "pending", type: "kicking error", description: "The kicker missed a field goal.", evidence: "a1, a2", ...over };
}

const typeList = (rows: unknown[]): RunOutcome => ({
  result: { render: "table", rows: rows as Record<string, unknown>[], object: { known: ["game", "injury"] }, summary: `${rows.length} event type proposal(s)` },
});

/**
 * serve answers runAction by action name; events.rules_queue returns the
 * latest queue and events.type_list the latest type proposals.
 */
function serve(q: QueueShape, more: Record<string, Handler> = {}, types: unknown[] = []) {
  let current = q;
  let currentTypes = types;
  run.mockImplementation(async (name, params) => {
    if (name === "events.rules_queue") return queueResult(current);
    const h = more[name];
    if (h) return h(params);
    if (name === "events.type_list") return typeList(currentTypes);
    throw new Error("unexpected action " + name);
  });
  return { setQueue: (next: QueueShape) => (current = next), setTypes: (next: unknown[]) => (currentTypes = next) };
}

const calls = (name: string) => run.mock.calls.filter(([n]) => n === name);
const decideCalls = () => calls("events.rules_decide");

async function open(q: QueueShape, more: Record<string, Handler> = {}, types: unknown[] = []) {
  const s = serve(q, more, types);
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
    poll.mockReset();
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

  it("accepts a passing proposal with A and adds it to the apply list without reloading the queue", async () => {
    await open({ items: [rule(), rule({ id: "r4", phrase: "signs with" })] }, { "events.rules_decide": () => obj({ id: "r1", status: "approved" }, 'Event rule "agrees to terms" approved.') });
    await screen.findByRole("heading", { name: /agrees to terms/ });
    fireEvent.keyDown(window, { key: "a" });
    await waitFor(() => expect(run).toHaveBeenCalledWith("events.rules_decide", { id: "r1", decision: "accept" }, false));
    expect(await screen.findByText('Event rule "agrees to terms" approved.')).toBeTruthy();
    const apply = screen.getByRole("region", { name: /Apply/ });
    expect(await within(apply).findByText(/agrees to terms/)).toBeTruthy();
    expect(screen.getByRole("heading", { name: /signs with/ })).toBeTruthy();
    await new Promise((r) => setTimeout(r, 20));
    expect(calls("events.rules_queue").length).toBe(1);
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
    expect(run).toHaveBeenCalledWith("events.rules_reject_failing", { ids: ["r2", "r3"] }, false);
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
    await waitFor(() => expect(run).toHaveBeenCalledWith("events.rules_decide", { id: "r7", decision: "reject", from: "approved" }, false));
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
    await waitFor(() => expect(events).toHaveBeenCalledWith("j2", expect.any(Function), expect.any(Function), expect.any(Function)));
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

  it("shows the new event types the gate applied and the phrases left without a target", async () => {
    await open({}, { "events.rules_apply": () => ({ job: job("j6", "events.rules_apply") }) });
    events.mockImplementation((_id, _line, onEnd) => {
      onEnd(
        job("j6", "events.rules_apply", "succeeded", {
          render: "object",
          summary: "PASS: 1 rule(s) applied to g.yml, 1 new event type(s), 1 phrase(s) without a declared or approved target",
          object: {
            nothing: false,
            blocked: false,
            reason: "",
            applied: [{ id: "r7", target_kind: "action", target: "kicking-error", phrase: "misses a kick" }],
            generated_rules: "g.yml",
            types_applied: [{ id: "t1", type: "kicking-error", window: "72h", referential: true }],
            types_waiting: [],
            orphans: [{ id: "r8", target_kind: "action", target: "gone-type", phrase: "posts the line" }],
          },
        }),
      );
      return () => {};
    });
    fireEvent.click(screen.getByRole("button", { name: "Run gate and apply" }));
    const applied = await screen.findByRole("list", { name: "New event types applied" });
    expect(within(applied).getByText("kicking-error")).toBeTruthy();
    expect(within(applied).getByText(/window 72h, referential/)).toBeTruthy();
    expect(screen.getByText(/1 approved phrase without a declared or approved target/)).toBeTruthy();
    expect(screen.getByText(/“posts the line” \(gone-type\)/)).toBeTruthy();
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
    await waitFor(() => expect(events).toHaveBeenCalledWith("j1", expect.any(Function), expect.any(Function), expect.any(Function)));
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

  it("lists the pending event type proposals that the phrase review does not show", async () => {
    await open({}, {}, [typeRow(), typeRow({ id: "t2", type: "betting odds", description: "Point spread reports.", evidence: "a3" })]);
    const zone = screen.getByRole("region", { name: /New event types/ });
    expect(await within(zone).findByText("kicking error")).toBeTruthy();
    expect(within(zone).getByText("The kicker missed a field goal.")).toBeTruthy();
    expect(within(zone).getByText(/2 evidence articles/)).toBeTruthy();
    expect(within(zone).getByText("betting odds")).toBeTruthy();
    expect(within(zone).getByText(/1 evidence article\b/)).toBeTruthy();
    expect(within(zone).getByText(/2 pending/)).toBeTruthy();
  });

  it("says when no event type proposal is pending", async () => {
    await open({});
    const zone = screen.getByRole("region", { name: /New event types/ });
    expect(await within(zone).findByText("No event type proposal pending.")).toBeTruthy();
  });

  it("approves a type proposal through its form with a window and the referential choice", async () => {
    await open(
      {},
      { "events.type_approve": () => obj({ id: "t1", type: "kicking error", status: "approved", window: "72h", referential: true }, "Event type proposal t1 (kicking error) approved.") },
      [typeRow()],
    );
    const zone = screen.getByRole("region", { name: /New event types/ });
    await within(zone).findByText("kicking error");
    fireEvent.click(within(zone).getByRole("button", { name: "Approve kicking error" }));
    const windowField = within(zone).getByRole("textbox", { name: "Window for kicking error" }) as HTMLInputElement;
    expect(windowField.value).toBe("48h");
    const referential = within(zone).getByRole("checkbox", { name: "Referential (a framing such as analysis or quote, not a concrete event)" }) as HTMLInputElement;
    expect(referential.checked).toBe(false);
    expect(calls("events.type_approve")).toHaveLength(0);
    fireEvent.input(windowField, { target: { value: "72h" } });
    fireEvent.click(referential);
    fireEvent.click(within(zone).getByRole("button", { name: "Confirm" }));
    await waitFor(() => expect(run).toHaveBeenCalledWith("events.type_approve", { id: "t1", window: "72h", referential: true }, false));
    expect(await within(zone).findByText("Event type proposal t1 (kicking error) approved.")).toBeTruthy();
    expect(within(zone).getByText(/approved · waiting for phrases/)).toBeTruthy();
    expect(within(zone).queryByRole("textbox", { name: "Window for kicking error" })).toBeNull();
    expect(within(zone).queryByRole("button", { name: "Approve kicking error" })).toBeNull();
    expect(within(zone).getByRole("button", { name: "Reject kicking error" })).toBeTruthy();
    expect(within(zone).getByRole("button", { name: "Add phrase kicking error" })).toBeTruthy();
  });

  it("shows why a window is refused and keeps the approve form", async () => {
    await open(
      {},
      {
        "events.type_approve": () => {
          throw new ApiError(400, "invalid_params", 'eventruleproposal: window "0s" is not a positive duration');
        },
      },
      [typeRow()],
    );
    const zone = screen.getByRole("region", { name: /New event types/ });
    await within(zone).findByText("kicking error");
    fireEvent.click(within(zone).getByRole("button", { name: "Approve kicking error" }));
    fireEvent.input(within(zone).getByRole("textbox", { name: "Window for kicking error" }), { target: { value: "0s" } });
    fireEvent.click(within(zone).getByRole("button", { name: "Confirm" }));
    expect(await within(zone).findByText(/is not a positive duration/)).toBeTruthy();
    expect((within(zone).getByRole("textbox", { name: "Window for kicking error" }) as HTMLInputElement).value).toBe("0s");
    expect(within(zone).getByText(/1 pending/)).toBeTruthy();
  });

  it("cancels the approve form without calling the action", async () => {
    await open({}, {}, [typeRow()]);
    const zone = screen.getByRole("region", { name: /New event types/ });
    await within(zone).findByText("kicking error");
    fireEvent.click(within(zone).getByRole("button", { name: "Approve kicking error" }));
    fireEvent.click(within(zone).getByRole("button", { name: "Cancel" }));
    expect(within(zone).queryByRole("textbox", { name: "Window for kicking error" })).toBeNull();
    expect(calls("events.type_approve")).toHaveLength(0);
  });

  it("labels an approved type by its phrases: waiting, or how many", async () => {
    await open({}, {}, [
      typeRow({ status: "approved", phrases: 0 }),
      typeRow({ id: "t2", type: "betting odds", status: "approved", phrases: 2 }),
      typeRow({ id: "t3", type: "stadium works", status: "approved", phrases: 1 }),
    ]);
    const zone = screen.getByRole("region", { name: /New event types/ });
    const item = (name: string) => within(zone).getByText(name).closest("li")!;
    await within(zone).findByText("kicking error");
    expect(within(item("kicking error")).getByText(/approved · waiting for phrases/)).toBeTruthy();
    expect(within(item("betting odds")).getByText(/approved · 2 phrases/)).toBeTruthy();
    expect(within(item("stadium works")).getByText(/approved · 1 phrase\b/)).toBeTruthy();
  });

  it("adds a phrase to an approved type and reloads the queue, where it appears", async () => {
    const s = await open(
      {},
      {
        "events.type_add_phrase": () => {
          s.setQueue({ items: [rule({ id: "r9", target: "kicking-error", phrase: "misses a kick", prompt_version: "manual" })] });
          s.setTypes([typeRow({ type: "kicking-error", status: "approved", phrases: 1 })]);
          return obj({ id: "r9", target_kind: "action", target: "kicking-error", phrase: "misses a kick", status: "pending" }, 'Phrase "misses a kick" proposed for kicking-error; review it in the queue.');
        },
      },
      [typeRow({ type: "kicking-error", status: "approved", phrases: 0 })],
    );
    const zone = screen.getByRole("region", { name: /New event types/ });
    await within(zone).findByText(/waiting for phrases/);
    fireEvent.click(within(zone).getByRole("button", { name: "Add phrase kicking-error" }));
    const field = within(zone).getByRole("textbox", { name: "Phrase for kicking-error" });
    fireEvent.input(field, { target: { value: "misses a kick" } });
    fireEvent.click(within(zone).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(run).toHaveBeenCalledWith("events.type_add_phrase", { id: "t1", phrase: "misses a kick" }, false));
    expect(await within(zone).findByText('Phrase "misses a kick" proposed for kicking-error; review it in the queue.')).toBeTruthy();
    await waitFor(() => expect(calls("events.rules_queue").length).toBe(2));
    const review = screen.getByRole("region", { name: /Review/ });
    expect((await within(review).findAllByText(/misses a kick/)).length).toBeGreaterThan(0);
    expect(await within(zone).findByText(/approved · 1 phrase\b/)).toBeTruthy();
    expect(within(zone).queryByRole("textbox", { name: "Phrase for kicking-error" })).toBeNull();
  });

  it("shows why a phrase could not be added and keeps its field", async () => {
    await open(
      {},
      {
        "events.type_add_phrase": () => {
          throw new ApiError(400, "invalid_params", "the phrase is empty");
        },
      },
      [typeRow({ status: "approved", phrases: 0 })],
    );
    const zone = screen.getByRole("region", { name: /New event types/ });
    await within(zone).findByText("kicking error");
    fireEvent.click(within(zone).getByRole("button", { name: "Add phrase kicking error" }));
    fireEvent.click(within(zone).getByRole("button", { name: "Save" }));
    expect(await within(zone).findByText(/the phrase is empty/)).toBeTruthy();
    expect(within(zone).getByRole("textbox", { name: "Phrase for kicking error" })).toBeTruthy();
    expect(calls("events.rules_queue")).toHaveLength(1);
  });

  it("lists in the apply zone the approved types that have an approved phrase", async () => {
    const approved = [{ id: "r7", target_kind: "action", target: "kicking-error", phrase: "misses a kick", decided_at: "2026-11-09T10:00:00Z" }];
    await open({ approved }, {}, [
      typeRow({ type: "kicking-error", status: "approved", phrases: 1, window: "72h", referential: false }),
      typeRow({ id: "t2", type: "betting-odds", status: "approved", phrases: 0 }),
    ]);
    const apply = screen.getByRole("region", { name: /Apply/ });
    const types = await within(apply).findByRole("list", { name: "New event types to apply" });
    expect(within(types).getByText("kicking-error")).toBeTruthy();
    expect(within(types).getByText(/window 72h/)).toBeTruthy();
    expect(within(types).queryByText("betting-odds")).toBeNull();
  });

  it("rejects a type proposal and removes it", async () => {
    await open({}, { "events.type_reject": () => obj({ id: "t1", type: "kicking error", status: "rejected" }, "Event type proposal t1 (kicking error) rejected.") }, [typeRow()]);
    const zone = screen.getByRole("region", { name: /New event types/ });
    await within(zone).findByText("kicking error");
    fireEvent.click(within(zone).getByRole("button", { name: "Reject kicking error" }));
    await waitFor(() => expect(run).toHaveBeenCalledWith("events.type_reject", { id: "t1" }, false));
    expect(await within(zone).findByText("Event type proposal t1 (kicking error) rejected.")).toBeTruthy();
    expect(within(zone).queryByText("kicking error")).toBeNull();
  });

  it("shows why a type decision failed and keeps the proposal", async () => {
    await open(
      {},
      {
        "events.type_approve": () => {
          throw new ApiError(409, "conflict", "event type proposal t1 is not pending");
        },
      },
      [typeRow()],
    );
    const zone = screen.getByRole("region", { name: /New event types/ });
    await within(zone).findByText("kicking error");
    fireEvent.click(within(zone).getByRole("button", { name: "Approve kicking error" }));
    fireEvent.click(within(zone).getByRole("button", { name: "Confirm" }));
    expect(await within(zone).findByText(/is not pending/)).toBeTruthy();
    expect(within(zone).getByText("kicking error")).toBeTruthy();
  });

  it("merges a type proposal into another proposal and reloads the list", async () => {
    const s = await open(
      {},
      {
        "events.type_merge": () => {
          s.setTypes([typeRow({ id: "t2", type: "betting odds", evidence: "a3, a4, a5" })]);
          return obj({ id: "t1", type: "kicking error", status: "rejected", into: "t2", covered_by: "" }, "Event type proposal t1 (kicking error) merged into t2 (betting odds).");
        },
      },
      [typeRow(), typeRow({ id: "t2", type: "betting odds", evidence: "a3" })],
    );
    const zone = screen.getByRole("region", { name: /New event types/ });
    await within(zone).findByText("kicking error");
    fireEvent.click(within(zone).getByRole("button", { name: "Merge kicking error" }));
    const pick = within(zone).getByRole("combobox") as HTMLSelectElement;
    const labels = Array.from(pick.options).map((o) => o.textContent);
    expect(labels).toEqual(["proposal: betting odds", "existing: game", "existing: injury"]);
    fireEvent.change(pick, { target: { value: "t2" } });
    fireEvent.click(within(zone).getByRole("button", { name: "Merge" }));
    await waitFor(() => expect(run).toHaveBeenCalledWith("events.type_merge", { id: "t1", into: "t2" }, false));
    expect(await within(zone).findByText("Event type proposal t1 (kicking error) merged into t2 (betting odds).")).toBeTruthy();
    expect(within(zone).queryByText("kicking error")).toBeNull();
    expect(await within(zone).findByText(/3 evidence articles/)).toBeTruthy();
    expect(within(zone).queryByRole("combobox")).toBeNull();
  });

  it("merges a type proposal into a known name", async () => {
    await open({}, { "events.type_merge": () => obj({ id: "t1", type: "kicking error", status: "rejected", into: "game", covered_by: "game" }, "Event type proposal t1 (kicking error) rejected: covered by game.") }, [typeRow()]);
    const zone = screen.getByRole("region", { name: /New event types/ });
    await within(zone).findByText("kicking error");
    fireEvent.click(within(zone).getByRole("button", { name: "Merge kicking error" }));
    fireEvent.change(within(zone).getByRole("combobox"), { target: { value: "game" } });
    fireEvent.click(within(zone).getByRole("button", { name: "Merge" }));
    await waitFor(() => expect(run).toHaveBeenCalledWith("events.type_merge", { id: "t1", into: "game" }, false));
    expect(await within(zone).findByText("Event type proposal t1 (kicking error) rejected: covered by game.")).toBeTruthy();
    expect(within(zone).queryByText("kicking error")).toBeNull();
  });

  it("cancels a merge without calling the action", async () => {
    await open({}, {}, [typeRow()]);
    const zone = screen.getByRole("region", { name: /New event types/ });
    await within(zone).findByText("kicking error");
    fireEvent.click(within(zone).getByRole("button", { name: "Merge kicking error" }));
    fireEvent.click(within(zone).getByRole("button", { name: "Cancel" }));
    expect(within(zone).queryByRole("combobox")).toBeNull();
    expect(calls("events.type_merge")).toHaveLength(0);
  });

  it("shows why a merge failed and keeps the proposal", async () => {
    await open(
      {},
      {
        "events.type_merge": () => {
          throw new ApiError(409, "conflict", "proposal t1 already decided");
        },
      },
      [typeRow()],
    );
    const zone = screen.getByRole("region", { name: /New event types/ });
    await within(zone).findByText("kicking error");
    fireEvent.click(within(zone).getByRole("button", { name: "Merge kicking error" }));
    fireEvent.click(within(zone).getByRole("button", { name: "Merge" }));
    expect(await within(zone).findByText(/already decided/)).toBeTruthy();
    expect(within(zone).getByText("kicking error")).toBeTruthy();
  });

  it("offers no Brief button", async () => {
    await open({}, {}, [typeRow(), typeRow({ id: "t2", type: "betting odds", status: "approved", phrases: 0 })]);
    const zone = screen.getByRole("region", { name: /New event types/ });
    await within(zone).findByText("kicking error");
    expect(within(zone).queryByRole("button", { name: /Brief/ })).toBeNull();
  });

  it("reloads the type proposals when discovery ends", async () => {
    const s = await open({}, { "events.rules_discover": () => ({ job: job("j5", "events.rules_discover") }) });
    const zone = screen.getByRole("region", { name: /New event types/ });
    await within(zone).findByText("No event type proposal pending.");
    events.mockImplementation((_id, _line, onEnd) => {
      s.setTypes([typeRow()]);
      onEnd(job("j5", "events.rules_discover", "succeeded", { render: "object", summary: "done", object: {} }));
      return () => {};
    });
    fireEvent.click(screen.getByRole("button", { name: "Discover phrases" }));
    expect(await within(zone).findByText("kicking error")).toBeTruthy();
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
  it("disables the decisions and the apply list's Reject while apply runs", async () => {
    const approved = [{ id: "r7", target_kind: "action", target: "sign", phrase: "inks deal", decided_at: "2026-11-09T10:00:00Z" }];
    await open({ items: [rule()], approved }, { "events.rules_apply": () => ({ job: job("j5", "events.rules_apply") }) });
    let end: (j: Job) => void = () => {};
    events.mockImplementation((_id, _line, onEnd) => {
      end = onEnd;
      return () => {};
    });
    await screen.findByRole("heading", { name: /agrees to terms/ });
    const apply = screen.getByRole("region", { name: /Apply/ });
    await within(apply).findByText(/inks deal/);
    fireEvent.click(within(apply).getByRole("button", { name: "Run gate and apply" }));
    await waitFor(() => expect(events).toHaveBeenCalled());
    expect((within(apply).getByRole("button", { name: "Reject" }) as HTMLButtonElement).disabled).toBe(true);
    for (const name of [/Accept/, /^Reject R/, /Edit phrase/]) {
      expect((within(card()).getByRole("button", { name }) as HTMLButtonElement).disabled).toBe(true);
    }
    for (const key of ["a", "r", "e"]) fireEvent.keyDown(window, { key });
    await new Promise((r) => setTimeout(r, 20));
    expect(decideCalls()).toEqual([]);
    expect(screen.queryByRole("textbox", { name: /New phrase/ })).toBeNull();
    end(job("j5", "events.rules_apply", "succeeded", { render: "object", summary: "Nothing to apply.", object: { nothing: true, blocked: false, reason: "", applied: [], generated_rules: "g.yml" } }));
    await waitFor(() => expect((within(card()).getByRole("button", { name: /Accept/ }) as HTMLButtonElement).disabled).toBe(false));
  });

  it("disables Reject all failing while the phrase editor is open", async () => {
    await open({ items: [rule(), failing()] });
    await screen.findByRole("heading", { name: /agrees to terms/ });
    const all = () => screen.getByRole("button", { name: /Reject all failing/ }) as HTMLButtonElement;
    expect(all().disabled).toBe(false);
    fireEvent.keyDown(window, { key: "e" });
    await screen.findByRole("textbox", { name: /New phrase/ });
    expect(all().disabled).toBe(true);
  });

  it("clears the reject-all notice on the next decision", async () => {
    await open(
      { items: [failing(), rule()] },
      {
        "events.rules_reject_failing": () => obj({ rejected: [{ id: "r2", phrase: "the", reasons: ["x"] }] }, "1 failing proposal(s) rejected."),
        "events.rules_decide": () => obj({ id: "r1", status: "rejected" }, 'Event rule "agrees to terms" rejected.'),
      },
    );
    await screen.findByRole("heading", { name: /“the”/ });
    fireEvent.click(screen.getByRole("button", { name: "Reject all failing (1)" }));
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Reject 1" }));
    expect(await screen.findByText(/1 failing proposal\(s\) rejected/)).toBeTruthy();
    await screen.findByRole("heading", { name: /agrees to terms/ });
    fireEvent.keyDown(window, { key: "r" });
    expect(await screen.findByText('Event rule "agrees to terms" rejected.')).toBeTruthy();
    expect(screen.queryByText(/1 failing proposal\(s\) rejected/)).toBeNull();
  });

  it("shows the empty-sample notice", async () => {
    serve({});
    run.mockImplementation(async (name) => {
      if (name === "events.rules_queue") return obj({ sampled: 0, items: [], no_evidence: [], approved: [], notice: "no generic articles in the sample" });
      throw new Error("unexpected action " + name);
    });
    render(<ReviewEvents />);
    expect(await screen.findByText(/no generic articles in the sample/)).toBeTruthy();
  });

  it("polls the job when its event stream fails", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      await open({}, { "events.rules_apply": () => ({ job: job("j6", "events.rules_apply") }) });
      events.mockImplementation((_id, _line, _end, onError) => {
        onError?.();
        return () => {};
      });
      poll
        .mockResolvedValueOnce(job("j6", "events.rules_apply", "running"))
        .mockResolvedValueOnce(job("j6", "events.rules_apply", "succeeded", { render: "object", summary: "Nothing to apply.", object: { nothing: true, blocked: false, reason: "", applied: [], generated_rules: "g.yml" } }));
      fireEvent.click(screen.getByRole("button", { name: "Run gate and apply" }));
      await waitFor(() => expect(poll).toHaveBeenCalledTimes(1));
      await vi.advanceTimersByTimeAsync(5000);
      expect(await screen.findByText("Nothing to apply.")).toBeTruthy();
      expect(poll).toHaveBeenCalledWith("j6");
    } finally {
      vi.useRealTimers();
    }
  });
});

describe("ReviewEventsScreen in the editorial cycle", () => {
  beforeEach(() => {
    run.mockReset();
    events.mockReset();
  });

  it("discovers and applies with the given actions and shows the apply summary", async () => {
    const { ReviewEventsScreen } = await import("./ReviewEvents");
    serve(
      {},
      {
        "cycle.event_rules_discover": () => ({ job: job("c1", "cycle.event_rules_discover") }),
        "cycle.event_rules_apply": () => ({ job: job("c2", "cycle.event_rules_apply") }),
      },
    );
    render(<ReviewEventsScreen discoverAction="cycle.event_rules_discover" applyAction="cycle.event_rules_apply" />);
    await waitFor(() => expect(calls("events.rules_queue").length).toBe(1));
    events.mockImplementation((id, _line, onEnd) => {
      onEnd(
        id === "c1"
          ? job("c1", "cycle.event_rules_discover", "succeeded", { render: "object", summary: "60 sampled, 2 calls (0 failed), 1 proposals created or updated", object: { sampled: 60, proposed: 1 } })
          : job("c2", "cycle.event_rules_apply", "succeeded", { render: "object", summary: "PASS: 2 rule(s) applied to configs/generated.yml", object: { sampled: 60, applied: 2 } }),
      );
      return () => {};
    });
    fireEvent.click(screen.getByRole("button", { name: "Discover phrases" }));
    expect(await screen.findByText("60 sampled, 2 calls (0 failed), 1 proposals created or updated")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Run gate and apply" }));
    expect(await screen.findByText("PASS: 2 rule(s) applied to configs/generated.yml")).toBeTruthy();
    expect(calls("events.rules_discover")).toHaveLength(0);
    expect(calls("events.rules_apply")).toHaveLength(0);
    expect(run).toHaveBeenCalledWith("cycle.event_rules_discover", {}, false);
    expect(run).toHaveBeenCalledWith("cycle.event_rules_apply", {}, false);
  });

  it("reports the start and end of its jobs", async () => {
    const { ReviewEventsScreen } = await import("./ReviewEvents");
    serve({}, { "events.rules_discover": () => ({ job: job("c3", "events.rules_discover") }) });
    const started = vi.fn();
    const ended = vi.fn();
    render(<ReviewEventsScreen onJobStart={started} onJobEnd={ended} />);
    await waitFor(() => expect(calls("events.rules_queue").length).toBe(1));
    let end: (j: Job) => void = () => {};
    events.mockImplementation((_id, _line, onEnd) => {
      end = onEnd;
      return () => {};
    });
    fireEvent.click(screen.getByRole("button", { name: "Discover phrases" }));
    await waitFor(() => expect(started).toHaveBeenCalledTimes(1));
    expect(ended).not.toHaveBeenCalled();
    end(job("c3", "events.rules_discover", "succeeded", { render: "object", summary: "done", object: {} }));
    await waitFor(() => expect(ended).toHaveBeenCalledTimes(1));
  });
});
