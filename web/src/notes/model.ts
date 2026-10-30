import { marked, type Token } from 'marked';
import DOMPurify from 'dompurify';
import type { Bot } from '../types';
import { entryNode, localID, machineName } from '../instances';

export type Mention = { nodeId: string; botId: string; label: string; start: number; end: number };
export type Note = { id: string; title: string; markdown: string; mentions: Mention[]; revision: number; createdAt: string; updatedAt: string };
export type OwnedNote = Note & { nodeId: string };
export type Draft = { note: OwnedNote; baseRevision: number; dirty: boolean; conflict?: boolean };
export type Marker = { text: string; start: number; end: number };
export const noteKey = (note: Pick<OwnedNote, 'id' | 'nodeId'>) => `${note.nodeId}:${note.id}`;
export const botKey = (mention: Pick<Mention, 'nodeId' | 'botId'>) => `${mention.nodeId}:${mention.botId}`;

export function sameContent(a: Note, b: Note): boolean {
  return (a.title.trim() || 'Untitled note') === (b.title.trim() || 'Untitled note') && a.markdown === b.markdown && JSON.stringify(a.mentions) === JSON.stringify(b.mentions);
}

// Walk only Markdown prose tokens. Raw token locations retain UTF-16 offsets
// into the source; destinations, escaped markers, code and HTML are excluded.
export function markers(markdown: string): Marker[] {
  const found: Marker[] = [];
  function locate(raw: string, source: string, cursor: number): { end: number; offset(index: number): number } | undefined {
    const at = source.indexOf(raw, cursor);
    if (at >= 0) return { end: at + raw.length, offset: index => at + index };
    // Marked removes quote/list prefixes and normalizes CRLF in child tokens.
    // Map each line back to the parent's raw text instead of losing its ranges.
    const lines = raw.split('\n'), spans: { raw: number; source: number }[] = [];
    let rawOffset = 0, position = cursor;
    for (let i = 0; i < lines.length; i++) {
      const line = lines[i], index = source.indexOf(line, position);
      if (index < 0 || (i > 0 && !/^[\t >]*$/.test(source.slice(position, index)))) return;
      spans.push({ raw: rawOffset, source: index });
      position = index + line.length;
      if (i < lines.length - 1) {
        if (source[position] === '\r') position++;
        if (source[position] !== '\n') return;
        position++; rawOffset += line.length + 1;
      }
    }
    return { end: position, offset: index => { const span = spans.findLast(span => span.raw <= index)!; return span.source + index - span.raw; } };
  }
  function walk(tokens: Token[], source: string, absolute: (index: number) => number) {
    let cursor = 0, htmlDepth = 0;
    for (const token of tokens) {
      const location = locate(token.raw, source, cursor);
      if (!location) {
        // Tight-list text wrappers can contain synthetic blank lines. Their
        // inline children still map to the original parent source.
        if ('tokens' in token && token.tokens) {
          const start = cursor;
          cursor += walk(token.tokens, source.slice(start), index => absolute(start + index));
        }
        continue;
      }
      cursor = location.end;
      const absoluteToken = (index: number) => absolute(location.offset(index));
      if (token.type === 'html') {
        if (/^<\//.test(token.raw)) htmlDepth = Math.max(0, htmlDepth - 1);
        else if (/^<[a-z][^>]*>$/i.test(token.raw) && !/\/$|\/\s*>$/.test(token.raw) && !/^<(?:br|hr|img|input|wbr)\b/i.test(token.raw)) htmlDepth++;
        continue;
      }
      if (htmlDepth || ['code', 'codespan', 'escape', 'image'].includes(token.type)) continue;
      if (token.type === 'link' && (token.raw.startsWith('<') || /^https?:\/\//i.test(token.raw))) continue;
      if ('items' in token && Array.isArray(token.items)) { walk(token.items, token.raw, absoluteToken); continue; }
      if ('tokens' in token && token.tokens) { walk(token.tokens, token.raw, absoluteToken); continue; }
      if (token.type === 'table') {
        // Cells carry tokens but no source position; locate each cell in order.
        let position = 0;
        for (const cell of [...token.header, ...token.rows.flat()]) {
          const index = token.raw.indexOf(cell.text, position);
          if (index >= 0) { walk(cell.tokens, cell.text, offset => absoluteToken(index + offset)); position = index + cell.text.length; }
        }
        continue;
      }
      if (token.type !== 'text') continue;
      const pattern = /(?<![\p{L}\p{N}_@#\\])(?:#[\p{L}\p{N}_-]+|@[A-Za-z][A-Za-z0-9_-]*(?:\.[A-Za-z][A-Za-z0-9_-]*)*)/gu;
      for (const match of token.raw.matchAll(pattern)) found.push({ text: match[0], start: absoluteToken(match.index!), end: absoluteToken(match.index! + match[0].length) });
    }
    return cursor;
  }
  walk(marked.lexer(markdown), markdown, index => index);
  return found.sort((a, b) => a.start - b.start).filter((m, i, all) => !i || m.start !== all[i - 1].start);
}

export function hashtags(markdown: string): string[] {
  return [...new Set(markers(markdown).filter(m => m.text.startsWith('#')).map(m => m.text.slice(1).toLocaleLowerCase()))].sort();
}

export function mentionHandle(bot: Bot, home: string): string {
  const node = bot.nodeId || entryNode;
  const handle = bot.handle.split('.').at(-1)!;
  return '@' + (node === home ? handle : `${machineName(node)}.${handle}`);
}

export function bindMentions(markdown: string, home: string, bots: Bot[], previous = '', bindings: Mention[] = []): Mention[] {
  let prefix = 0, suffix = 0;
  while (prefix < previous.length && prefix < markdown.length && previous[prefix] === markdown[prefix]) prefix++;
  while (suffix < previous.length - prefix && suffix < markdown.length - prefix && previous[previous.length - 1 - suffix] === markdown[markdown.length - 1 - suffix]) suffix++;
  const retained = bindings.flatMap(m => {
    if (m.end <= prefix) return [m];
    if (m.start >= previous.length - suffix) { const delta = markdown.length - previous.length; return [{ ...m, start: m.start + delta, end: m.end + delta }]; }
    return [];
  });
  return markers(markdown).filter(m => m.text.startsWith('@')).flatMap(marker => {
    const old = retained.find(m => m.start === marker.start && m.end === marker.end && m.label === marker.text);
    if (old) return [old];
    const bot = bots.find(b => mentionHandle(b, home).toLowerCase() === marker.text.toLowerCase() || `@${machineName(b.nodeId || entryNode)}.${b.handle.split('.').at(-1)}`.toLowerCase() === marker.text.toLowerCase());
    return bot ? [{ nodeId: bot.nodeId || entryNode, botId: localID(bot.id), label: marker.text, start: marker.start, end: marker.end }] : [];
  });
}

export function renderNote(markdown: string): DocumentFragment {
  const fragment = DOMPurify.sanitize(marked.parse(markdown, { async: false, gfm: true }), {
    RETURN_DOM_FRAGMENT: true,
    ALLOWED_TAGS: ['p','br','hr','strong','em','b','i','del','s','blockquote','pre','code','ul','ol','li','h1','h2','h3','h4','h5','h6','table','thead','tbody','tr','th','td','a','img','input','details','summary','kbd','sub','sup'],
    ALLOWED_ATTR: ['href','src','alt','title','type','checked','disabled','start','align'],
    ALLOW_DATA_ATTR: false, ALLOW_ARIA_ATTR: false,
  });
  for (const link of fragment.querySelectorAll('a')) {
    const href = link.getAttribute('href') || '';
    link.removeAttribute('href');
    if (/^https?:\/\//i.test(href)) { link.href = href; link.target = '_blank'; link.rel = 'noopener noreferrer'; }
  }
  for (const img of fragment.querySelectorAll('img')) {
    if (!/^https?:\/\//i.test(img.getAttribute('src') || '')) { img.replaceWith(document.createTextNode(img.alt)); continue; }
    img.loading = 'lazy'; img.referrerPolicy = 'no-referrer';
  }
  for (const input of fragment.querySelectorAll('input')) {
    if (input.type === 'checkbox') input.disabled = true; else input.remove();
  }
  return fragment;
}
