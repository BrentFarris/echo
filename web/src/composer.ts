import { escapeHTML as esc } from './helpers';
import type { ReferenceInput } from './referenceEditor';
import type { Attachment, Bot, Segment } from './types';

const svg = (body: string) => `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${body}</svg>`;
export const icons = {
  copy: svg('<rect x="9" y="9" width="11" height="11" rx="2"/><path d="M15 9V6a2 2 0 0 0-2-2H6a2 2 0 0 0-2 2v7a2 2 0 0 0 2 2h3"/>'),
  check: svg('<path d="m20 6-11 11-5-5"/>'),
  fork: svg('<circle cx="12" cy="18" r="3"/><circle cx="6" cy="6" r="3"/><circle cx="18" cy="6" r="3"/><path d="M18 9v2c0 .6-.4 1-1 1H7c-.6 0-1-.4-1-1V9M12 12v3"/>'),
  edit: svg('<path d="m16 3 5 5L8 21H3v-5L16 3ZM14 5l5 5"/>'),
  rerun: svg('<path d="M21 12a9 9 0 0 1-15 6.7L3 16"/><path d="M3 21v-5h5"/><path d="M3 12a9 9 0 0 1 15-6.7L21 8"/><path d="M21 3v5h-5"/>'),
  trash: svg('<path d="M3 6h18M9 6V3h6v3M5 6l1 15h12l1-15M10 10v7M14 10v7"/>'),
  send: svg('<path d="m22 2-7 20-4-9-9-4 20-7Z"/><path d="M22 2 11 13"/>'),
  stop: svg('<rect x="6" y="6" width="12" height="12" rx="1" fill="currentColor" stroke="none"/>'),
  plus: svg('<path d="M12 5v14M5 12h14"/>'),
  close: svg('<path d="m6 6 12 12M6 18 18 6"/>'),
  image: svg('<rect x="3" y="3" width="18" height="18" rx="2"/><circle cx="8" cy="8" r="1.5"/><path d="m21 15-6-6L3 21"/>'),
  video: svg('<rect x="3" y="5" width="13" height="14" rx="2"/><path d="m16 10 5-3v10l-5-3"/>'),
  file: svg('<path d="M14 2H5v20h14V7l-5-5Z"/><path d="M14 2v6h5M8 13h8M8 17h6"/>'),
  chevron: svg('<path d="m6 9 6 6 6-6"/>'),
};

export const composerMarkup = `<form class="composer">
  <div class="picker" id="reference-picker" role="listbox" aria-label="File and bot suggestions" hidden></div>
  <div class="attachment-drafts" aria-label="Attachments" hidden></div>
  <reference-input role="textbox" contenteditable="true" aria-multiline="true" data-empty id="composer" aria-label="Message" aria-controls="reference-picker" aria-expanded="false" aria-autocomplete="list" data-placeholder="Ask Echo, mention @bots, or reference #files…"></reference-input>
  <div class="composer-bottom">
    <details class="attachment-menu composer-menu"><summary class="toolbar-icon" title="Attach file" aria-label="Attach file" aria-haspopup="menu">${icons.plus}</summary>
      <div class="attachment-popup popover" role="menu" aria-label="Attachment types">
        <button type="button" role="menuitem" data-attach="image">${icons.image}<span>Image</span></button>
        <button type="button" role="menuitem" data-attach="video">${icons.video}<span>Video</span></button>
        <button type="button" role="menuitem" data-attach="file">${icons.file}<span>File</span></button>
      </div>
    </details>
    <details class="model-settings composer-menu"><summary aria-haspopup="dialog">Model Settings ${icons.chevron}</summary>
      <div class="model-popup popover" role="dialog" aria-label="Model Settings">
        <div class="popover-heading">Model</div><div id="endpoint-options" role="listbox" aria-label="Model connection"></div>
        <label class="custom-model">Model identifier<input id="model" placeholder="Model identifier"></label>
        <label class="slider-setting"><span class="range-label">Thinking <output id="thinking-label">Default</output></span><input aria-label="Thinking" id="thinking" type="range" min="0" max="7" value="0"></label>
        <label class="slider-setting"><span class="range-label">Temperature <output id="temperature-label">0.60</output></span><input aria-label="Temperature" id="temperature" type="range" min="0" max="2" step="0.01" value="0.6"></label>
        <button type="button" id="manage-models" class="popover-footer">Manage models in Settings</button>
      </div>
    </details>
    <span id="model-name" class="subtle"></span><span class="spacer"></span>
    <button id="send" type="submit" class="send-button" aria-label="Send message" title="Send message">${icons.send}</button>
  </div>
</form>`;

export function formatBytes(bytes: number) {
  if (bytes < 1024) return `${bytes} B`;
  return bytes < 1024 * 1024 ? `${(bytes / 1024).toFixed(1)} KB` : `${(bytes / 1024 / 1024).toFixed(1)} MB`;
}
export type DraftAttachment = { file: File; url: string; uploaded?: Attachment };

