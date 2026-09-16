export const INDEX_KEY = "notes.index.v1";
export const INDEX_VERSION = 1;
export const NOTE_KEY_PREFIX = "note.";
export const AUTOSAVE_DELAY_MS = 500;
export const MAX_TITLE_LENGTH = 120;
export const MAX_SLUG_LENGTH = 64;
export const MAX_NOTE_BYTES = 60 * 1024;

const NOTE_ID_PATTERN = /^[A-Za-z0-9-]{1,80}$/;
const SLUG_PATTERN = /^[a-z0-9]+(?:-[a-z0-9]+)*$/;

export function noteStorageKey(id) {
  if (!NOTE_ID_PATTERN.test(id)) throw new Error("The note identifier is invalid.");
  return NOTE_KEY_PREFIX + id;
}

export function noteBodyBytes(content) {
  return new TextEncoder().encode(String(content)).byteLength;
}

export function validateNoteBody(content) {
  if (typeof content !== "string") return { ok: false, error: "Stored note content is invalid." };
  const bytes = noteBodyBytes(content);
  if (bytes > MAX_NOTE_BYTES) {
    return { ok: false, error: `This note is ${formatBytes(bytes)}. Notes are limited to 60 KiB.` };
  }
  return { ok: true, bytes };
}

export function normalizeIndex(value) {
  if (!value || typeof value !== "object" || Array.isArray(value) || value.version !== INDEX_VERSION || !Array.isArray(value.notes)) {
    return { ok: false, error: "Stored note metadata is invalid. Echo left it unchanged." };
  }
  const ids = new Set();
  const slugs = new Set();
  const notes = [];
  for (const candidate of value.notes) {
    if (!candidate || typeof candidate !== "object" || Array.isArray(candidate)) {
      return { ok: false, error: "Stored note metadata contains an invalid entry. Echo left it unchanged." };
    }
    const { id, title, slug, createdAt, updatedAt } = candidate;
    if (!NOTE_ID_PATTERN.test(id) || typeof title !== "string" || !title.trim() || title.length > MAX_TITLE_LENGTH ||
        typeof slug !== "string" || slug.length > MAX_SLUG_LENGTH || !SLUG_PATTERN.test(slug) ||
        !validTimestamp(createdAt) || !validTimestamp(updatedAt) || ids.has(id) || slugs.has(slug)) {
      return { ok: false, error: "Stored note metadata contains an invalid or duplicate entry. Echo left it unchanged." };
    }
    ids.add(id);
    slugs.add(slug);
    notes.push({ id, title, slug, createdAt, updatedAt });
  }
  return { ok: true, index: { version: INDEX_VERSION, notes: sortNotes(notes) } };
}

export function sortNotes(notes) {
  return [...notes].sort((left, right) => {
    const byTime = Date.parse(right.updatedAt) - Date.parse(left.updatedAt);
    return byTime || left.title.localeCompare(right.title);
  });
}

export function slugForTitle(title, notes = [], currentID = "") {
  let base = String(title || "")
    .normalize("NFKD")
    .replace(/[\u0300-\u036f]/g, "")
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "") || "untitled-note";
  base = base.slice(0, MAX_SLUG_LENGTH).replace(/-+$/g, "") || "untitled-note";
  const occupied = new Set(notes.filter(note => note.id !== currentID).map(note => note.slug));
  if (!occupied.has(base)) return base;
  for (let number = 2; number < 10000; number++) {
    const suffix = `-${number}`;
    const candidate = base.slice(0, MAX_SLUG_LENGTH - suffix.length).replace(/-+$/g, "") + suffix;
    if (!occupied.has(candidate)) return candidate;
  }
  throw new Error("A unique note name could not be created.");
}

export function createNoteRecord(notes, id, timestamp = new Date().toISOString()) {
  const title = "Untitled note";
  return {
    metadata: {
      id,
      title,
      slug: slugForTitle(title, notes),
      createdAt: timestamp,
      updatedAt: timestamp,
    },
    content: `# ${title}\n\n`,
  };
}

export function formatModified(timestamp, now = Date.now()) {
  const elapsed = Math.max(0, now - Date.parse(timestamp));
  if (elapsed < 60_000) return "Just now";
  if (elapsed < 3_600_000) return `${Math.floor(elapsed / 60_000)}m ago`;
  if (elapsed < 86_400_000) return `${Math.floor(elapsed / 3_600_000)}h ago`;
  if (elapsed < 604_800_000) return `${Math.floor(elapsed / 86_400_000)}d ago`;
  return new Date(timestamp).toLocaleDateString(undefined, { month: "short", day: "numeric", year: new Date(timestamp).getFullYear() === new Date(now).getFullYear() ? undefined : "numeric" });
}

