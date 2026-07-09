/**
 * CodeMirror spell-check ViewPlugin.
 *
 * Traverses the Lezer syntax tree on each visible viewport to find misspelled
 * words in comments, strings, and identifiers.  Uses @codemirror/lint for
 * diagnostic decoration rendering and exposes diagnostics through a state field
 * so that context-menu code can query the current misspelling at a position.
 */

import { RangeSetBuilder, StateEffect, StateField, type Extension } from "@codemirror/state";
import { Decoration, type DecorationSet, type EditorView, ViewPlugin, type ViewUpdate } from "@codemirror/view";
import { syntaxTree } from "@codemirror/language";
import type { SyntaxNodeRef } from "@lezer/common";
import { checkWord, splitIdentifier } from "./dictionary";
import { ensureCodeState } from "./state";

// ─── Configuration ──────────────────────────────────────────────

/** Debounce interval (ms) between re-checks after a doc or viewport change. */
const SPELL_CHECK_DEBOUNCE_MS = 350;

/** Lezer node-type names treated as spell-checkable comments. */
const COMMENT_TYPES = new Set([
  "Comment",
  "LineComment",
  "BlockComment",
  "DocComment",
  "Shebang",
  "HTMLComment",
  "XMLComment",
]);

/** Lezer node-type names treated as spell-checkable strings. */
const STRING_TYPES = new Set([
  "String",
  "StringLiteral",
  "StringExpression",
  "TemplateElement",
  "Text",
  "Regex",
  "CharLiteral",
  "Character",
]);

/** Lezer node-type names treated as spell-checkable identifiers. */
const IDENTIFIER_TYPES = new Set([
  "Name",
  "PropertyName",
  "VariableName",
  "TypeName",
  "LabelName",
  "FieldName",
  "AttributeName",
]);

// ─── Public types ───────────────────────────────────────────────

/** Describes a single misspelled word and its document range. */
export type MisspellingInfo = {
  /** The raw misspelled token (lowercase for dictionary comparison). */
  word: string;
  /** Inclusive start offset in the document. */
  from: number;
  /** Exclusive end offset in the document. */
  to: number;
};

// ─── State field for external queries ───────────────────────────

const updateSpellDiagnosticsEffect = StateEffect.define<MisspellingInfo[]>();

/**
 * State field that holds the current list of misspellings.
 * Updated by the spell-check ViewPlugin after each debounced scan.
 */
export const spellCheckField = StateField.define<MisspellingInfo[]>({
  create() {
    return [];
  },
  update(value, transaction) {
    for (const effect of transaction.effects) {
      if (effect.is(updateSpellDiagnosticsEffect)) {
        return effect.value;
      }
    }
    return value;
  },
});

// ─── Decoration ─────────────────────────────────────────────────

const spellErrorDecoration = Decoration.mark({ class: "cm-spell-error" });

// ─── Word-finding helpers ───────────────────────────────────────

/**
 * Find whitespace-separated letter-words in `text` that fail the dictionary
 * check.  Returns MisspellingInfo entries with positions relative to
 * `baseOffset`.
 */
function findMisspelledWhitespaceWords(
  text: string,
  baseOffset: number,
  ignoreList: Set<string>,
): MisspellingInfo[] {
  const results: MisspellingInfo[] = [];
  let wordStart = -1;

  for (let i = 0; i <= text.length; i++) {
    const ch = i < text.length ? text[i] : " ";
    if (/^[a-zA-Z]$/.test(ch)) {
      if (wordStart < 0) {
        wordStart = i;
      }
    } else {
      if (wordStart >= 0) {
        const word = text.slice(wordStart, i);
        if (!checkWord(word, ignoreList)) {
          results.push({
            word: word.toLowerCase(),
            from: baseOffset + wordStart,
            to: baseOffset + i,
          });
        }
        wordStart = -1;
      }
    }
  }

  return results;
}

/**
 * Split `identifier` into camelCase / snake_case sub-words and check each
 * against the dictionary.  Returns MisspellingInfo entries with positions
 * relative to `baseOffset`.
 */
function findMisspelledIdentifierSubWords(
  identifier: string,
  baseOffset: number,
  ignoreList: Set<string>,
): MisspellingInfo[] {
  const results: MisspellingInfo[] = [];
  const subWords = splitIdentifier(identifier);

  let searchFrom = 0;
  const lower = identifier.toLowerCase();
  for (const subWord of subWords) {
    if (checkWord(subWord, ignoreList)) continue;
    const idx = lower.indexOf(subWord, searchFrom);
    if (idx >= 0) {
      results.push({
        word: subWord,
        from: baseOffset + idx,
        to: baseOffset + idx + subWord.length,
      });
      searchFrom = idx + 1;
    }
  }

  return results;
}

// ─── Text cleaning helpers ──────────────────────────────────────

