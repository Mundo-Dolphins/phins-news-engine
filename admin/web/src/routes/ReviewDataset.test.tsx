import { render, screen, fireEvent, waitFor } from "@testing-library/preact";
import { ApiError, getSession, runAction } from "../api";
import { ReviewDataset } from "./ReviewDataset";

vi.mock("../api", async (orig) => ({ ...(await orig<typeof import("../api")>()), runAction: vi.fn(), getSession: vi.fn() }));
const run = vi.mocked(runAction);

const PATHS = {
  dataset: "data/editorial_dataset.yml",
  audit: "data/editorial_dataset-review.csv",
  out: "data/editorial_dataset-reviewed.yml",
};

function item(over: Record<string, unknown> = {}) {
  return {
    article_id: "a1",
    audit_reason: "label disagrees with the model",
    title: "First article",
    body: "The stored body.",
    source_id: "example-news",
    source_type: "rss",
    domain: "example.com",
    content_type: "news",
    published_at: "2026-11-09T10:00:00Z",
    label: "publish",
    label_source: "llm",
    url: "https://example.com/first",
    ...over,
  };
}

function queue(items: unknown[], paths = PATHS, total = 5) {
  return {
    result: {
      render: "object" as const,
      object: { paths: { ...paths, log: paths.out + ".review.csv" }, total, pending_count: items.length, items },
    },
  };
}

function decided(id: string, previous: string, reviewed: string) {
  return {
    result: {
      render: "object" as const,
      object: { article_id: id, previous_label: previous, reviewed_label: reviewed },
      summary: `${id}: ${previous} → ${reviewed}`,
    },
  };
}

