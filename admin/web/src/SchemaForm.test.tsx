import { render, screen, fireEvent } from "@testing-library/preact";
import { SchemaForm } from "./components/SchemaForm";
import type { Schema } from "./api";

const schema: Schema = {
  type: "object",
  required: ["name"],
  additionalProperties: false,
  properties: {
    name: { type: "string", description: "Claim name" },
    state: { type: "string", enum: ["pending", "failed"], default: "failed" },
    force: { type: "boolean" },
    limit: { type: "integer", default: 20 },
    ratio: { type: "number" },
    tags: { type: "array", items: { type: "string" } },
  },
};

describe("SchemaForm", () => {
  it("renders each type and prefills defaults", () => {
    render(<SchemaForm schema={schema} onSubmit={() => {}} />);
    expect((screen.getByLabelText(/^name/) as HTMLInputElement).type).toBe("text");
    const sel = screen.getByLabelText("state") as HTMLSelectElement;
    expect(sel.tagName).toBe("SELECT");
    expect(sel.value).toBe("failed");
    expect((screen.getByLabelText("force") as HTMLInputElement).type).toBe("checkbox");
    const limit = screen.getByLabelText("limit") as HTMLInputElement;
    expect(limit.type).toBe("number");
    expect(limit.value).toBe("20");
    expect((screen.getByLabelText("ratio") as HTMLInputElement).type).toBe("number");
    expect(screen.getByText("Claim name")).toBeTruthy();
    expect(screen.getByRole("button", { name: /add tags/i })).toBeTruthy();
  });

  it("blocks submit when a required field is missing", () => {
    const onSubmit = vi.fn();
    render(<SchemaForm schema={schema} onSubmit={onSubmit} />);
    fireEvent.click(screen.getByRole("button", { name: "Run" }));
    expect(onSubmit).not.toHaveBeenCalled();
    expect(screen.getByText(/name is required/i)).toBeTruthy();
  });

  it("emits typed values and omits empty optional fields", () => {
    const onSubmit = vi.fn();
    render(<SchemaForm schema={schema} onSubmit={onSubmit} />);
    fireEvent.input(screen.getByLabelText(/^name/), { target: { value: "x" } });
    fireEvent.input(screen.getByLabelText("limit"), { target: { value: "5" } });
    fireEvent.input(screen.getByLabelText("ratio"), { target: { value: "1.5" } });
    fireEvent.click(screen.getByLabelText("force"));
    fireEvent.click(screen.getByRole("button", { name: /add tags/i }));
    fireEvent.input(screen.getAllByLabelText(/^tags/)[0], { target: { value: "a" } });
    fireEvent.click(screen.getByRole("button", { name: /add tags/i }));
    fireEvent.click(screen.getByRole("button", { name: "Run" }));
    expect(onSubmit).toHaveBeenCalledWith({
      name: "x", state: "failed", limit: 5, ratio: 1.5, force: true, tags: ["a"],
    });
  });

  it("omits an emptied optional number", () => {
    const onSubmit = vi.fn();
    render(<SchemaForm schema={schema} onSubmit={onSubmit} />);
    fireEvent.input(screen.getByLabelText(/^name/), { target: { value: "x" } });
    fireEvent.input(screen.getByLabelText("limit"), { target: { value: "" } });
    fireEvent.click(screen.getByRole("button", { name: "Run" }));
    const p = onSubmit.mock.calls[0][0];
    expect("limit" in p).toBe(false);
    expect("ratio" in p).toBe(false);
  });
});
