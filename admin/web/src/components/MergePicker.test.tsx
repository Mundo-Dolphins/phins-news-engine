import { render, screen, fireEvent, waitFor } from "@testing-library/preact";
import { ApiError, runAction } from "../api";
import { MergePicker } from "./MergePicker";

vi.mock("../api", async (orig) => ({ ...(await orig<typeof import("../api")>()), runAction: vi.fn() }));
const run = vi.mocked(runAction);

function results(rows: { id: string; name: string; type?: string; team?: string }[]) {
  return {
    result: {
      render: "object" as const,
      object: { results: rows.map((r) => ({ type: "player", team: "", terms: [r.name], ...r })) },
    },
  };
}

const TWO = results([
  { id: "player-ann-example", name: "Ann Example", team: "team-one" },
  { id: "player-anne-sample", name: "Anne Sample" },
]);

function setup(initialQuery = "Ann") {
  const onPick = vi.fn();
  const onClose = vi.fn();
  const utils = render(<MergePicker open initialQuery={initialQuery} onPick={onPick} onClose={onClose} />);
  const input = screen.getByRole("combobox", { name: /Search the entity catalog/ }) as HTMLInputElement;
  return { ...utils, onPick, onClose, input };
}

describe("MergePicker", () => {
  beforeEach(() => run.mockReset());

  it("renders nothing when closed", () => {
    render(<MergePicker open={false} initialQuery="Ann" onPick={() => {}} onClose={() => {}} />);
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(run).not.toHaveBeenCalled();
  });

  it("is a modal dialog with the search focused and pre-filled", async () => {
    run.mockResolvedValue(TWO);
    const { input } = setup();
    const dialog = screen.getByRole("dialog");
    expect(dialog.getAttribute("aria-modal")).toBe("true");
    expect(document.activeElement).toBe(input);
    expect(input.value).toBe("Ann");
    await waitFor(() => expect(run).toHaveBeenCalledWith("entities.catalog_search", { query: "Ann", limit: 20 }, false));
    expect(await screen.findByRole("option", { name: /Ann Example/ })).toBeTruthy();
    const first = screen.getByRole("option", { name: /Ann Example/ });
    expect(first.textContent).toContain("player");
    expect(first.textContent).toContain("team-one");
    expect(first.textContent).toContain("player-ann-example");
  });

  it("debounces the search while typing", async () => {
    run.mockResolvedValue(TWO);
    const { input } = setup("");
    expect(run).not.toHaveBeenCalled();
    fireEvent.input(input, { target: { value: "s" } });
    fireEvent.input(input, { target: { value: "sa" } });
    fireEvent.input(input, { target: { value: "sam" } });
    await waitFor(() => expect(run).toHaveBeenCalledTimes(1));
    expect(run).toHaveBeenCalledWith("entities.catalog_search", { query: "sam", limit: 20 }, false);
  });

  it("moves with the arrow keys and picks the highlighted result with Enter", async () => {
    run.mockResolvedValue(TWO);
    const { input, onPick } = setup();
    await screen.findByRole("option", { name: /Ann Example/ });
    expect(screen.getByRole("option", { name: /Ann Example/ }).getAttribute("aria-selected")).toBe("true");
    fireEvent.keyDown(input, { key: "ArrowDown" });
    expect(screen.getByRole("option", { name: /Anne Sample/ }).getAttribute("aria-selected")).toBe("true");
    fireEvent.keyDown(input, { key: "ArrowDown" });
    expect(screen.getByRole("option", { name: /Anne Sample/ }).getAttribute("aria-selected")).toBe("true");
    fireEvent.keyDown(input, { key: "ArrowUp" });
    expect(screen.getByRole("option", { name: /Ann Example/ }).getAttribute("aria-selected")).toBe("true");
    fireEvent.keyDown(input, { key: "ArrowDown" });
    fireEvent.keyDown(input, { key: "Enter" });
    expect(onPick).toHaveBeenCalledWith("player-anne-sample");
  });

  it("picks a clicked result", async () => {
    run.mockResolvedValue(TWO);
    const { onPick } = setup();
    fireEvent.click(await screen.findByRole("option", { name: /Ann Example/ }));
    expect(onPick).toHaveBeenCalledWith("player-ann-example");
  });

  it("closes on Escape and on Cancel", async () => {
    run.mockResolvedValue(TWO);
    const { input, onClose, onPick } = setup();
    fireEvent.keyDown(input, { key: "Escape" });
    expect(onClose).toHaveBeenCalledTimes(1);
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(onClose).toHaveBeenCalledTimes(2);
    expect(onPick).not.toHaveBeenCalled();
  });

  it("keeps key presses away from page-level shortcuts", async () => {
    run.mockResolvedValue(TWO);
    const page = vi.fn();
    window.addEventListener("keydown", page);
    try {
      const { input } = setup();
      for (const key of ["r", "s", "a", "n"]) fireEvent.keyDown(input, { key });
      // Focus left the dialog (e.g. a click on the backdrop): still not the page's.
      fireEvent.keyDown(document.body, { key: "a" });
      expect(page).not.toHaveBeenCalled();
      expect(document.activeElement).toBe(input);
    } finally {
      window.removeEventListener("keydown", page);
    }
  });

  it("says when nothing matches and shows a search error", async () => {
    run.mockResolvedValueOnce(results([]));
    const { input } = setup("zzz");
    expect(await screen.findByText(/No catalog entity matches/)).toBeTruthy();
    run.mockRejectedValueOnce(new ApiError(400, "invalid_params", 'field "query" must not be empty'));
    fireEvent.input(input, { target: { value: "--" } });
    expect(await screen.findByText(/field "query" must not be empty/)).toBeTruthy();
  });

  it("does not search an empty query", async () => {
    const { input } = setup("   ");
    expect(screen.getByText(/Type a name or alias/)).toBeTruthy();
    fireEvent.input(input, { target: { value: "" } });
    await new Promise((r) => setTimeout(r, 400));
    expect(run).not.toHaveBeenCalled();
  });
});
