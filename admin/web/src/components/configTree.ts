// configTree.ts holds the pure tree operations the config editor uses:
// immutably set/remove a value at a key path, and diff an original value
// against the edited working tree to recover the list of surgical edits the
// backend applies (only the leaves that actually changed).

export interface ConfigEdit {
  path: string[];
  value?: any;
  remove?: boolean;
}

/** isMap is true for plain objects (not arrays, not null). */
export const isMap = (v: any): v is Record<string, any> =>
  typeof v === "object" && v !== null && !Array.isArray(v);

/** setAt returns a new tree with the value at path replaced by newVal. */
export function setAt(node: any, path: string[], newVal: any): any {
  if (path.length === 0) return newVal;
  const [head, ...rest] = path;
  if (Array.isArray(node)) {
    const copy = node.slice();
    copy[Number(head)] = setAt(copy[Number(head)], rest, newVal);
    return copy;
  }
  if (isMap(node)) {
    const copy = { ...node };
    copy[head] = setAt(copy[head], rest, newVal);
    return copy;
  }
  // A scalar node but the path has more segments: rebuild as a map.
  return { [head]: setAt(undefined, rest, newVal) };
}

/** removeAt returns a new tree with the key/index at path removed. */
export function removeAt(node: any, path: string[]): any {
  if (path.length === 0) return node;
  const [head, ...rest] = path;
  if (Array.isArray(node)) {
    const idx = Number(head);
    if (rest.length === 0) {
      const copy = node.slice();
      copy.splice(idx, 1);
      return copy;
    }
    const copy = node.slice();
    copy[idx] = removeAt(copy[idx], rest);
    return copy;
  }
  if (isMap(node)) {
    if (rest.length === 0) {
      const copy = { ...node };
      delete copy[head];
      return copy;
    }
    const copy = { ...node };
    copy[head] = removeAt(copy[head], rest);
    return copy;
  }
  return node;
}

/**
 * computeEdits diffs orig against tree and returns the edits that transform
 * orig into tree: one per changed/added leaf or removed node. A structural
 * type change (scalar to container or vice versa) is emitted as a remove
 * followed by a set.
 */
export function computeEdits(orig: any, tree: any): ConfigEdit[] {
  const out: ConfigEdit[] = [];
  diff(orig, tree, [], out);
  return out;
}

function diff(orig: any, tree: any, path: string[], out: ConfigEdit[]): void {
  const om = isMap(orig), tm = isMap(tree);
  const oa = Array.isArray(orig), ta = Array.isArray(tree);
  if (om && tm) {
    for (const k of new Set([...Object.keys(orig), ...Object.keys(tree)])) {
      const inO = k in orig, inT = k in tree;
      const p = path.concat(k);
      if (inO && !inT) out.push({ path: p, remove: true });
      else if (!inO && inT) out.push({ path: p, value: tree[k] });
      else diff(orig[k], tree[k], p, out);
    }
    return;
  }
  if (oa && ta) {
    for (let i = 0; i < Math.max(orig.length, tree.length); i++) {
      const p = path.concat(String(i));
      if (i < orig.length && i < tree.length) diff(orig[i], tree[i], p, out);
      else if (i < tree.length) out.push({ path: p, value: tree[i] });
      else out.push({ path: p, remove: true });
    }
    return;
  }
  if ((om || oa) !== (tm || ta)) {
    if (path.length > 0) out.push({ path, remove: true });
    out.push({ path, value: tree });
    return;
  }
  if (orig !== tree) out.push({ path, value: tree });
}
