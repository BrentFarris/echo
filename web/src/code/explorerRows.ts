export type ExplorerRow = {
  key: string;
  attributes: Record<string, string>;
  chevronClass: string;
  iconClass: string;
  labelClass: string;
  name: string;
  renaming: boolean;
};

function setAttribute(element: Element, name: string, value: string): void {
  if (element.getAttribute(name) !== value) element.setAttribute(name, value);
}

/** Reconcile the virtual window without replacing unchanged rows or live rename inputs. */
export function renderExplorerRows(canvas: HTMLElement, rows: ExplorerRow[]): HTMLInputElement | undefined {
  const existing = new Map(Array.from(canvas.children, (element) => [(element as HTMLElement).dataset.treeKey!, element as HTMLElement]));
  const keys = new Set(rows.map((row) => row.key));
  for (const [key, element] of existing) if (!keys.has(key)) element.remove();
  let cursor = canvas.firstElementChild;
  let newInput: HTMLInputElement | undefined;
  for (const spec of rows) {
    let row = existing.get(spec.key);
    if (!row) {
      row = document.createElement("div");
      row.append(document.createElement("span"), document.createElement("span"), document.createElement("span"));
    }
    for (const attribute of Array.from(row.attributes)) {
      if (!(attribute.name in spec.attributes)) row.removeAttribute(attribute.name);
    }
    for (const [name, value] of Object.entries(spec.attributes)) setAttribute(row, name, value);
    setAttribute(row.children[0], "class", spec.chevronClass);
    setAttribute(row.children[1], "class", spec.iconClass);
    let label = row.children[2];
    if (spec.renaming) {
      if (!(label instanceof HTMLInputElement)) {
        const input = document.createElement("input");
        input.className = "code-tree-rename";
        input.setAttribute("data-rename-input", "");
        input.value = spec.name;
        label.replaceWith(input);
        label = input;
        newInput = input;
      }
      setAttribute(label, "aria-label", `Rename ${spec.name}`);
    } else {
      if (label instanceof HTMLInputElement) {
        const span = document.createElement("span");
        label.replaceWith(span);
        label = span;
      }
      setAttribute(label, "class", spec.labelClass);
      if (label.textContent !== spec.name) label.textContent = spec.name;
    }
    if (row !== cursor) canvas.insertBefore(row, cursor);
    cursor = row.nextElementSibling;
  }
  return newInput;
}
