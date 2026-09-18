import type { editor as Editor, IDisposable } from "monaco-editor";
import { api } from "../../js/api.js";
import { monaco } from "./language";
import { readFile } from "./editorApi";
import { replacementEdit } from "./diffHunks";
import type { FileRef } from "./types";
import { createReferenceChip, installMentionPicker } from "../chatMentionPicker";
import { composerText, snapshotComposer, restoreComposer } from "../chatMentions";
import { patchMarkdownElement } from "../markdown";
import {
  closeInlineChat, findInlineChat, inlineChatKey, openInlineChat,
  type InlineChatFile, type InlineChatSession, type InlineChatChange,
} from "./inlineChatSession";
import "./inlineChat.css";

export type InlineChatTarget = {
  id: string; title: string; ref: FileRef | null; model: Editor.ITextModel;
  revision: string; conflict: boolean; deleted: boolean;
};

export class InlineChatController {
  private disposables: IDisposable[] = [];
  private renderQueued = false;
  private mounted: {
    session: InlineChatSession; target: InlineChatTarget; node: HTMLElement; input: HTMLElement;
    log: HTMLElement; status: HTMLElement; send: HTMLButtonElement; accept: HTMLButtonElement;
    regenerate: HTMLButtonElement; model: HTMLSelectElement;
    zone: string; zoneSpec: Editor.IViewZone; abort: AbortController; unsubscribe: () => void; observer: ResizeObserver;
    previewZones: string[]; decorations: string[]; preview: InlineChatChange[] | null;
    height: number; accepting: boolean; renderGeneration: number;
    zoneHost: HTMLElement | null; zoneHostHidden: string | null;
  } | null = null;

  constructor(private editor: Editor.IStandaloneCodeEditor, private options: {
    workspaceId: string; target(): InlineChatTarget | null; keepOpen(): void;
    activateReference(ref: FileRef): void | Promise<void>;
  }) {
    this.disposables.push(
      editor.onDidChangeModel(() => this.refresh()),
      editor.onDidChangeModelContent(() => this.scheduleRender()),
      editor.onDidLayoutChange(() => this.layout()),
      editor.onDidChangeConfiguration(event => {
        if (event.hasChanged(monaco.editor.EditorOption.fontInfo) || event.hasChanged(monaco.editor.EditorOption.lineHeight)) {
          if (this.mounted) this.mounted.preview = null;
          this.scheduleRender();
        }
      }),
      editor.onDidChangeModelOptions(() => { if (this.mounted) this.mounted.preview = null; this.scheduleRender(); }),
      editor.onDidScrollChange(() => this.layout()),
    );
  }

  private key(target: InlineChatTarget): string { return inlineChatKey(this.options.workspaceId, target.id); }

  private scheduleRender(): void {
    if (this.renderQueued) return;
    this.renderQueued = true;
    queueMicrotask(() => { this.renderQueued = false; this.render(); });
  }

  open(): void {
    const target = this.options.target();
    if (!target || this.editor.getModel() !== target.model) return;
    this.options.keepOpen();
    const session = openInlineChat(this.options.workspaceId, this.key(target), this.snapshot(target));
    this.refresh();
    const mounted = this.mounted;
    if (!mounted || mounted.session !== session) return;
    const line = session.file.selections[0]?.startLine || 1;
    this.editor.revealLineInCenter(Math.min(line, target.model.getLineCount()), monaco.editor.ScrollType.Immediate);
    // Include the view zone itself when the selection is near the top edge.
    const top = this.editor.getTopForLineNumber(Math.min(line, target.model.getLineCount())) - mounted.height;
    if (top < this.editor.getScrollTop()) this.editor.setScrollTop(Math.max(0, top - 16));
    // Newly added zones are hidden until Monaco's next render. Focusing a
    // hidden contenteditable is ignored by the browser.
    this.editor.render(true);
    mounted.input.focus({ preventScroll: true });
  }

