import { render, screen, fireEvent } from "@testing-library/preact";
import { ConfirmDialog } from "./components/ConfirmDialog";

describe("ConfirmDialog", () => {
  it("enables Confirm only for the exact action name", () => {
    const onConfirm = vi.fn();
    render(<ConfirmDialog name="claims.release" onConfirm={onConfirm} onCancel={() => {}} />);
    const btn = screen.getByRole("button", { name: "Confirm" }) as HTMLButtonElement;
    const input = screen.getByLabelText("Action name");
    expect(btn.disabled).toBe(true);
    fireEvent.input(input, { target: { value: "claims.releas" } });
    expect(btn.disabled).toBe(true);
    fireEvent.input(input, { target: { value: "claims.release" } });
    expect(btn.disabled).toBe(false);
    fireEvent.click(btn);
    expect(onConfirm).toHaveBeenCalled();
  });

  it("closes on Escape", () => {
    const onCancel = vi.fn();
    render(<ConfirmDialog name="x" onConfirm={() => {}} onCancel={onCancel} />);
    fireEvent.keyDown(document, { key: "Escape" });
    expect(onCancel).toHaveBeenCalled();
  });
});