// Only replace the prefix we inserted; user-authored mentions remain recipients.
export function prefixBotMention(input: ReferenceInput, bot: Bot, automatic?: Segment): Segment | undefined {
  const draft = input.snapshot(), leading = draft.references.find(ref => ref.type === 'bot' && ref.range?.start === 0);
  const owned = !!automatic && !!leading && leading.botId === automatic.botId && leading.text === automatic.text;
  const text = '@' + bot.handle;
  if (leading?.botId === bot.id && leading.text === text) return owned ? automatic : undefined;
  const end = owned ? leading.range!.end + Number(draft.text[leading.range!.end] === ' ') : 0;
  const startSelection = input.selectionStart, endSelection = input.selectionEnd;
  const alreadyPrefixed = draft.references.some(ref => ref.range?.start === end && ref.botId === bot.id && ref.text === text);
  const prefix = alreadyPrefixed ? '' : text + ' ';
  const ref: Segment | undefined = alreadyPrefixed ? undefined : { type: 'bot', botId: bot.id, text };
  input.replace(prefix, 0, end, ref);
  const shift = (position: number) => Math.max(prefix.length, position + prefix.length - end);
  input.setSelectionRange(shift(startSelection), shift(endSelection));
  return ref;
}

export function releaseAttachments(attachments: DraftAttachment[]) { attachments.forEach(a => URL.revokeObjectURL(a.url)); }
export function attachmentCard(name: string, size: number, type: string, url: string, remove = false) {
  const preview = type.startsWith('image/') ? `<img src="${esc(url)}" alt="">` : type.startsWith('video/') ? icons.video : icons.file;
  return `<button type="button" class="attachment-preview" aria-label="Preview ${esc(name)}">${preview}</button><span class="attachment-details"><strong title="${esc(name)}">${esc(name)}</strong><small>${formatBytes(size)}</small></span>${remove ? `<button type="button" class="attachment-remove" aria-label="Remove ${esc(name)}">${icons.close}</button>` : ''}`;
}
export function previewAttachment(name: string, type: string, url: string) {
  const modal = document.createElement('dialog'); modal.className = 'attachment-viewer';
  modal.innerHTML = `<header><strong>${esc(name)}</strong><button class="toolbar-icon" aria-label="Close preview">${icons.close}</button></header>${type.startsWith('image/') ? `<img src="${esc(url)}" alt="${esc(name)}">` : type.startsWith('video/') ? `<video src="${esc(url)}" controls></video>` : `<p>This file is attached to the message.</p>`}<a href="${esc(url)}" download="${esc(name)}">Download ${esc(name)}</a>`;
  modal.querySelector('button')!.onclick = () => modal.close(); modal.onclose = () => modal.remove();
  document.body.append(modal); modal.showModal();
}

export function autosizeComposer() {
  const input = document.querySelector<ReferenceInput>('#composer');
  const form = document.querySelector<HTMLElement>('.composer'), main = document.querySelector<HTMLElement>('.main');
  if (!input || !form || !main) return;
  input.style.height = '0px';
  const otherHeight = form.getBoundingClientRect().height;
  const limit = Math.max(42, main.clientHeight * .5 - otherHeight);
  input.style.height = `${Math.min(Math.max(42, input.scrollHeight), limit)}px`;
  input.style.overflowY = input.scrollHeight > limit ? 'auto' : 'hidden';
}

export function wireComposerMenus(onFiles: (files: File[]) => void) {
  const menus = [...document.querySelectorAll<HTMLDetailsElement>('.composer-menu')];
  const close = () => menus.forEach(m => { m.open = false; });
  menus.forEach(menu => {
    menu.addEventListener('toggle', () => {
      menu.querySelector('summary')!.setAttribute('aria-expanded', String(menu.open));
      if (menu.open) menus.filter(m => m !== menu).forEach(m => { m.open = false; });
    });
    menu.addEventListener('keydown', e => {
      if (e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); menu.open = false; menu.querySelector('summary')!.focus(); }
      if (!['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(e.key) || (e.target as HTMLElement).matches('input')) return;
      const items = [...menu.querySelectorAll<HTMLElement>('[role="option"], [role="menuitem"]')];
      if (!items.length) return;
      e.preventDefault(); menu.open = true;
      const index = items.indexOf(document.activeElement as HTMLElement);
      const next = e.key === 'Home' ? 0 : e.key === 'End' ? items.length - 1 : (index + (e.key === 'ArrowUp' ? -1 : 1) + items.length) % items.length;
      items[next].focus();
    });
  });
  document.addEventListener('pointerdown', e => { if (!(e.target as HTMLElement).closest('.composer-menu')) close(); });
  document.addEventListener('focusin', e => { if (!(e.target as HTMLElement).closest('.composer-menu')) close(); });
  for (const button of document.querySelectorAll<HTMLButtonElement>('[data-attach]')) button.onclick = () => {
    close(); const input = document.createElement('input'); input.type = 'file'; input.multiple = true;
    input.accept = button.dataset.attach === 'image' ? 'image/png,image/jpeg,image/gif,image/webp' : button.dataset.attach === 'video' ? 'video/mp4,video/webm,video/quicktime,.mov' : '';
    input.onchange = () => { onFiles([...input.files || []]); input.remove(); }; input.oncancel = () => input.remove();
    input.hidden = true; document.body.append(input); input.click();
  };
  const input = document.querySelector<ReferenceInput>('#composer')!;
  input.addEventListener('paste', e => {
    const files = [...e.clipboardData?.files || []];
    if (files.length) { e.preventDefault(); onFiles(files); }
  });
  const form = document.querySelector<HTMLElement>('.composer')!;
  form.ondragover = e => { if (e.dataTransfer?.types.includes('Files')) { e.preventDefault(); form.classList.add('drag-over'); } };
  form.ondragleave = e => { if (!form.contains(e.relatedTarget as Node)) form.classList.remove('drag-over'); };
  form.ondrop = e => { form.classList.remove('drag-over'); const files = [...e.dataTransfer?.files || []]; if (files.length) { e.preventDefault(); onFiles(files); } };
  new ResizeObserver(autosizeComposer).observe(document.querySelector('.main')!);
  autosizeComposer();
}