export function renderMarkdown(container, markdown) {
  container.replaceChildren();
  const lines = String(markdown || "").replace(/\r\n?/g, "\n").split("\n");
  let index = 0;
  while (index < lines.length) {
    const line = lines[index];
    if (!line.trim()) { index++; continue; }

    const fence = line.match(/^\s*```\s*([^\s`]*)\s*$/);
    if (fence) {
      const codeLines = [];
      index++;
      while (index < lines.length && !/^\s*```\s*$/.test(lines[index])) codeLines.push(lines[index++]);
      if (index < lines.length) index++;
      const pre = document.createElement("pre");
      const code = document.createElement("code");
      if (fence[1]) code.dataset.language = fence[1].slice(0, 40);
      code.textContent = codeLines.join("\n");
      pre.append(code);
      container.append(pre);
      continue;
    }

    const heading = line.match(/^(#{1,6})\s+(.+)$/);
    if (heading) {
      const element = document.createElement(`h${heading[1].length}`);
      appendInline(element, heading[2]);
      container.append(element);
      index++;
      continue;
    }

    if (/^\s*>\s?/.test(line)) {
      const quoteLines = [];
      while (index < lines.length && /^\s*>\s?/.test(lines[index])) quoteLines.push(lines[index++].replace(/^\s*>\s?/, ""));
      const quote = document.createElement("blockquote");
      appendInline(quote, quoteLines.join(" "));
      container.append(quote);
      continue;
    }

    const listMatch = line.match(/^\s*(?:(\d+)\.|([-+*]))\s+(.+)$/);
    if (listMatch) {
      const ordered = Boolean(listMatch[1]);
      const list = document.createElement(ordered ? "ol" : "ul");
      while (index < lines.length) {
        const itemMatch = lines[index].match(/^\s*(?:(\d+)\.|([-+*]))\s+(.+)$/);
        if (!itemMatch || Boolean(itemMatch[1]) !== ordered) break;
        const item = document.createElement("li");
        appendInline(item, itemMatch[3]);
        list.append(item);
        index++;
      }
      container.append(list);
      continue;
    }

    const paragraphLines = [];
    while (index < lines.length && lines[index].trim() && !isBlockStart(lines[index])) paragraphLines.push(lines[index++].trim());
    if (!paragraphLines.length) paragraphLines.push(lines[index++]);
    const paragraph = document.createElement("p");
    appendInline(paragraph, paragraphLines.join(" "));
    container.append(paragraph);
  }
}

function appendInline(parent, source) {
  const pattern = /(`[^`\n]+`|\*\*[^*\n]+\*\*|__[^_\n]+__|\*[^*\n]+\*|_[^_\n]+_|\[[^\]\n]+\]\([^)\n]+\))/g;
  let offset = 0;
  for (const match of source.matchAll(pattern)) {
    if (match.index > offset) parent.append(document.createTextNode(source.slice(offset, match.index)));
    const token = match[0];
    let element;
    if (token.startsWith("`")) {
      element = document.createElement("code");
      element.textContent = token.slice(1, -1);
    } else if (token.startsWith("**") || token.startsWith("__")) {
      element = document.createElement("strong");
      element.textContent = token.slice(2, -2);
    } else if (token.startsWith("*") || token.startsWith("_")) {
      element = document.createElement("em");
      element.textContent = token.slice(1, -1);
    } else {
      const link = token.match(/^\[([^\]]+)\]\(([^)]+)\)$/);
      element = document.createElement("span");
      element.className = "markdown-link";
      element.textContent = link ? link[1] : token;
      if (link) element.title = link[2].trim().slice(0, 512);
    }
    parent.append(element);
    offset = match.index + token.length;
  }
  if (offset < source.length) parent.append(document.createTextNode(source.slice(offset)));
}

function isBlockStart(line) {
  return /^\s*```/.test(line) || /^(#{1,6})\s+/.test(line) || /^\s*>\s?/.test(line) || /^\s*(?:(\d+)\.|[-+*])\s+/.test(line);
}

function validTimestamp(value) {
  return typeof value === "string" && value.length <= 40 && Number.isFinite(Date.parse(value));
}

function formatBytes(bytes) {
  return `${(bytes / 1024).toFixed(bytes < 10 * 1024 ? 1 : 0)} KiB`;
}