describe("ReviewDataset", () => {
  beforeEach(() => {
    run.mockReset();
    vi.mocked(getSession).mockResolvedValue({ username: "ana", mode: "local" });
  });

  it("renders the header counts, the paths and the card", async () => {
    run.mockResolvedValueOnce(queue([item(), item({ article_id: "a2", title: "Second" })]));
    render(<ReviewDataset />);
    await screen.findByRole("heading", { name: "First article" });
    expect(run).toHaveBeenCalledWith("train.review_dataset_queue", {}, false);
    expect(screen.getByText("2 / 5")).toBeTruthy();
    expect((screen.getByLabelText("Dataset") as HTMLInputElement).value).toBe(PATHS.dataset);
    expect((screen.getByLabelText("Audit") as HTMLInputElement).value).toBe(PATHS.audit);
    expect((screen.getByLabelText("Reviewed copy") as HTMLInputElement).value).toBe(PATHS.out);
    expect(screen.getByText("Do not review the same copy from the CLI at the same time.")).toBeTruthy();
    expect(screen.getByText("label disagrees with the model")).toBeTruthy();
    expect(screen.getByText("The stored body.")).toBeTruthy();
    expect(screen.getAllByText(/example-news/).length).toBeGreaterThan(0);
  });

  it("links the original for an https url", async () => {
    run.mockResolvedValueOnce(queue([item()]));
    render(<ReviewDataset />);
    const link = await screen.findByRole("link", { name: /Open original/ });
    expect(link.getAttribute("href")).toBe("https://example.com/first");
    expect(link.getAttribute("target")).toBe("_blank");
    expect(link.getAttribute("rel")).toBe("noopener noreferrer");
    expect(link.hasAttribute("data-review-open")).toBe(true);
  });

  it("shows the domain as text when the url is empty", async () => {
    run.mockResolvedValueOnce(queue([item({ url: "" })]));
    render(<ReviewDataset />);
    await screen.findByRole("heading", { name: "First article" });
    expect(screen.queryByRole("link", { name: /Open original/ })).toBeNull();
    expect(screen.getByText(/article no longer stored/)).toBeTruthy();
    expect(document.querySelector(".review-original")?.textContent).toContain("example.com");
  });

  it("posts confirm on C with the loaded paths and removes the item", async () => {
    run.mockResolvedValueOnce(queue([item(), item({ article_id: "a2", title: "Second" })]));
    run.mockResolvedValueOnce(decided("a1", "publish", "publish"));
    render(<ReviewDataset />);
    await screen.findByRole("heading", { name: "First article" });
    fireEvent.keyDown(window, { key: "c" });
    await waitFor(() =>
      expect(run).toHaveBeenCalledWith(
        "train.review_dataset_decide",
        { ...PATHS, article_id: "a1", decision: "confirm" },
        false,
      ),
    );
    expect(await screen.findByRole("heading", { name: "Second" })).toBeTruthy();
    expect(screen.queryByText("First article")).toBeNull();
    expect(screen.getByText("a1: publish → publish")).toBeTruthy();
    expect(screen.getByText("1 / 5")).toBeTruthy();
  });

  it("posts reject on R and publish on P", async () => {
    run.mockResolvedValueOnce(queue([item(), item({ article_id: "a2", title: "Second" })]));
    run.mockResolvedValueOnce(decided("a1", "publish", "reject"));
    run.mockResolvedValueOnce(decided("a2", "publish", "publish"));
    render(<ReviewDataset />);
    await screen.findByRole("heading", { name: "First article" });
    fireEvent.keyDown(window, { key: "r" });
    await waitFor(() =>
      expect(run).toHaveBeenCalledWith("train.review_dataset_decide", { ...PATHS, article_id: "a1", decision: "reject" }, false),
    );
    await screen.findByRole("heading", { name: "Second" });
    fireEvent.keyDown(window, { key: "p" });
    await waitFor(() =>
      expect(run).toHaveBeenCalledWith("train.review_dataset_decide", { ...PATHS, article_id: "a2", decision: "publish" }, false),
    );
  });

  it("treats an already reviewed conflict as info and removes the item", async () => {
    run.mockResolvedValueOnce(queue([item(), item({ article_id: "a2", title: "Second" })]));
    run.mockRejectedValueOnce(new ApiError(409, "conflict", "a1: already reviewed"));
    render(<ReviewDataset />);
    await screen.findByRole("heading", { name: "First article" });
    fireEvent.keyDown(window, { key: "c" });
    const note = await screen.findByText(/a1 was already reviewed/);
    expect(note.getAttribute("role")).toBe("status");
    expect(screen.queryByText("First article")).toBeNull();
    expect(screen.getByRole("heading", { name: "Second" })).toBeTruthy();
  });

  it("shows other decide errors as error notices and keeps the item", async () => {
    run.mockResolvedValueOnce(queue([item()]));
    run.mockRejectedValueOnce(new ApiError(409, "conflict", "reviewed copy does not match the dataset; use a new --out path"));
    render(<ReviewDataset />);
    await screen.findByRole("heading", { name: "First article" });
    fireEvent.keyDown(window, { key: "p" });
    const err = await screen.findByText(/use a new --out path \(conflict\)/);
    expect(err.getAttribute("role")).toBe("alert");
    expect(screen.getByRole("heading", { name: "First article" })).toBeTruthy();
  });

  it("re-queries with the edited paths on Apply and decides with them", async () => {
    const other = { dataset: "/tmp/d.yml", audit: "/tmp/a.csv", out: "/tmp/o.yml" };
    run.mockResolvedValueOnce(queue([item()]));
    run.mockResolvedValueOnce(queue([item({ article_id: "b1", title: "Other" })], other, 1));
    run.mockResolvedValueOnce(decided("b1", "publish", "reject"));
    render(<ReviewDataset />);
    await screen.findByRole("heading", { name: "First article" });
    fireEvent.input(screen.getByLabelText("Dataset"), { target: { value: other.dataset } });
    fireEvent.input(screen.getByLabelText("Audit"), { target: { value: other.audit } });
    fireEvent.input(screen.getByLabelText("Reviewed copy"), { target: { value: other.out } });
    fireEvent.click(screen.getByRole("button", { name: "Apply" }));
    await waitFor(() => expect(run).toHaveBeenLastCalledWith("train.review_dataset_queue", other, false));
    expect(await screen.findByRole("heading", { name: "Other" })).toBeTruthy();
    fireEvent.keyDown(window, { key: "r" });
    await waitFor(() =>
      expect(run).toHaveBeenLastCalledWith("train.review_dataset_decide", { ...other, article_id: "b1", decision: "reject" }, false),
    );
  });

  it("does not send a cleared path field as an empty string", async () => {
    run.mockResolvedValueOnce(queue([item()]));
    run.mockResolvedValueOnce(queue([item()]));
    render(<ReviewDataset />);
    await screen.findByRole("heading", { name: "First article" });
    fireEvent.input(screen.getByLabelText("Audit"), { target: { value: "" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply" }));
    await waitFor(() =>
      expect(run).toHaveBeenLastCalledWith(
        "train.review_dataset_queue",
        { dataset: PATHS.dataset, out: PATHS.out },
        false,
      ),
    );
  });

  it("keeps unapplied path edits and the loaded paths on a tab refresh", async () => {
    run.mockResolvedValue(queue([item()]));
    render(<ReviewDataset />);
    await screen.findByRole("heading", { name: "First article" });
    fireEvent.input(screen.getByLabelText("Dataset"), { target: { value: "/tmp/editing.yml" } });
    const spy = vi.spyOn(document, "visibilityState", "get").mockReturnValue("visible");
    document.dispatchEvent(new Event("visibilitychange"));
    spy.mockRestore();
    await waitFor(() => expect(run).toHaveBeenCalledTimes(2));
    expect(run).toHaveBeenLastCalledWith("train.review_dataset_queue", PATHS, false);
    expect((screen.getByLabelText("Dataset") as HTMLInputElement).value).toBe("/tmp/editing.yml");
  });

  it("offers a retry when the load fails and keeps the path fields", async () => {
    run.mockRejectedValueOnce(new ApiError(409, "conflict", "audit file is empty"));
    run.mockResolvedValueOnce(queue([item()]));
    render(<ReviewDataset />);
    expect(await screen.findByText(/Could not load the dataset review: audit file is empty/)).toBeTruthy();
    expect(screen.getByLabelText("Dataset")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    expect(await screen.findByRole("heading", { name: "First article" })).toBeTruthy();
  });

  it("shows the empty state", async () => {
    run.mockResolvedValueOnce(queue([]));
    render(<ReviewDataset />);
    expect(await screen.findByText(/Nothing to review/)).toBeTruthy();
    expect(screen.getByText("0 / 5")).toBeTruthy();
  });

  it("shows a local-mode notice in production mode", async () => {
    vi.mocked(getSession).mockResolvedValue({ username: "ana", mode: "production" });
    render(<ReviewDataset />);
    expect(await screen.findByText("Review screens are only available in local mode.")).toBeTruthy();
    expect(run).not.toHaveBeenCalled();
  });
});
