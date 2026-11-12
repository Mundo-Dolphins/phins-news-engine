import { render, screen, fireEvent, waitFor, within } from "@testing-library/preact";
import { ApiError, getJob, getSession, jobEvents, listActions, runAction, type CatalogItem, type Job, type RunOutcome } from "../api";
import { ReviewDictionary } from "./ReviewDictionary";

vi.mock("../api", async (orig) => ({
  ...(await orig<typeof import("../api")>()),
  runAction: vi.fn(),
  getSession: vi.fn(),
  listActions: vi.fn(),
  jobEvents: vi.fn(),
  getJob: vi.fn(),
}));
const run = vi.mocked(runAction);
const events = vi.mocked(jobEvents);

const SID = "20261112T100000Z";

const catalogItem = (name: string): CatalogItem => ({
  name,
  group: "dictionary",
  title: name,
  description: "",
  kind: "read",
  modes: ["local"],
  long_running: false,
  exclusive: false,
  schema: { type: "object", properties: {} },
});

const hostCatalog = () =>
  [
    "dictionary.update_start",
    "dictionary.update_session",
    "dictionary.update_decide",
    "dictionary.update_decide_type",
    "dictionary.update_preview",
    "dictionary.update_apply",
    "dictionary.update_discard",
  ].map(catalogItem);

type Decision = { action: string; decided_at: string; edited?: { category: string; detail: string } } | null;

function proposal(index: number, over: Record<string, unknown> = {}) {
  return {
    index,
    kind: "update_number",
    canonical_key: "ann-example",
    category: "players",
    detail: "number 12 → 21",
    conflict_with: null,
    current_entry: {
      canonical_name: "Ann Example",
      aliases: ["A. Example"],
      type: "person",
      roster_status: "active",
      role: "",
      position: "QB",
      number: "12",
      source: "https://club.example.org/roster",
      confidence: 0.9,
      notes: "",
    },
    observed: {
      display_name: "Ann Example",
      canonical_key: "ann-example",
      kind: "player",
      role: "",
      position: "QB",
      number: "21",
      department: "",
      profile_url: "https://club.example.org/players/ann",
      aliases: [{ value: "A. Example", class: "short" }],
      roster_status: "active",
      source: "roster",
      observed_at: "2026-11-12T09:00:00Z",
    },
    alias: null,
    decision: null as Decision,
    ...over,
  };
}

function sessionResult(proposals: ReturnType<typeof proposal>[]) {
  const undecided = proposals.filter((p) => !p.decision).length;
  return obj({
    session: { id: SID, status: "open", started_at: "2026-11-12T10:00:00Z", snapshot_refs: ["s1"], dictionary_version: "v7" },
    proposals,
    undecided,
  });
}

const noSession = () => obj({ session: null, proposals: [], undecided: 0 }, "No open session");

const obj = (object: unknown, summary = ""): RunOutcome => ({ result: { render: "object", object, summary } });

type Handler = (params: Record<string, unknown>, confirm: boolean) => Promise<RunOutcome> | RunOutcome;

/** serve answers runAction by name; dictionary.update_session returns the latest state. */
function serve(session: () => RunOutcome | Promise<RunOutcome>, more: Record<string, Handler> = {}) {
  run.mockImplementation(async (name, params, confirm) => {
    if (name === "dictionary.update_session") return session();
    const h = more[name];
    if (!h) throw new Error("unexpected action " + name);
    return h(params, confirm);
  });
}

const calls = (name: string) => run.mock.calls.filter(([n]) => n === name);

async function open(session: () => RunOutcome | Promise<RunOutcome>, more: Record<string, Handler> = {}) {
  serve(session, more);
  render(<ReviewDictionary />);
  await screen.findByRole("heading", { name: "Dictionary update review" });
  await waitFor(() => expect(calls("dictionary.update_session").length).toBeGreaterThan(0));
}

const card = () => screen.getByRole("region", { name: "Selected item" });
const applyZone = () => screen.getByRole("region", { name: /Apply/ });
const sessionZone = () => screen.getByRole("region", { name: /Session/ });