  private snapshot(target: InlineChatTarget): InlineChatFile {
    const selections = this.editor.getSelections() || [new monaco.Selection(1, 1, 1, 1)];
    return { title: target.title, ref: target.ref || undefined, content: target.model.getValue(), selections: selections.map(range => ({
      startLine: range.startLineNumber, startColumn: range.startColumn,
      endLine: range.endLineNumber, endColumn: range.endColumn, text: target.model.getValueInRange(range),
    })) };
  }

  refresh(): void {
    const target = this.options.target();
    const session = target ? findInlineChat(this.key(target)) : undefined;
    if (session && this.mounted?.session === session && this.mounted.target.model === target?.model && this.editor.getModel() === target?.model) { this.render(); return; }
    this.detach();
    if (!session || !target || this.editor.getModel() !== target.model) return;
    const node = document.createElement("section");
    node.className = "inline-chat-zone";
    node.setAttribute("aria-label", "Inline chat");
    node.innerHTML = `
      <div class="inline-chat-log" aria-label="Inline conversation"></div>
      <form class="inline-chat-form">
        <div class="inline-chat-input-wrap">
          <div class="chat-composer-editor" contenteditable="true" role="textbox" aria-multiline="true" aria-label="Inline message"
            aria-autocomplete="list" aria-expanded="false" spellcheck="true" data-placeholder="Ask about or edit this code…"></div>
        </div>
        <div class="inline-chat-actions">
          <button type="button" data-inline-accept title="Accept changes" aria-label="Accept changes"><span class="codicon codicon-check"></span></button>
          <button type="submit" data-inline-send title="Send message" aria-label="Send message"><span class="codicon codicon-send"></span></button>
          <button type="button" data-inline-close title="Cancel and close" aria-label="Cancel and close"><span class="codicon codicon-close"></span></button>
        </div>
        <div class="inline-chat-toolbar"><select aria-label="Inline chat model"><option value="">Default chat model</option></select><span class="inline-chat-context"></span></div>
      </form>
      <div class="inline-chat-footer"><span role="status"></span><button type="button" data-inline-regenerate>Regenerate from current file</button></div>`;
    const abort = new AbortController(), { signal } = abort;
    // The editor's mouse and keyboard handlers must not claim interactions
    // with the composer. Native contenteditable behavior still runs normally.
    for (const type of ["mousedown", "pointerdown", "keydown", "keyup", "contextmenu"]) {
      node.addEventListener(type, event => event.stopPropagation(), { signal });
    }
    node.addEventListener("wheel", event => {
      const scroller = (event.target as Element).closest<HTMLElement>(".inline-chat-log, .chat-composer-editor, .chat-mention-picker");
      if (scroller && (event.deltaY < 0 ? scroller.scrollTop > 0 : scroller.scrollTop + scroller.clientHeight < scroller.scrollHeight)) event.stopPropagation();
    }, { signal, passive: true });
    const input = node.querySelector<HTMLElement>("[contenteditable]")!;
    const inputWrap = node.querySelector<HTMLElement>(".inline-chat-input-wrap")!;
    const mentions = installMentionPicker(input, inputWrap, {
      workspaceId: this.options.workspaceId, signal,
      onActivateReference: reference => reference.kind === "file" ? this.options.activateReference(reference.ref) : undefined,
    });
    restoreComposer(input, session.draft, createReferenceChip);
    input.addEventListener("input", () => { session.draft = snapshotComposer(input); this.layout(); }, { signal });
    // Paste only plain text into the composer, preserving existing mention chips.
    input.addEventListener("paste", event => {
      event.preventDefault();
      document.execCommand("insertText", false, event.clipboardData?.getData("text/plain") || "");
    }, { signal });
    const submit = () => {
      if (session.status === "running") { session.stop(); return; }
      if (this.staleReason()) { this.render(); return; }
      const text = composerText(input);
      if (!text.trim()) return;
      const references = snapshotComposer(input).flatMap(segment => {
        if (segment.type !== "reference") return [];
        const { type: _type, workspaceId: _workspaceId, ...reference } = segment;
        return [reference];
      });
      input.replaceChildren();
      session.draft = [];
      mentions.clear();
      void session.send(text, references);
      input.focus();
    };
    node.querySelector("form")!.addEventListener("submit", event => { event.preventDefault(); submit(); }, { signal });
    input.addEventListener("keydown", event => {
      if (mentions.handleKeydown(event)) return;
      if (event.key === "Enter" && !event.shiftKey && !event.isComposing) {
        event.preventDefault(); event.stopPropagation();
        if (session.status !== "running") submit();
      } else if (event.key === "Escape") { event.preventDefault(); event.stopPropagation(); this.editor.focus(); }
    }, { signal });
    node.querySelector("[data-inline-close]")!.addEventListener("click", () => this.close(), { signal });
    const accept = node.querySelector<HTMLButtonElement>("[data-inline-accept]")!;
    accept.addEventListener("click", () => void this.accept(), { signal });
    const regenerate = node.querySelector<HTMLButtonElement>("[data-inline-regenerate]")!;
    regenerate.addEventListener("click", () => {
      const latest = this.options.target();
      if (!latest || latest.conflict || latest.deleted) return;
      const file = this.snapshot(latest);
      session.regenerate(file);
    }, { signal });
    const model = node.querySelector<HTMLSelectElement>("select")!;
    model.addEventListener("change", () => { session.model = model.value; }, { signal });
    void api("/api/settings", { method: "GET" }).then((data: { settings: { endpoints?: { model: string; name?: string }[] } }) => {
      if (signal.aborted) return;
      for (const endpoint of data.settings.endpoints || []) if (endpoint.model) model.add(new Option(endpoint.name || endpoint.model, endpoint.model));
      model.value = session.model;
    }).catch(() => { /* The default model remains usable without the selector. */ });
    const line = Math.min(session.file.selections[0]?.startLine || 1, target.model.getLineCount());
    let zone = "";
    const zoneSpec: Editor.IViewZone = { afterLineNumber: line-1, heightInPx: 100, domNode: node, ordinal: 0, showInHiddenAreas: true, suppressMouseDown: false };
    this.editor.changeViewZones(accessor => { zone = accessor.addZone(zoneSpec); });
    // Monaco marks its decorative view-zone layer aria-hidden. This zone has
    // interactive controls, so expose the layer for the lifetime of the widget.
    const zoneHost = node.parentElement, zoneHostHidden = zoneHost?.getAttribute("aria-hidden") ?? null;
    zoneHost?.removeAttribute("aria-hidden");
    const observer = new ResizeObserver(() => this.layout());
    this.mounted = {
      session, target: { ...target }, node, input, log: node.querySelector(".inline-chat-log")!, status: node.querySelector("[role=status]")!,
      send: node.querySelector("[data-inline-send]")!, accept, regenerate, model, zone, zoneSpec, abort, observer,
      unsubscribe: session.subscribe(() => this.render()), previewZones: [], decorations: [], preview: null,
      height: 100, accepting: false, renderGeneration: 0,
      zoneHost, zoneHostHidden,
    };
    // Observe children because Monaco assigns the outer view zone's height.
    Array.from(node.children).forEach(child => observer.observe(child));
    this.render();
  }

