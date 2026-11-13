import { render, screen, fireEvent, waitFor, within } from "@testing-library/preact";
import { ApiError, getSession, runAction, type RunOutcome } from "../api";
import { ReviewTerms } from "./ReviewTerms";

vi.mock("../api", async (orig) => ({
  ...(await orig<typeof import("../api")>()),
  runAction: vi.fn(),
  getSession: vi.fn(),
}));
const run = vi.mocked(runAction);

const obj = (object: unknown, summary = ""): RunOutcome => ({ result: { render: "object", object, summary } });

function cycleState(step: string | null, status = "pending") {
  return obj({
    cycle:
      step === null
        ? null
        : {
            cycle_id: "20261113T090000-0a0b0c0d",
            started_at: "2026-11-13T09:00:00Z",
            step,
            session_id: "sess-1",
            steps: { [step]: { status, result: null, error: "", updated_at: "2026-11-13T09:10:00Z" } },
            sample_size: 0,
            entities_verification: "local",
          },
    available: { sync: true, telegram: true },
    order: ["sync", "telegram_import", "roster_sync", "article_review", "entities", "event_rules", "mined_terms", "training", "summary"],
    skippable: [],
    disabled: {},
  });
}

const CATEGORIES = ["coaching_staff", "people_context", "official_names"];

type Decision = { status: string; category: string } | null;

function term(name: string, over: Record<string, unknown> = {}) {
  return {
    term: name,
    support: 2,
    first_seen_article: { id: "a-" + name, title: "First story about " + name, url: "https://news.example.com/" + encodeURIComponent(name) },
    category_guess: "people_context",
    decision: null as Decision,
    ...over,
  };
}

type Item = ReturnType<typeof term>;
type Handler = (params: Record<string, unknown>, confirm: boolean) => Promise<RunOutcome> | RunOutcome;

function serve(items: Item[], more: Record<string, Handler> = {}, state = () => cycleState("mined_terms")) {
  const current = items.map((t) => ({ ...t }));
  const undecided = () => current.filter((t) => !t.decision).length;
  run.mockImplementation(async (name, params, confirm) => {
    const h = more[name];
    if (h) return h(params, confirm);
    switch (name) {
      case "cycle.state":
        return state();
      case "cycle.terms_queue":
        return obj({ items: current, categories: CATEGORIES, undecided: undecided(), warnings: [] });
      case "cycle.terms_decide": {
        const t = current.find((x) => x.term === params.term)!;
        const reject = params.action === "reject";
        const category = reject ? "" : ((params.category as string) || t.category_guess);
        t.decision = { status: reject ? "rejected" : "approved", category };
        return obj({
          term: t.term,
          action: params.action,
          category,
          undecided: undecided(),
          notices: reject ? [`  added "${t.term}" to ignored_terms in policy.yaml`] : [],
        });
      }
    }
    throw new Error("unexpected action " + name);
  });
}

const calls = (name: string) => run.mock.calls.filter(([n]) => n === name);
const card = () => screen.getByRole("region", { name: "Selected item" });

async function open(items: Item[], more: Record<string, Handler> = {}, onDone?: () => void) {
  serve(items, more);
  render(<ReviewTerms onDone={onDone} />);
  await screen.findByRole("heading", { name: "Mined term review" });
  await waitFor(() => expect(calls("cycle.terms_queue").length).toBeGreaterThan(0));
}

