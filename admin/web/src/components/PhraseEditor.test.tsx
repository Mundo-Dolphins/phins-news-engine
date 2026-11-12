import { render, screen, fireEvent, waitFor } from "@testing-library/preact";
import { PhraseEditor, type Assessment } from "./PhraseEditor";

function assessment(over: Partial<Assessment> = {}): Assessment {
  return {
    phrase: "agrees to terms",
    passes: true,
    reasons: [],
    impact: { moved: [{ type: "sign", count: 3 }], examples: ["Ann Example agrees to terms"], matches: 3, retyped: 0 },
    ...over,
  };
}

function setup(preview = vi.fn().mockResolvedValue(assessment())) {
  const onSave = vi.fn().mockResolvedValue(undefined);
  const onClose = vi.fn();
  const outside = vi.fn();
  window.addEventListener("keydown", outside);
  const utils = render(
    <PhraseEditor open proposal={{ id: "r1", phrase: "agrees" }} onPreview={preview} onSave={onSave} onClose={onClose} />,
  );
  const input = screen.getByRole("textbox", { name: /New phrase/ }) as HTMLInputElement;
  const save = () => screen.getByRole("button", { name: /Save/ }) as HTMLButtonElement;
  return { ...utils, input, preview, onSave, onClose, outside, save };
}

describe("PhraseEditor", () => {
  it("renders nothing when closed", () => {
    render(<PhraseEditor open={false} proposal={{ id: "r1", phrase: "x" }} onPreview={vi.fn()} onSave={vi.fn()} onClose={vi.fn()} />);
    expect(screen.queryByRole("textbox")).toBeNull();
  });

  it("focuses the pre-filled phrase", () => {
    const { input } = setup();
    expect(document.activeElement).toBe(input);
    expect(input.value).toBe("agrees");
  });

  it("keeps every key typed in it away from page shortcuts", () => {
    const { input, outside, preview } = setup();
    for (const key of ["r", "a", "s", "e", "j"]) fireEvent.keyDown(input, { key });
    // A key landing outside the editor is swallowed and focus returns to the field.
    (document.body as HTMLElement).focus();
    fireEvent.keyDown(document.body, { key: "r" });
    expect(outside).not.toHaveBeenCalled();
    expect(preview).not.toHaveBeenCalled();
    expect(document.activeElement).toBe(input);
  });

  it("previews with Enter and shows the impact and verdict", async () => {
    const { input, preview } = setup();
    fireEvent.input(input, { target: { value: "agrees to terms" } });
    fireEvent.keyDown(input, { key: "Enter" });
    expect(preview).toHaveBeenCalledWith("agrees to terms");
    expect(await screen.findByText(/Passes the checks/)).toBeTruthy();
    expect(screen.getByText("Ann Example agrees to terms")).toBeTruthy();
    expect(screen.getByText(/generic → sign/)).toBeTruthy();
  });

  it("shows a failing preview with its reasons and keeps Save disabled", async () => {
    const { input, save, onSave } = setup(vi.fn().mockResolvedValue(assessment({ passes: false, reasons: ["matches 1 article(s), need 2"] })));
    fireEvent.input(input, { target: { value: "agrees to terms" } });
    fireEvent.keyDown(input, { key: "Enter" });
    expect(await screen.findByText(/Does not pass/)).toBeTruthy();
    expect(screen.getByText("matches 1 article(s), need 2")).toBeTruthy();
    expect(save().disabled).toBe(true);
    fireEvent.keyDown(input, { key: "Enter", ctrlKey: true });
    expect(onSave).not.toHaveBeenCalled();
  });

  it("enables Save only after a passing preview of the current text", async () => {
    const { input, save, onSave } = setup();
    expect(save().disabled).toBe(true);
    fireEvent.input(input, { target: { value: "agrees to terms" } });
    fireEvent.keyDown(input, { key: "Enter", ctrlKey: true });
    expect(onSave).not.toHaveBeenCalled();
    fireEvent.keyDown(input, { key: "Enter" });
    await waitFor(() => expect(save().disabled).toBe(false));
    // Changing the text invalidates the preview.
    fireEvent.input(input, { target: { value: "agrees to term" } });
    expect(save().disabled).toBe(true);
    fireEvent.input(input, { target: { value: "agrees to terms" } });
    fireEvent.keyDown(input, { key: "Enter" });
    await waitFor(() => expect(save().disabled).toBe(false));
    fireEvent.keyDown(input, { key: "Enter", ctrlKey: true });
    await waitFor(() => expect(onSave).toHaveBeenCalledWith("agrees to terms"));
    expect(onSave).toHaveBeenCalledTimes(1);
  });

  it("saves with the Save button", async () => {
    const { input, save, onSave } = setup();
    fireEvent.input(input, { target: { value: "agrees to terms" } });
    fireEvent.keyDown(input, { key: "Enter" });
    await waitFor(() => expect(save().disabled).toBe(false));
    fireEvent.click(save());
    await waitFor(() => expect(onSave).toHaveBeenCalledWith("agrees to terms"));
  });

  it("shows a preview or save error and stays open", async () => {
    const { input, onClose } = setup(vi.fn().mockRejectedValue(new Error("phrase unchanged")));
    fireEvent.keyDown(input, { key: "Enter" });
    expect(await screen.findByText(/phrase unchanged/)).toBeTruthy();
    expect(onClose).not.toHaveBeenCalled();
  });

  it("closes with Escape, from the field or from outside", () => {
    const { input, onClose } = setup();
    fireEvent.keyDown(input, { key: "Escape" });
    expect(onClose).toHaveBeenCalledTimes(1);
    fireEvent.keyDown(document.body, { key: "Escape" });
    expect(onClose).toHaveBeenCalledTimes(2);
  });

  it("closes with Cancel", () => {
    const { onClose } = setup();
    fireEvent.click(screen.getByRole("button", { name: /Cancel/ }));
    expect(onClose).toHaveBeenCalled();
  });
});
