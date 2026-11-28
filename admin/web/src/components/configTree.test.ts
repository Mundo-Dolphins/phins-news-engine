import { describe, expect, it } from "vitest";
import { computeEdits, removeAt, setAt } from "./configTree";

describe("setAt", () => {
  it("sets a nested scalar without mutating the original", () => {
    const orig = { a: { b: 1 } };
    const out = setAt(orig, ["a", "b"], 2);
    expect(out).toEqual({ a: { b: 2 } });
    expect(orig).toEqual({ a: { b: 1 } });
  });
  it("sets an array element", () => {
    expect(setAt({ l: [1, 2, 3] }, ["l", "1"], 9)).toEqual({ l: [1, 9, 3] });
  });
  it("appends to an array", () => {
    expect(setAt({ l: [1] }, ["l", "1"], 2)).toEqual({ l: [1, 2] });
  });
  it("creates missing intermediate keys", () => {
    expect(setAt({}, ["a", "b"], "x")).toEqual({ a: { b: "x" } });
  });
});

describe("removeAt", () => {
  it("removes a map key", () => {
    expect(removeAt({ a: 1, b: 2 }, ["a"])).toEqual({ b: 2 });
  });
  it("removes an array element", () => {
    expect(removeAt({ l: [1, 2, 3] }, ["l", "1"])).toEqual({ l: [1, 3] });
  });
});

describe("computeEdits", () => {
  it("reports no edits when the trees are equal", () => {
    expect(computeEdits({ a: 1 }, { a: 1 })).toEqual([]);
  });
  it("reports a changed scalar", () => {
    expect(computeEdits({ a: 1 }, { a: 2 })).toEqual([{ path: ["a"], value: 2 }]);
  });
  it("reports an added key", () => {
    expect(computeEdits({}, { a: 1 })).toEqual([{ path: ["a"], value: 1 }]);
  });
  it("reports a removed key", () => {
    expect(computeEdits({ a: 1 }, {})).toEqual([{ path: ["a"], remove: true }]);
  });
  it("reports a nested change by full path", () => {
    expect(computeEdits({ a: { b: 1 } }, { a: { b: 2 } })).toEqual([{ path: ["a", "b"], value: 2 }]);
  });
  it("reports an array append", () => {
    expect(computeEdits({ l: [1] }, { l: [1, 2] })).toEqual([{ path: ["l", "1"], value: 2 }]);
  });
  it("reports an array element removal", () => {
    expect(computeEdits({ l: [1, 2] }, { l: [1] })).toEqual([{ path: ["l", "1"], remove: true }]);
  });
  it("emits a remove then a set for a structural type change", () => {
    expect(computeEdits({ a: 1 }, { a: { b: 1 } })).toEqual([
      { path: ["a"], remove: true },
      { path: ["a"], value: { b: 1 } },
    ]);
  });
});