describe("ReviewDictionary", () => {
  beforeEach(() => {
    run.mockReset();
    events.mockReset();
    vi.mocked(getJob).mockReset();
    vi.mocked(getSession).mockResolvedValue({ username: "ana", mode: "local" });
    vi.mocked(listActions).mockResolvedValue(hostCatalog());
  });

  it("says it is not available when the host registers no dictionary update actions", async () => {
    vi.mocked(listActions).mockResolvedValue([catalogItem("events.rules_queue")]);
    render(<ReviewDictionary />);
    expect(await screen.findByText("Not available on this host")).toBeTruthy();
    expect(run).not.toHaveBeenCalled();
  });

  it("shows the session, the proposal with current and observed side by side, and safe links only", async () => {
    const bad = proposal(1, { kind: "add_entity", canonical_key: "bo-sample", current_entry: null, observed: { ...proposal(0).observed, display_name: "Bo Sample", profile_url: "javascript:alert(1)" } });
    await open(() => sessionResult([proposal(0), bad]));
    const s = sessionZone();
    expect(await within(s).findByText(SID)).toBeTruthy();
    expect(within(s).getByText(/2 proposals, 2 undecided/)).toBeTruthy();
    const c = card();
    expect(within(c).getByText("update number")).toBeTruthy();
    expect(within(c).getByText("number 12 → 21")).toBeTruthy();
    const row = within(c).getByText("Number").closest("tr")!;
    expect(within(row).getByText("12")).toBeTruthy();
    expect(within(row).getByText("21")).toBeTruthy();
    const profile = within(c).getByRole("link", { name: "https://club.example.org/players/ann" });
    expect(profile.hasAttribute("data-review-open")).toBe(true);
    // Select the second proposal: its unsafe profile URL is shown as text.
    fireEvent.keyDown(window, { key: "j" });
    expect(await within(card()).findByText("javascript:alert(1)")).toBeTruthy();
    expect(within(card()).queryByRole("link", { name: "javascript:alert(1)" })).toBeNull();
    expect(within(card()).getByText(/Not in the dictionary/)).toBeTruthy();
  });

  it("accepts with A, posts the index and moves to the next undecided proposal", async () => {
    let ps = [proposal(0), proposal(1, { canonical_key: "bo-sample" })];
    await open(() => sessionResult(ps), {
      "dictionary.update_decide": (p) => {
        ps = ps.map((x) => (x.index === p.index ? { ...x, decision: { action: String(p.action), decided_at: "2026-11-12T10:05:00Z" } } : x));
        return obj({ index: p.index, action: p.action, undecided: 1 }, "proposal 0: accept; 1 undecided");
      },
    });
    await within(card()).findByText("number 12 → 21");
    fireEvent.keyDown(window, { key: "a" });
    await waitFor(() =>
      expect(run).toHaveBeenCalledWith("dictionary.update_decide", { session_id: SID, index: 0, action: "accept" }, false),
    );
    expect(await screen.findByText("proposal 0: accept; 1 undecided")).toBeTruthy();
    await waitFor(() => expect(within(card()).getByRole("heading", { name: /bo-sample/ })).toBeTruthy());
    const queue = screen.getByRole("list", { name: "Queue" });
    expect(within(queue).getByText("accepted")).toBeTruthy();
  });

  it("skips with S as a recorded decision and rejects with R", async () => {
    await open(() => sessionResult([proposal(0), proposal(1)]), {
      "dictionary.update_decide": (p) => obj({ index: p.index, action: p.action, undecided: 1 }),
    });
    await within(card()).findByText("number 12 → 21");
    fireEvent.keyDown(window, { key: "s" });
    await waitFor(() =>
      expect(run).toHaveBeenCalledWith("dictionary.update_decide", { session_id: SID, index: 0, action: "skip" }, false),
    );
    await waitFor(() => expect(calls("dictionary.update_session").length).toBe(2));
    fireEvent.keyDown(window, { key: "r" });
    await waitFor(() => expect(calls("dictionary.update_decide").length).toBe(2));
    expect(calls("dictionary.update_decide")[1][1]).toMatchObject({ action: "reject" });
  });

  it("re-decides an earlier, already decided proposal with its own index", async () => {
    const decided = proposal(0, { decision: { action: "accept", decided_at: "2026-11-12T10:05:00Z" } });
    await open(() => sessionResult([decided, proposal(1, { canonical_key: "bo-sample" })]), {
      "dictionary.update_decide": (p) => obj({ index: p.index, action: p.action, undecided: 1 }),
    });
    const queue = await screen.findByRole("list", { name: "Queue" });
    // The decided proposal stays listed with its decision.
    expect(within(queue).getByText("accepted")).toBeTruthy();
    fireEvent.click(within(queue).getAllByRole("button")[0]);
    expect(await within(card()).findByText(/Decided: accepted/)).toBeTruthy();
    fireEvent.keyDown(window, { key: "r" });
    await waitFor(() =>
      expect(run).toHaveBeenCalledWith("dictionary.update_decide", { session_id: SID, index: 0, action: "reject" }, false),
    );
  });

  it("edits with E: typing in the editor fires no decision, Enter posts category and note", async () => {
    await open(() => sessionResult([proposal(0)]), {
      "dictionary.update_decide": (p) => obj({ index: p.index, action: p.action, undecided: 0 }, "proposal 0: edit; 0 undecided"),
      "dictionary.update_preview": () => obj({ undecided: 0, changes: 1, diff: "", regression: null }),
    });
    await within(card()).findByText("number 12 → 21");
    fireEvent.keyDown(window, { key: "e" });
    const category = await screen.findByRole("textbox", { name: "Category" });
    const note = screen.getByRole("textbox", { name: "Note" });
    for (const key of ["a", "r", "s"]) {
      fireEvent.keyDown(category, { key });
      fireEvent.keyDown(note, { key });
      fireEvent.keyDown(document.body, { key });
    }
    expect(calls("dictionary.update_decide")).toHaveLength(0);
    fireEvent.input(category, { target: { value: "staff" } });
    fireEvent.input(note, { target: { value: "ars" } });
    fireEvent.keyDown(note, { key: "Enter" });
    await waitFor(() =>
      expect(run).toHaveBeenCalledWith(
        "dictionary.update_decide",
        { session_id: SID, index: 0, action: "edit", category: "staff", note: "ars" },
        false,
      ),
    );
    expect(calls("dictionary.update_decide")).toHaveLength(1);
    await waitFor(() => expect(screen.queryByRole("textbox", { name: "Category" })).toBeNull());
  });

  it("shows an edited decision with its category and detail", async () => {
    const edited = proposal(0, { decision: { action: "edit", decided_at: "2026-11-12T10:05:00Z", edited: { category: "staff", detail: "number 12 → 21; ars" } } });
    await open(() => sessionResult([edited]), {
      "dictionary.update_preview": () => obj({ undecided: 0, changes: 1, diff: "", regression: null }),
    });
    const c = await waitFor(() => card());
    expect(within(c).getByText(/Decided: edited/)).toBeTruthy();
    expect(within(c).getByText("number 12 → 21; ars")).toBeTruthy();
  });

  it("accepts all of this type after a confirmation", async () => {
    await open(() => sessionResult([proposal(0), proposal(1, { kind: "add_alias" }), proposal(2)]), {
      "dictionary.update_decide_type": () => obj({ decided: 2, undecided: 1 }, "2 proposals accepted; 1 undecided"),
    });
    await within(card()).findByText("number 12 → 21");
    fireEvent.click(within(card()).getByRole("button", { name: "Accept all of this type" }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText(/2 proposals/)).toBeTruthy();
    // Keys inside the dialog never reach the review shortcuts.
    fireEvent.keyDown(dialog, { key: "a" });
    expect(calls("dictionary.update_decide")).toHaveLength(0);
    fireEvent.click(within(dialog).getByRole("button", { name: /Accept 2/ }));
    await waitFor(() =>
      expect(run).toHaveBeenCalledWith("dictionary.update_decide_type", { session_id: SID, index: 0, accept: true }, false),
    );
    expect(await screen.findByText("2 proposals accepted; 1 undecided")).toBeTruthy();
  });

  it("rejecting all of this type can be cancelled", async () => {
    await open(() => sessionResult([proposal(0)]));
    await within(card()).findByText("number 12 → 21");
    fireEvent.click(within(card()).getByRole("button", { name: "Reject all of this type" }));
    const dialog = await screen.findByRole("dialog");
    fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(calls("dictionary.update_decide_type")).toHaveLength(0);
  });

  it("keeps Apply disabled while proposals are undecided", async () => {
    await open(() => sessionResult([proposal(0, { decision: { action: "skip", decided_at: "x" } }), proposal(1)]));
    await within(card()).findByText("number 12 → 21");
    const z = applyZone();
    expect(within(z).getByText(/1 proposal still undecided/)).toBeTruthy();
    expect((within(z).getByRole("button", { name: "Apply to the dictionary" }) as HTMLButtonElement).disabled).toBe(true);
    expect(calls("dictionary.update_apply")).toHaveLength(0);
  });

  it("previews when all are decided, applies after the confirmation and shows the result", async () => {
    let applied = false;
    const decided = [
      proposal(0, { decision: { action: "accept", decided_at: "x" } }),
      proposal(1, { decision: { action: "skip", decided_at: "x" } }),
    ];
    await open(() => (applied ? noSession() : sessionResult(decided)), {
      "dictionary.update_preview": () =>
        obj({
          undecided: 0,
          changes: 1,
          diff: "- number: 12\n+ number: 21",
          regression: { total_cases: 40, flip_count: 1, flips: [{ case_name: "case-a", direction: "related→unrelated", score_before: 3, score_after: 1, rule_before: "r1", rule_after: "r2" }] },
        }),
      "dictionary.update_apply": () => {
        applied = true;
        return obj(
          { from: "v7", to: "v8", changes: 1, unchanged: false, diff: "- number: 12\n+ number: 21", regression: { total_cases: 40, flip_count: 0, flips: [] } },
          "dictionary updated: v7 -> v8 (1 changes)",
        );
      },
    });
    const z = applyZone();
    await waitFor(() => expect(calls("dictionary.update_preview")).toHaveLength(1));
    expect(run).toHaveBeenCalledWith("dictionary.update_preview", { session_id: SID }, false);
    expect(await within(z).findByText(/1 change/)).toBeTruthy();
    expect(within(z).getAllByText(/\+ number: 21/).length).toBeGreaterThan(0);
    expect(within(z).getByText(/1 of 40 regression cases flip/)).toBeTruthy();
    expect(within(z).getByText(/case-a/)).toBeTruthy();
    const apply = within(z).getByRole("button", { name: "Apply to the dictionary" }) as HTMLButtonElement;
    expect(apply.disabled).toBe(false);
    fireEvent.click(apply);
    const dialog = await screen.findByRole("dialog");
    expect(calls("dictionary.update_apply")).toHaveLength(0);
    fireEvent.input(within(dialog).getByRole("textbox"), { target: { value: "dictionary.update_apply" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Confirm" }));
    await waitFor(() => expect(run).toHaveBeenCalledWith("dictionary.update_apply", { session_id: SID }, true));
    expect(await within(applyZone()).findByText("dictionary updated: v7 -> v8 (1 changes)")).toBeTruthy();
    expect(within(applyZone()).getByText(/v7 → v8/)).toBeTruthy();
    expect(await within(sessionZone()).findByText(/No open session/)).toBeTruthy();
  });

  it("shows an apply refusal", async () => {
    await open(() => sessionResult([proposal(0, { decision: { action: "accept", decided_at: "x" } })]), {
      "dictionary.update_preview": () => obj({ undecided: 0, changes: 1, diff: "", regression: null }),
      "dictionary.update_apply": () => {
        throw new ApiError(409, "conflict", "1 proposal(s) still undecided");
      },
    });
    const apply = (await within(applyZone()).findByRole("button", { name: "Apply to the dictionary" })) as HTMLButtonElement;
    await waitFor(() => expect(apply.disabled).toBe(false));
    fireEvent.click(apply);
    const dialog = await screen.findByRole("dialog");
    fireEvent.input(within(dialog).getByRole("textbox"), { target: { value: "dictionary.update_apply" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Confirm" }));
    expect(await within(applyZone()).findByText(/1 proposal\(s\) still undecided \(conflict\)/)).toBeTruthy();
  });

  it("discards the session after the confirmation", async () => {
    let gone = false;
    await open(() => (gone ? noSession() : sessionResult([proposal(0)])), {
      "dictionary.update_discard": () => {
        gone = true;
        return obj({ session_id: SID }, "session discarded: " + SID);
      },
    });
    fireEvent.click(await within(sessionZone()).findByRole("button", { name: "Discard session" }));
    const dialog = await screen.findByRole("dialog");
    fireEvent.input(within(dialog).getByRole("textbox"), { target: { value: "dictionary.update_discard" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Confirm" }));
    await waitFor(() => expect(run).toHaveBeenCalledWith("dictionary.update_discard", { session_id: SID }, true));
    expect(await within(sessionZone()).findByText(/No open session/)).toBeTruthy();
  });

  it("shows a stale session with the host's reason and discards it by its id", async () => {
    // The id comes from the result, not from the message text.
    const staleId = "stale-session-7";
    const reason = "the dictionary on disk is not the one this session started from";
    await open(
      () =>
        obj({
          session: { id: staleId, status: "open", started_at: "2026-11-12T10:00:00Z", snapshot_refs: null, dictionary_version: "v6" },
          proposals: [],
          undecided: 0,
          stale: true,
          stale_reason: reason,
        }),
      { "dictionary.update_discard": () => obj({ session_id: staleId }) },
    );
    expect(await within(sessionZone()).findByText(reason)).toBeTruthy();
    // A stale session is neither reviewed nor applied.
    expect(screen.queryByRole("region", { name: "Selected item" })).toBeNull();
    expect(within(applyZone()).queryByRole("button", { name: "Apply to the dictionary" })).toBeNull();
    fireEvent.click(within(sessionZone()).getByRole("button", { name: "Discard session" }));
    const dialog = await screen.findByRole("dialog");
    fireEvent.input(within(dialog).getByRole("textbox"), { target: { value: "dictionary.update_discard" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Confirm" }));
    await waitFor(() => expect(run).toHaveBeenCalledWith("dictionary.update_discard", { session_id: staleId }, true));
  });

  it("shows a load error with Retry and no discard", async () => {
    await open(() => {
      throw new ApiError(409, "conflict", "dictionary changed since session x started — resolve manually before resuming");
    });
    expect(await within(sessionZone()).findByRole("button", { name: "Retry" })).toBeTruthy();
    expect(within(sessionZone()).queryByRole("button", { name: "Discard session" })).toBeNull();
  });

  it("shows why the regression check could not run", async () => {
    await open(() => sessionResult([proposal(0, { decision: { action: "accept", decided_at: "x" } })]), {
      "dictionary.update_preview": () =>
        obj({ undecided: 0, changes: 1, diff: "", regression: null, regression_error: "regression cases do not fit this dictionary" }),
    });
    expect(await within(applyZone()).findByText(/regression cases do not fit this dictionary/)).toBeTruthy();
  });

  it("says not to review the session in the command line and here at the same time", async () => {
    await open(noSession);
    expect(screen.getByText(/do not review it in both at the same time/)).toBeTruthy();
  });

  it("blocks A and R while a skip is in flight", async () => {
    let release: (() => void) | undefined;
    await open(() => sessionResult([proposal(0), proposal(1)]), {
      "dictionary.update_decide": (p) =>
        p.action === "skip"
          ? new Promise<RunOutcome>((resolve) => {
              release = () => resolve(obj({ index: p.index, action: p.action, undecided: 1 }));
            })
          : obj({ index: p.index, action: p.action, undecided: 1 }),
    });
    await within(card()).findByText("number 12 → 21");
    fireEvent.keyDown(window, { key: "s" });
    await waitFor(() => expect(calls("dictionary.update_decide")).toHaveLength(1));
    fireEvent.keyDown(window, { key: "a" });
    fireEvent.keyDown(window, { key: "r" });
    expect(calls("dictionary.update_decide")).toHaveLength(1);
    release!();
    await waitFor(() => expect(calls("dictionary.update_session").length).toBeGreaterThan(1));
  });

  it("disables Preview changes while the editor is open", async () => {
    await open(() => sessionResult([proposal(0)]));
    await within(card()).findByText("number 12 → 21");
    const previewBtn = within(applyZone()).getByRole("button", { name: "Preview changes" }) as HTMLButtonElement;
    expect(previewBtn.disabled).toBe(false);
    fireEvent.keyDown(window, { key: "e" });
    await screen.findByRole("textbox", { name: "Category" });
    expect(previewBtn.disabled).toBe(true);
  });

  it("fetches and starts a session as a job, then loads it", async () => {
    let started = false;
    const job: Job = { id: "j1", action: "dictionary.update_start", actor: "ana", status: "queued", created_at: "2026-11-12T10:00:00Z" };
    let end: ((j: Job) => void) | undefined;
    events.mockImplementation((_id, _line, onEnd) => {
      end = onEnd;
      return () => {};
    });
    await open(() => (started ? sessionResult([proposal(0)]) : noSession()), {
      "dictionary.update_start": () => ({ job }),
    });
    fireEvent.click(await within(sessionZone()).findByRole("button", { name: "Fetch and start" }));
    await waitFor(() => expect(run).toHaveBeenCalledWith("dictionary.update_start", {}, false));
    expect(await within(sessionZone()).findByRole("link", { name: "j1" })).toBeTruthy();
    started = true;
    end!({ ...job, status: "succeeded", result: { render: "object", object: { session_id: SID, proposals: 1 }, summary: "1 proposals from three sources" } });
    expect(await within(sessionZone()).findByText(SID)).toBeTruthy();
    expect(within(sessionZone()).getByText("1 proposals from three sources")).toBeTruthy();
  });

  it("shows a failed start job with its error", async () => {
    const job: Job = { id: "j2", action: "dictionary.update_start", actor: "ana", status: "queued", created_at: "2026-11-12T10:00:00Z" };
    events.mockImplementation((_id, _line, onEnd) => {
      setTimeout(() => onEnd({ ...job, status: "failed", error: "dictionary update: fetch roster: timeout" }));
      return () => {};
    });
    await open(noSession, { "dictionary.update_start": () => ({ job }) });
    fireEvent.click(await within(sessionZone()).findByRole("button", { name: "Fetch and start" }));
    expect(await within(sessionZone()).findByText(/fetch roster: timeout/)).toBeTruthy();
  });
});