  private staleReason(): string {
    const m = this.mounted, target = this.options.target();
    if (!m || !target || this.key(target) !== m.session.key) return "The file is no longer active.";
    if (target.ref?.rootId !== m.session.file.ref?.rootId || target.ref?.path !== m.session.file.ref?.path) return "The file location has changed. Regenerate to use the current file.";
    if (target.conflict || target.deleted || m.session.diskConflict) return "The file changed on disk. Resolve or reload the file before regenerating.";
    if (target.model.getValue() !== m.session.file.content) return "The file has changed. Regenerate to use your latest edits.";
    return "";
  }

  private render(): void {
    const m = this.mounted;
    if (!m) return;
    const { session } = m, busy = session.status === "running", stale = this.staleReason();
    m.node.classList.toggle("is-running", busy);
    m.node.setAttribute("aria-busy", String(busy));
    m.send.innerHTML = `<span class="codicon codicon-${busy ? "debug-stop" : "send"}"></span>`;
    m.send.title = busy ? "Stop response" : "Send message";
    m.send.setAttribute("aria-label", m.send.title);
    m.send.disabled = m.accepting || Boolean(stale && !busy);
    m.accept.hidden = !session.hasProposal;
    m.accept.disabled = busy || m.accepting || Boolean(stale);
    m.model.disabled = busy || m.accepting;
    m.regenerate.hidden = !stale;
    m.regenerate.disabled = busy || m.accepting || Boolean(this.options.target()?.conflict || this.options.target()?.deleted);
    m.status.textContent = stale || session.error || (busy ? "Working…" : session.status === "stopped" ? "Stopped" : session.hasProposal ? "Review changes, accept, or send a follow-up." : "");
    m.node.querySelector<HTMLElement>(".inline-chat-footer")!.hidden = !m.status.textContent;
    const selection = session.file.selections[0];
    m.node.querySelector<HTMLElement>(".inline-chat-context")!.textContent = `${session.file.title} · ${selection?.text ? `lines ${selection.startLine}–${selection.endLine}` : `line ${selection?.startLine || 1}`}`;
    const atBottom = m.log.scrollHeight - m.log.scrollTop - m.log.clientHeight < 32;
    session.history.forEach((message, index) => {
      let item = m.log.children[index] as HTMLElement | undefined;
      if (!item) { item = document.createElement("div"); m.log.append(item); }
      item.className = `inline-chat-message is-${message.role}`;
      item.setAttribute("aria-label", message.role === "user" ? "Your message" : "AI response");
      if (item.dataset.content !== message.content) { patchMarkdownElement(item, message.content); item.dataset.content = message.content; }
    });
    while (m.log.children.length > session.history.length) m.log.lastChild?.remove();
    m.log.hidden = session.history.length === 0;
    if (atBottom) m.log.scrollTop = m.log.scrollHeight;
    const preview = stale ? null : session.changes;
    if (m.preview !== preview) { this.clearPreview(); if (preview) this.renderPreview(preview); m.preview = preview; }
    this.layout();
  }

