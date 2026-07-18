function escapeHtml(value: string): string {
  return value
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&#039;");
}

function escapeAttribute(value: string): string {
  return escapeHtml(value).replaceAll("`", "&#096;");
}

export function renderMarkdown(markdown = ""): string {
  const lines = markdown.replaceAll("\r\n", "\n").split("\n");
  const blocks: string[] = [];
  let paragraph: string[] = [];
  let list: { items: string[]; ordered: boolean; start: number } | null = null;
  let code: string[] | null = null;

  const flushParagraph = () => {
    if (paragraph.length) {
      blocks.push(`<p>${renderInlineMarkdown(paragraph.join(" "))}</p>`);
      paragraph = [];
    }
  };
  const flushList = () => {
    if (list) {
      const tag = list.ordered ? "ol" : "ul";
      const start = list.ordered && list.start !== 1 ? ` start="${list.start}"` : "";
      blocks.push(`<${tag}${start}>${list.items.map((item) => `<li>${renderInlineMarkdown(item)}</li>`).join("")}</${tag}>`);
      list = null;
    }
  };

  for (let index = 0; index < lines.length; index++) {
    const line = lines[index];
    if (line.startsWith("```")) {
      flushParagraph();
      flushList();
      if (code) {
        blocks.push(`<pre><code>${escapeHtml(code.join("\n"))}</code></pre>`);
        code = null;
      } else {
        code = [];
      }
      continue;
    }
    if (code) {
      code.push(line);
      continue;
    }
    if (!line.trim()) {
      flushParagraph();
      flushList();
      continue;
    }
    if (isMarkdownTableStart(lines, index)) {
      flushParagraph();
      flushList();
      const table = collectMarkdownTable(lines, index);
      blocks.push(renderMarkdownTable(table.rows, table.alignments));
      index = table.endIndex;
      continue;
    }
    const heading = line.match(/^(#{1,4})\s+(.+)$/);
    if (heading) {
      flushParagraph();
      flushList();
      const level = Math.min(heading[1].length + 2, 6);
      blocks.push(`<h${level}>${renderInlineMarkdown(heading[2])}</h${level}>`);
      continue;
    }
    const unorderedItem = line.match(/^\s*[-*]\s+(.+)$/);
    const orderedItem = line.match(/^\s*(\d+)\.\s+(.+)$/);
    if (unorderedItem || orderedItem) {
      flushParagraph();
      const ordered = Boolean(orderedItem);
      if (!list || list.ordered !== ordered) {
        flushList();
        list = {
          items: [],
          ordered,
          start: orderedItem ? Number.parseInt(orderedItem[1], 10) : 1,
        };
      }
      list.items.push((orderedItem ?? unorderedItem)![orderedItem ? 2 : 1]);
      continue;
    }
    flushList();
    paragraph.push(line.trim());
  }

  flushParagraph();
  flushList();
  if (code) {
    blocks.push(`<pre><code>${escapeHtml(code.join("\n"))}</code></pre>`);
  }

  return blocks.join("") || `<p class="stream-placeholder">Thinking...</p>`;
}

export function patchMarkdownElement(element: HTMLElement, markdown = "") {
  patchChildrenFromHtml(element, renderMarkdown(markdown));
}

export function patchChildrenFromHtml(element: HTMLElement, html: string) {
  const template = document.createElement("template");
  template.innerHTML = html;
  morphChildren(element, template.content);
}

export function elementFromHtml(html: string): HTMLElement {
  const template = document.createElement("template");
  template.innerHTML = html.trim();
  const element = template.content.firstElementChild;
  if (!(element instanceof HTMLElement)) {
    throw new Error("Expected rendered HTML to contain an element.");
  }
  return element;
}

function morphChildren(target: Node, source: Node) {
  let targetChild = target.firstChild;
  let sourceChild = source.firstChild;
  while (targetChild && sourceChild) {
    const nextTarget = targetChild.nextSibling;
    const nextSource = sourceChild.nextSibling;
    if (
      targetChild.nodeType !== sourceChild.nodeType ||
      targetChild.nodeName !== sourceChild.nodeName
    ) {
      target.replaceChild(sourceChild, targetChild);
    } else if (
      targetChild.nodeType === Node.TEXT_NODE ||
      targetChild.nodeType === Node.COMMENT_NODE
    ) {
      if (targetChild.nodeValue !== sourceChild.nodeValue) {
        targetChild.nodeValue = sourceChild.nodeValue;
      }
    } else if (targetChild.nodeType === Node.ELEMENT_NODE) {
      morphElement(targetChild as Element, sourceChild as Element);
    }
    targetChild = nextTarget;
    sourceChild = nextSource;
  }

  while (targetChild) {
    const nextTarget = targetChild.nextSibling;
    target.removeChild(targetChild);
    targetChild = nextTarget;
  }

  while (sourceChild) {
    const nextSource = sourceChild.nextSibling;
    target.appendChild(sourceChild);
    sourceChild = nextSource;
  }
}

export function morphElement(target: Element, source: Element) {
  if (
    target instanceof HTMLDetailsElement &&
    source instanceof HTMLDetailsElement &&
    target.open
  ) {
    source.setAttribute("open", "");
  }

  for (let index = target.attributes.length - 1; index >= 0; index--) {
    const name = target.attributes[index].name;
    if (!source.hasAttribute(name)) {
      target.removeAttribute(name);
    }
  }

  for (let index = 0; index < source.attributes.length; index++) {
    const attr = source.attributes[index];
    if (target.getAttribute(attr.name) !== attr.value) {
      target.setAttribute(attr.name, attr.value);
    }
  }

  morphChildren(target, source);
}

function isMarkdownTableStart(lines: string[], index: number): boolean {
  if (index + 1 >= lines.length) {
    return false;
  }
  const header = splitMarkdownTableRow(lines[index]);
  const separator = splitMarkdownTableRow(lines[index + 1]);
  return header.length > 1 && isMarkdownTableSeparator(separator);
}

function collectMarkdownTable(lines: string[], startIndex: number): { rows: string[][]; alignments: string[]; endIndex: number } {
  const separator = splitMarkdownTableRow(lines[startIndex + 1]);
  const rows = [splitMarkdownTableRow(lines[startIndex])];
  let endIndex = startIndex + 1;

  for (let index = startIndex + 2; index < lines.length; index++) {
    if (!lines[index].trim()) {
      break;
    }
    const row = splitMarkdownTableRow(lines[index]);
    if (row.length < 2) {
      break;
    }
    rows.push(row);
    endIndex = index;
  }

  return {
    rows,
    alignments: separator.map(tableColumnAlignment),
    endIndex,
  };
}

function splitMarkdownTableRow(row: string): string[] {
  const cells: string[] = [];
  let cell = "";
  let inCode = false;
  for (let index = 0; index < row.length; index++) {
    const character = row[index];
    const previous = index > 0 ? row[index - 1] : "";
    if (character === "`" && previous !== "\\") {
      inCode = !inCode;
    }
    if (character === "|" && !inCode && previous !== "\\") {
      cells.push(cell.trim());
      cell = "";
      continue;
    }
    cell += character;
  }
  cells.push(cell.trim());

  if (cells[0] === "") {
    cells.shift();
  }
  if (cells[cells.length - 1] === "") {
    cells.pop();
  }
  return cells.map((value) => value.replaceAll("\\|", "|"));
}

function isMarkdownTableSeparator(cells: string[]): boolean {
  return cells.length > 1 && cells.every((cell) => /^:?-{3,}:?$/.test(cell.trim()));
}

function tableColumnAlignment(separator: string): string {
  const value = separator.trim();
  if (value.startsWith(":") && value.endsWith(":")) {
    return "center";
  }
  if (value.endsWith(":")) {
    return "right";
  }
  return "left";
}

function renderMarkdownTable(rows: string[][], alignments: string[]): string {
  const columnCount = Math.max(...rows.map((row) => row.length));
  const renderCell = (tag: "th" | "td", value: string, index: number) => {
    const alignment = alignments[index] ?? "left";
    return `<${tag} style="text-align: ${alignment}">${renderInlineMarkdown(value ?? "")}</${tag}>`;
  };
  const header = rows[0] ?? [];
  const body = rows.slice(1);
  return `
    <div class="markdown-table-scroll">
      <table>
        <thead>
          <tr>${Array.from({ length: columnCount }, (_unused, index) => renderCell("th", header[index] ?? "", index)).join("")}</tr>
        </thead>
        <tbody>
          ${body
            .map((row) => `<tr>${Array.from({ length: columnCount }, (_unused, index) => renderCell("td", row[index] ?? "", index)).join("")}</tr>`)
            .join("")}
        </tbody>
      </table>
    </div>
  `;
}

function renderInlineMarkdown(value: string): string {
  let html = escapeHtml(value);
  // Render @task:<id> references as styled chips (before other inline replacements)
  html = html.replace(/@task:([A-Za-z0-9_-]+)/g, (_match, taskID) => {
    const escapedId = escapeAttribute(taskID);
    return `<span class="chat-task-ref" data-task-ref="${escapedId}" data-task-id="${escapedId}">@task:${taskID}</span>`;
  });
  html = html.replace(/`([^`]+)`/g, "<code>$1</code>");
  html = html.replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>");
  html = html.replace(/\*([^*]+)\*/g, "<em>$1</em>");
  html = html.replace(
    /\[([^\]]+)\]\((https?:\/\/[^)\s]+)\)/g,
    (_match, text, url) =>
      `<a href="${escapeAttribute(url)}" target="_blank" rel="noreferrer">${text}</a>`,
  );
  return html;
}