describe("ReviewTerms", () => {
  beforeEach(() => {
    run.mockReset();
    vi.mocked(getSession).mockResolvedValue({ username: "ana", mode: "local" });
  });

  it("asks to open it from the editorial cycle when no cycle is at the mined terms", async () => {
    serve([], {}, () => cycleState("article_review"));
    render(<ReviewTerms />);
    expect(await screen.findByText(/Open from the editorial cycle/)).toBeTruthy();
    expect(calls("cycle.terms_queue").length).toBe(0);
  });

  it("shows the term, its support, the first-seen article and the guess", async () => {
    await open([term("Ezo Quarrington", { support: 3 })]);
    const c = card();
    expect(within(c).getByRole("heading", { name: "Ezo Quarrington" })).toBeTruthy();
    expect(within(c).getByText("support 3")).toBeTruthy();
    const link = within(c).getByRole("link", { name: "First story about Ezo Quarrington" });
    expect(link.getAttribute("href")).toBe("https://news.example.com/Ezo%20Quarrington");
    expect(link.hasAttribute("data-review-open")).toBe(true);
    expect(within(c).getByText("people_context")).toBeTruthy();
  });

  it("approves with A into the guess when it is a category, without a picker", async () => {
    await open([term("Ezo Quarrington"), term("Ula Brint")]);
    fireEvent.keyDown(window, { key: "a" });
    await waitFor(() => expect(run).toHaveBeenCalledWith("cycle.terms_decide", { term: "Ezo Quarrington", action: "approve" }, false));
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(await within(card()).findByRole("heading", { name: "Ula Brint" })).toBeTruthy();
  });

  it("rejects with R and shows the notices", async () => {
    await open([term("leads", { category_guess: "" })]);
    fireEvent.keyDown(window, { key: "r" });
    await waitFor(() => expect(run).toHaveBeenCalledWith("cycle.terms_decide", { term: "leads", action: "reject" }, false));
    expect(await screen.findByText(/added "leads" to ignored_terms/)).toBeTruthy();
  });

  it("opens the picker on A with a guess that is not a category; typing in it decides nothing; Escape closes it", async () => {
    await open([term("Ezo Quarrington", { category_guess: "unknown_bucket" })]);
    fireEvent.keyDown(window, { key: "a" });
    const dialog = await screen.findByRole("dialog", { name: /Choose a category/ });
    const field = within(dialog).getByRole("combobox");
    for (const key of ["a", "r", "c"]) {
      fireEvent.keyDown(field, { key });
      // Outside the dialog too (focus lost to the page).
      fireEvent.keyDown(document.body, { key });
    }
    fireEvent.input(field, { target: { value: "arc" } });
    expect(calls("cycle.terms_decide").length).toBe(0);
    fireEvent.keyDown(field, { key: "Escape" });
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(calls("cycle.terms_decide").length).toBe(0);
    // The screen answers its keys again.
    fireEvent.keyDown(window, { key: "r" });
    await waitFor(() => expect(run).toHaveBeenCalledWith("cycle.terms_decide", { term: "Ezo Quarrington", action: "reject" }, false));
  });

  it("approves into the category picked for a non-category guess", async () => {
    await open([term("Ezo Quarrington", { category_guess: "unknown_bucket" })]);
    fireEvent.keyDown(window, { key: "a" });
    const dialog = await screen.findByRole("dialog", { name: /Choose a category/ });
    fireEvent.click(within(dialog).getByRole("option", { name: "official_names" }));
    await waitFor(() =>
      expect(run).toHaveBeenCalledWith("cycle.terms_decide", { term: "Ezo Quarrington", action: "approve", category: "official_names" }, false),
    );
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("categorizes with C: filter, arrows and Enter pick the category", async () => {
    await open([term("Ezo Quarrington")]);
    fireEvent.keyDown(window, { key: "c" });
    const dialog = await screen.findByRole("dialog", { name: /Choose a category/ });
    const field = within(dialog).getByRole("combobox");
    fireEvent.input(field, { target: { value: "staff" } });
    expect(within(dialog).getAllByRole("option").map((o) => o.textContent)).toEqual(["coaching_staff"]);
    fireEvent.input(field, { target: { value: "" } });
    fireEvent.keyDown(field, { key: "ArrowDown" });
    fireEvent.keyDown(field, { key: "Enter" });
    await waitFor(() =>
      expect(run).toHaveBeenCalledWith("cycle.terms_decide", { term: "Ezo Quarrington", action: "categorize", category: "people_context" }, false),
    );
  });

  it("shows the refusal to re-decide a rejected term and keeps it", async () => {
    await open([term("leads", { decision: { status: "rejected", category: "" } })], {
      "cycle.terms_decide": () => {
        throw new ApiError(409, "conflict", '"leads" was already rejected and added to ignored_terms');
      },
    });
    expect(within(screen.getByRole("list", { name: "Queue" })).getByText("Rejected")).toBeTruthy();
    fireEvent.keyDown(window, { key: "a" });
    expect(await screen.findByText(/already rejected/)).toBeTruthy();
    expect(within(card()).getByRole("heading", { name: "leads" })).toBeTruthy();
  });

  it("keeps Finish disabled while undecided; the confirmed finish posts once and calls onDone", async () => {
    const onDone = vi.fn();
    await open(
      [term("Ezo Quarrington")],
      { "cycle.terms_finish": () => obj({ added: 1, entries: 1, from: "1.0.0", to: "1.0.1" }, "dictionary updated") },
      onDone,
    );
    const finish = screen.getByRole("button", { name: "Finish and write the dictionary" }) as HTMLButtonElement;
    expect(finish.disabled).toBe(true);
    fireEvent.keyDown(window, { key: "a" });
    await waitFor(() => expect(finish.disabled).toBe(false));
    fireEvent.click(finish);
    const dialog = await screen.findByRole("dialog", { name: /Confirm/ });
    // The typed name never reaches the review's keys.
    const field = within(dialog).getByLabelText("Action name");
    fireEvent.keyDown(field, { key: "r" });
    fireEvent.input(field, { target: { value: "cycle.terms_finish" } });
    const confirm = within(dialog).getByRole("button", { name: "Confirm" });
    fireEvent.click(confirm);
    fireEvent.click(confirm);
    await waitFor(() => expect(onDone).toHaveBeenCalledTimes(1));
    expect(calls("cycle.terms_finish")).toEqual([["cycle.terms_finish", {}, true]]);
    expect(calls("cycle.terms_decide").length).toBe(1);
    expect(await screen.findByText(/1\.0\.0 → 1\.0\.1/)).toBeTruthy();
  });

  it("explains when the mined terms are not prepared yet", async () => {
    serve([], {
      "cycle.terms_queue": () => {
        throw new ApiError(409, "conflict", "prepare the mined terms first");
      },
    });
    render(<ReviewTerms />);
    expect(await screen.findByText(/prepare the mined terms first/)).toBeTruthy();
  });
});