  private renderPreview(changes: InlineChatChange[]): void {
    const m = this.mounted;
    if (!m) return;
    const generation = ++m.renderGeneration;
    const font = this.editor.getOption(monaco.editor.EditorOption.fontInfo);
    const lineHeight = this.editor.getOption(monaco.editor.EditorOption.lineHeight);
    const decorations: Editor.IModelDeltaDecoration[] = [];
    this.editor.changeViewZones(accessor => {
      for (const change of changes) {
        if (change.endLine > change.startLine) decorations.push({
          range: new monaco.Range(change.startLine, 1, change.endLine-1, m.target.model.getLineMaxColumn(change.endLine-1)),
          options: { isWholeLine: true, className: "inline-chat-removed", linesDecorationsClassName: "inline-chat-removed-margin" },
        });
        if (!change.newLines.length) continue;
        const node = document.createElement("div");
        node.className = "inline-chat-added-lines";
        node.setAttribute("aria-label", "Proposed added code");
        node.style.fontFamily = font.fontFamily;
        node.style.fontSize = `${font.fontSize}px`;
        node.style.fontWeight = font.fontWeight;
        node.style.letterSpacing = `${font.letterSpacing}px`;
        node.style.fontFeatureSettings = font.fontFeatureSettings;
        node.style.fontVariationSettings = font.fontVariationSettings;
        node.style.lineHeight = `${lineHeight}px`;
        node.style.tabSize = String(m.target.model.getOptions().tabSize);
        const text = change.newLines.join("\n");
        node.textContent = text;
        void monaco.editor.colorize(text, m.target.model.getLanguageId(), { tabSize: m.target.model.getOptions().tabSize }).then(html => {
          if (this.mounted === m && generation === m.renderGeneration) node.innerHTML = html;
        });
        m.previewZones.push(accessor.addZone({ afterLineNumber: change.endLine-1,
          heightInPx: change.newLines.length * lineHeight, domNode: node, ordinal: 1, showInHiddenAreas: true,
          minWidthInPx: change.newLines.reduce((maximum, line) => Math.max(maximum, line.length), 0) * font.typicalHalfwidthCharacterWidth,
        }));
      }
    });
    if (decorations.length) m.decorations = m.target.model.deltaDecorations([], decorations);
  }