/** Strip common string delimiters so inner content can be spell-checked. */
function stripStringDelimiters(raw: string): string {
  let s = raw;
  // Remove surrounding quotes / backticks (handle triple-quote languages)
  for (const q of ['"""', "'''", '```', '"', "'", "`"]) {
    if (s.startsWith(q) && s.endsWith(q) && s.length > q.length * 2) {
      s = s.slice(q.length, -q.length);
      break;
    }
  }
  // Replace common escape sequences with spaces to avoid false positives
  return s.replace(/\\[\\'\"nrtbfux0-9]/g, " ");
}

/** Strip common comment markers so inner content can be spell-checked. */
function stripCommentMarkers(raw: string): string {
  let s = raw;
  // Line-comment prefixes
  if (s.startsWith("//")) s = s.slice(2);
  else if (s.startsWith("##")) s = s.slice(2);
  else if (s.startsWith("#!")) s = s.slice(2);
  else if (s.startsWith("#")) s = s.slice(1);
  // Block-comment delimiters
  if (s.startsWith("/*")) s = s.slice(2);
  if (s.endsWith("*/")) s = s.slice(0, -2);
  // HTML / XML comments
  if (s.startsWith("<!--")) s = s.slice(4);
  if (s.endsWith("-->")) s = s.slice(0, -3);
  return s;
}

// ─── ViewPlugin class ───────────────────────────────────────────

class SpellCheckPlugin {
  decorations: DecorationSet;
  private timerId: ReturnType<typeof setTimeout> | null = null;
  private workspaceID: string;

  constructor(view: EditorView, workspaceID: string) {
    this.workspaceID = workspaceID;
    this.decorations = Decoration.none;
    this.scheduleCheck(view);
  }

  update(update: ViewUpdate) {
    if (update.docChanged || update.viewportChanged) {
      this.scheduleCheck(update.view);
    }
  }

  // ── Debounced scheduling ───────────────────────────────────

  private scheduleCheck(view: EditorView) {
    if (this.timerId !== null) {
      window.clearTimeout(this.timerId);
    }
    this.timerId = window.setTimeout(() => {
      this.timerId = null;
      this.runCheck(view);
    }, SPELL_CHECK_DEBOUNCE_MS);
  }

  // ── Core check ─────────────────────────────────────────────

  private runCheck(view: EditorView) {
    const ignoreList = ensureCodeState(this.workspaceID).spellCheckIgnoreList;
    const misspellings: MisspellingInfo[] = [];
    const builder = new DecorationBuilder();

    const tree = syntaxTree(view.state);

    for (const { from, to } of view.visibleRanges) {
      let lastProcessedTo = from;

      tree.iterate({
        from,
        to,
        enter: (node: SyntaxNodeRef) => {
          // Skip children of an already-processed parent node.
          if (node.to <= lastProcessedTo) return;

          const typeName = node.type.name;

          if (COMMENT_TYPES.has(typeName)) {
            const cleaned = stripCommentMarkers(
              view.state.doc.sliceString(node.from, node.to),
            );
            const hits = findMisspelledWhitespaceWords(cleaned, node.from, ignoreList);
            misspellings.push(...hits);
            builder.addRange(node.from, node.to, hits);
            lastProcessedTo = node.to;
          } else if (STRING_TYPES.has(typeName)) {
            const cleaned = stripStringDelimiters(
              view.state.doc.sliceString(node.from, node.to),
            );
            const hits = findMisspelledWhitespaceWords(cleaned, node.from, ignoreList);
            misspellings.push(...hits);
            builder.addRange(node.from, node.to, hits);
            lastProcessedTo = node.to;
          } else if (IDENTIFIER_TYPES.has(typeName)) {
            const text = view.state.doc.sliceString(node.from, node.to);
            // Skip very short identifiers and pure numbers
            if (text.length <= 1 || /^\d+$/.test(text)) return;
            const hits = findMisspelledIdentifierSubWords(text, node.from, ignoreList);
            misspellings.push(...hits);
            builder.addRange(node.from, node.to, hits);
          }
        },
      });
    }

    this.decorations = builder.finish();

    // Publish to the state field so getMisspellingAtPosition can read it.
    view.dispatch({
      effects: updateSpellDiagnosticsEffect.of(misspellings),
    });
  }
}

// ─── Decoration builder helper ──────────────────────────────────

/**
 * Thin wrapper around RangeSetBuilder that maps MisspellingInfo hits into
 * spell-error decorations within a given range.
 */
class DecorationBuilder {
  private readonly inner = new RangeSetBuilder<Decoration>();

  addRange(_from: number, _to: number, hits: MisspellingInfo[]) {
    for (const hit of hits) {
      this.inner.add(hit.from, hit.to, spellErrorDecoration);
    }
  }

  finish() {
    return this.inner.finish();
  }
}

// ─── Extension factory ──────────────────────────────────────────

/**
 * Create the spell-check extension for a given workspace.
 * Returns an array of extensions (state field + ViewPlugin) to push into
 * the editor's extensions array.
 */
export function spellCheckExtension(workspaceID: string): Extension[] {
  return [
    spellCheckField,
    ViewPlugin.fromClass(
      class {
        inner: SpellCheckPlugin;

        constructor(view: EditorView) {
          this.inner = new SpellCheckPlugin(view, workspaceID);
        }

        get decorations() {
          return this.inner.decorations;
        }

        update(update: ViewUpdate) {
          this.inner.update(update);
        }
      },
      {
        decorations: (plugin) => plugin.decorations,
      },
    ),
  ];
}

// ─── Public query API ───────────────────────────────────────────

/**
 * Return the misspelling that covers `pos` in the given editor view, or
 * `null` if the cursor is not on a flagged word.
 *
 * Intended for context-menu "Add to dictionary / Show suggestions" actions.
 */
export function getMisspellingAtPosition(
  view: EditorView,
  pos: number,
): MisspellingInfo | null {
  const misspellings = view.state.field(spellCheckField);
  // Binary-search or linear scan — the list is small in practice.
  for (const m of misspellings) {
    if (pos >= m.from && pos < m.to) {
      return m;
    }
  }
  return null;
}
