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

export type MarkdownMediaKind = "image" | "audio" | "video";

export type MarkdownRenderOptions = {
  enableMedia?: boolean;
  emptyPlaceholder?: string;
  headingOffset?: number;
  resolveMediaURL?: (source: string, kind: MarkdownMediaKind) => string;
};

export function renderMarkdown(
  markdown = "",
  options: MarkdownRenderOptions = {},
): string {
  const lines = markdown.replaceAll("\r\n", "\n").split("\n");
  const blocks: string[] = [];
  let paragraph: string[] = [];
  let list: { items: string[]; ordered: boolean; start: number } | null = null;
  let code: string[] | null = null;

  const flushParagraph = () => {
    if (paragraph.length) {
      blocks.push(`<p>${renderInlineMarkdown(paragraph.join(" "), options)}</p>`);
      paragraph = [];
    }
  };
  const flushList = () => {
    if (list) {
      const tag = list.ordered ? "ol" : "ul";
      const start = list.ordered && list.start !== 1 ? ` start="${list.start}"` : "";
      blocks.push(`<${tag}${start}>${list.items.map((item) => `<li>${renderInlineMarkdown(item, options)}</li>`).join("")}</${tag}>`);
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
      blocks.push(renderMarkdownTable(table.rows, table.alignments, options));
      index = table.endIndex;
      continue;
    }
    const heading = line.match(/^(#{1,6})\s+(.+)$/);
    if (heading) {
      flushParagraph();
      flushList();
      const offset = options.headingOffset ?? 2;
      const level = Math.min(Math.max(heading[1].length + offset, 1), 6);
      blocks.push(`<h${level}>${renderInlineMarkdown(heading[2], options)}</h${level}>`);
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

  return blocks.join("") || `<p class="stream-placeholder">${escapeHtml(options.emptyPlaceholder ?? "Thinking...")}</p>`;
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

function renderMarkdownTable(
  rows: string[][],
  alignments: string[],
  options: MarkdownRenderOptions,
): string {
  const columnCount = Math.max(...rows.map((row) => row.length));
  const renderCell = (tag: "th" | "td", value: string, index: number) => {
    const alignment = alignments[index] ?? "left";
    return `<${tag} style="text-align: ${alignment}">${renderInlineMarkdown(value ?? "", options)}</${tag}>`;
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

function renderInlineMarkdown(
  value: string,
  options: MarkdownRenderOptions,
): string {
  const tokens: string[] = [];
  const storeToken = (html: string) => {
    const index = tokens.push(html) - 1;
    return `\uE000${index}\uE001`;
  };
  let prepared = value;

  if (options.enableMedia) {
    prepared = replaceRawMediaHTML(prepared, options, storeToken);
    prepared = prepared.replace(
      /!\[([^\]]*)\]\(\s*((?:<[^>]+>)|(?:[^)\s]+))(?:\s+["']([^"']*)["'])?\s*\)/g,
      (_match, alt, source, title) => {
        const destination = markdownDestination(source);
        const kind = markdownMediaKindForSource(destination) ?? "image";
        return storeToken(
          renderMarkdownMedia(kind, destination, alt, title, options),
        );
      },
    );
  }

  prepared = prepared.replace(
    /\[([^\]]+)\]\(\s*((?:<[^>]+>)|(?:[^)\s]+))(?:\s+["']([^"']*)["'])?\s*\)/g,
    (match, label, source, title) => {
      const destination = markdownDestination(source);
      const mediaKind = options.enableMedia
        ? markdownMediaKindForSource(destination)
        : null;
      if (mediaKind) {
        return storeToken(
          renderMarkdownMedia(mediaKind, destination, label, title, options),
        );
      }
      if (!isSafeMarkdownLink(destination)) {
        return match;
      }
      return storeToken(
        `<a href="${escapeAttribute(destination)}" target="_blank" rel="noreferrer">${renderInlineText(label)}</a>`,
      );
    },
  );

  prepared = prepared.replace(
    /`([^`]+)`/g,
    (_match, code) => storeToken(`<code>${escapeHtml(code)}</code>`),
  );

  let html = escapeHtml(prepared);
  // Render @task:<id> references as styled chips (before other inline replacements)
  html = html.replace(/@task:([A-Za-z0-9_-]+)/g, (_match, taskID) => {
    const escapedId = escapeAttribute(taskID);
    return `<span class="chat-task-ref" data-task-ref="${escapedId}" data-task-id="${escapedId}">@task:${taskID}</span>`;
  });
  html = html.replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>");
  html = html.replace(/\*([^*]+)\*/g, "<em>$1</em>");
  html = html.replace(/\uE000(\d+)\uE001/g, (_match, index) => tokens[Number(index)] ?? "");
  return html;
}

function renderInlineText(value: string): string {
  return escapeHtml(value)
    .replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>")
    .replace(/\*([^*]+)\*/g, "<em>$1</em>");
}

function replaceRawMediaHTML(
  value: string,
  options: MarkdownRenderOptions,
  storeToken: (html: string) => string,
): string {
  let output = value.replace(
    /<img\b([^>]*)\/?>/gi,
    (match, attributes) => {
      const source = mediaHTMLAttribute(attributes, "src");
      if (!source) {
        return match;
      }
      return storeToken(
        renderMarkdownMedia(
          "image",
          source,
          mediaHTMLAttribute(attributes, "alt"),
          mediaHTMLAttribute(attributes, "title"),
          options,
        ),
      );
    },
  );
  output = output.replace(
    /<(audio|video)\b([^>]*)>([\s\S]*?)<\/\1>/gi,
    (match, tag, attributes, content) => {
      const source =
        mediaHTMLAttribute(attributes, "src") ||
        mediaHTMLAttributeFromSourceTag(content);
      if (!source) {
        return match;
      }
      const kind = tag.toLowerCase() as MarkdownMediaKind;
      return storeToken(
        renderMarkdownMedia(
          kind,
          source,
          mediaHTMLAttribute(attributes, "aria-label"),
          mediaHTMLAttribute(attributes, "title"),
          options,
        ),
      );
    },
  );
  return output;
}

function mediaHTMLAttribute(attributes: string, name: string): string {
  const quoted = attributes.match(
    new RegExp(`(?:^|\\s)${name}\\s*=\\s*(["'])(.*?)\\1`, "i"),
  );
  if (quoted) {
    return quoted[2];
  }
  const unquoted = attributes.match(
    new RegExp(`(?:^|\\s)${name}\\s*=\\s*([^\\s>]+)`, "i"),
  );
  return unquoted?.[1] ?? "";
}

function mediaHTMLAttributeFromSourceTag(content: string): string {
  const source = content.match(/<source\b([^>]*)\/?>/i);
  return source ? mediaHTMLAttribute(source[1], "src") : "";
}

function markdownDestination(value: string): string {
  const trimmed = value.trim();
  if (trimmed.startsWith("<") && trimmed.endsWith(">")) {
    return trimmed.slice(1, -1).trim();
  }
  return trimmed;
}

function isSafeMarkdownLink(value: string): boolean {
  return /^https?:\/\//i.test(value);
}

function isDirectMarkdownMediaURL(value: string): boolean {
  return (
    /^https?:\/\//i.test(value) ||
    /^blob:/i.test(value) ||
    /^data:(?:image|audio|video)\//i.test(value)
  );
}

function markdownMediaKindForSource(
  source: string,
): MarkdownMediaKind | null {
  const path = source.split(/[?#]/, 1)[0].toLowerCase();
  const extension = path.includes(".") ? path.slice(path.lastIndexOf(".") + 1) : "";
  if (["apng", "avif", "bmp", "gif", "ico", "jpeg", "jpg", "png", "svg", "tif", "tiff", "webp"].includes(extension)) {
    return "image";
  }
  if (["aac", "flac", "m4a", "mp3", "oga", "ogg", "opus", "wav", "weba", "wma"].includes(extension)) {
    return "audio";
  }
  if (["avi", "flv", "mkv", "mov", "mp4", "ogv", "webm", "wmv"].includes(extension)) {
    return "video";
  }
  if (/^data:image\//i.test(source)) {
    return "image";
  }
  if (/^data:audio\//i.test(source)) {
    return "audio";
  }
  if (/^data:video\//i.test(source)) {
    return "video";
  }
  return null;
}

function renderMarkdownMedia(
  kind: MarkdownMediaKind,
  source: string,
  label: string,
  title: string,
  options: MarkdownRenderOptions,
): string {
  const directURL = isDirectMarkdownMediaURL(source)
    ? source
    : options.resolveMediaURL?.(source, kind) ?? "";
  const sourceAttribute = directURL
    ? ` src="${escapeAttribute(directURL)}"`
    : "";
  const pendingAttribute = isDirectMarkdownMediaURL(source)
    ? ""
    : ` data-markdown-media-source="${escapeAttribute(source)}"`;
  const hiddenAttribute = directURL ? "" : " hidden";
  const titleAttribute = title
    ? ` title="${escapeAttribute(title)}"`
    : "";
  const accessibleLabel = label || title || `${kind} preview`;
  const media = kind === "image"
    ? `<img${sourceAttribute}${titleAttribute} alt="${escapeAttribute(accessibleLabel)}" loading="lazy" decoding="async"${hiddenAttribute} data-markdown-media-target>`
    : `<${kind}${sourceAttribute}${titleAttribute} aria-label="${escapeAttribute(accessibleLabel)}" controls preload="metadata"${hiddenAttribute} data-markdown-media-target></${kind}>`;
  const status = directURL
    ? ""
    : `<span class="markdown-media-status" data-markdown-media-status>Loading ${kind}...</span>`;
  const caption = label
    ? `<span class="markdown-media-caption">${renderInlineText(label)}</span>`
    : "";
  return `<span class="markdown-media markdown-media-${kind}"${pendingAttribute} data-markdown-media-kind="${kind}">${status}${media}${caption}</span>`;
}