  private clearPreview(): void {
    const m = this.mounted;
    if (!m) return;
    ++m.renderGeneration;
    if (m.decorations.length && !m.target.model.isDisposed()) m.target.model.deltaDecorations(m.decorations, []);
    this.editor.changeViewZones(accessor => m.previewZones.forEach(id => accessor.removeZone(id)));
    m.previewZones = []; m.decorations = []; m.preview = null;
  }

  private layout(): void {
    const m = this.mounted;
    if (!m) return;
    const layout = this.editor.getLayoutInfo();
    m.node.style.width = `${Math.max(180, layout.contentWidth)}px`;
    m.node.style.transform = `translateX(${this.editor.getScrollLeft()}px)`;
    const height = Math.ceil(Array.from(m.node.children).reduce((sum, child) => sum + (child as HTMLElement).getBoundingClientRect().height, 0)) + 18;
    const afterLine = Math.min(m.session.file.selections[0]?.startLine || 1, m.target.model.getLineCount())-1;
    if (height > 18 && (height !== m.height || afterLine !== m.zoneSpec.afterLineNumber)) {
      m.height = height;
      m.zoneSpec.heightInPx = height;
      m.zoneSpec.afterLineNumber = afterLine;
      this.editor.changeViewZones(accessor => accessor.layoutZone(m.zone));
    }
  }

  private async accept(): Promise<void> {
    const m = this.mounted;
    if (!m || !m.session.hasProposal || m.session.status === "running" || m.accepting) return;
    if (this.staleReason()) { this.render(); return; }
    m.accepting = true;
    this.render();
    try {
      const target = this.options.target()!;
      if (target.ref) {
        const snapshot = await readFile(this.options.workspaceId, target.ref);
        if (snapshot.revision !== target.revision) { m.session.diskConflict = true; return; }
      }
      if (this.mounted !== m || this.staleReason()) return;
      const replacement = replacementEdit(target.model.getValue(), m.session.proposal!);
      const start = target.model.getPositionAt(replacement.start), end = target.model.getPositionAt(replacement.end);
      this.detach();
      closeInlineChat(m.session.key);
      target.model.pushStackElement();
      target.model.pushEditOperations(null, [{ range: new monaco.Range(start.lineNumber, start.column, end.lineNumber, end.column), text: replacement.text }], () => null);
      target.model.pushStackElement();
      this.editor.focus();
    } catch (error) {
      m.session.error = error instanceof Error ? error.message : String(error);
    } finally {
      m.accepting = false;
      if (this.mounted === m) this.render();
    }
  }

  close(): void {
    const key = this.mounted?.session.key;
    this.detach();
    if (key) closeInlineChat(key);
    this.editor.focus();
  }

  detach(): void {
    const m = this.mounted;
    if (!m) return;
    m.session.draft = snapshotComposer(m.input);
    m.unsubscribe(); m.abort.abort(); m.observer.disconnect();
    if (m.zoneHostHidden !== null) m.zoneHost?.setAttribute("aria-hidden", m.zoneHostHidden);
    this.clearPreview();
    this.mounted = null;
    this.editor.changeViewZones(accessor => accessor.removeZone(m.zone));
  }

  dispose(): void { this.detach(); this.disposables.forEach(item => item.dispose()); }
}
