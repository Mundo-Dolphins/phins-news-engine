import { render, screen, fireEvent, waitFor } from "@testing-library/preact";
import { ProposalEditor } from "./ProposalEditor";

function setup(onSave = vi.fn().mockResolvedValue(undefined)) {
  const onClose = vi.fn();
  const outside = vi.fn();
  window.addEventListener("keydown", outside);
  const utils = render(
    <ProposalEditor
      open
      proposal={{ index: 2, canonical_key: "ann-example", category: "players", detail: "new on the roster" }}
      onSave={onSave}
      onClose={onClose}
    />,
  );
  const category = screen.getByRole("textbox", { name: "Category" }) as HTMLInputElement;
  const note = screen.getByRole("textbox", { name: "Note" }) as HTMLInputElement;
  return { ...utils, category, note, onSave, onClose, outside };
}

describe("ProposalEditor", () => {
  afterEach(() => vi.restoreAllMocks());

  it("renders nothing when closed", () => {
    render(
      <ProposalEditor open={false} proposal={{ index: 0, canonical_key: "k", category: "c", detail: "" }} onSave={vi.fn()} onClose={vi.fn()} />,
    );
    expect(screen.queryByRole("textbox")).toBeNull();
  });

  it("focuses the pre-filled category and starts with an empty note", () => {
    const { category, note } = setup();
    expect(document.activeElement).toBe(category);
    expect(category.value).toBe("players");
    expect(note.value).toBe("");
  });

  it("keeps every key typed in it away from page shortcuts", () => {
    const { category, note, outside, onSave } = setup();
    for (const key of ["a", "r", "s", "e", "j"]) {
      fireEvent.keyDown(category, { key });
      fireEvent.keyDown(note, { key });
    }
    (document.body as HTMLElement).focus();
    fireEvent.keyDown(document.body, { key: "a" });
    expect(outside).not.toHaveBeenCalled();
    expect(onSave).not.toHaveBeenCalled();
    expect(document.activeElement).toBe(category);
  });

  it("shows the detail the note produces and saves category and note with Enter", async () => {
    const { category, note, onSave } = setup();
    fireEvent.input(category, { target: { value: "staff" } });
    fireEvent.input(note, { target: { value: "checked by hand" } });
    expect(screen.getByText("new on the roster; checked by hand")).toBeTruthy();
    fireEvent.keyDown(note, { key: "Enter" });
    await waitFor(() => expect(onSave).toHaveBeenCalledWith("staff", "checked by hand"));
    expect(onSave).toHaveBeenCalledTimes(1);
  });

  it("saves with the Save button", async () => {
    const { note, onSave } = setup();
    fireEvent.input(note, { target: { value: "x" } });
    fireEvent.click(screen.getByRole("button", { name: /Save/ }));
    await waitFor(() => expect(onSave).toHaveBeenCalledWith("players", "x"));
  });

  it("shows a save error and stays open", async () => {
    const { note, onClose } = setup(vi.fn().mockRejectedValue(new Error("session is not open")));
    fireEvent.keyDown(note, { key: "Enter" });
    expect(await screen.findByText(/session is not open/)).toBeTruthy();
    expect(onClose).not.toHaveBeenCalled();
  });

  it("closes with Escape, from a field or from outside, and with Cancel", () => {
    const { note, onClose } = setup();
    fireEvent.keyDown(note, { key: "Escape" });
    expect(onClose).toHaveBeenCalledTimes(1);
    fireEvent.keyDown(document.body, { key: "Escape" });
    expect(onClose).toHaveBeenCalledTimes(2);
    fireEvent.click(screen.getByRole("button", { name: /Cancel/ }));
    expect(onClose).toHaveBeenCalledTimes(3);
  });
});
