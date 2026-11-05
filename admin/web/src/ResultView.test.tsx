import { render, screen, fireEvent } from "@testing-library/preact";
import { ResultView } from "./components/ResultView";

describe("ResultView", () => {
  it("renders table columns in order with summary", () => {
    render(
      <ResultView
        result={{
          render: "table",
          summary: "2 claims",
          columns: [{ key: "b", label: "Second" }, { key: "a", label: "First" }],
          rows: [{ a: 2, b: "x" }, { a: 1, b: "y" }],
        }}
      />,
    );
    expect(screen.getByText("2 claims")).toBeTruthy();
    const heads = screen.getAllByRole("columnheader").map((h) => h.textContent);
    expect(heads).toEqual(["Second", "First"]);
    const firstRow = screen.getAllByRole("row")[1];
    expect(firstRow.textContent).toBe("x2");
  });

  it("sorts by clicking a header", () => {
    render(
      <ResultView
        result={{ render: "table", columns: [{ key: "a", label: "A" }], rows: [{ a: 2 }, { a: 1 }] }}
      />,
    );
    fireEvent.click(screen.getByRole("columnheader", { name: /A/ }));
    expect(screen.getAllByRole("row")[1].textContent).toBe("1");
  });

  it("renders text in a pre and objects as JSON", () => {
    const { container, rerender } = render(<ResultView result={{ render: "text", text: "hello" }} />);
    expect(container.querySelector("pre")?.textContent).toBe("hello");
    rerender(<ResultView result={{ render: "object", object: { k: { n: 1 } } }} />);
    expect(screen.getByText(/k/)).toBeTruthy();
  });
});
