import type { FileChange, Message } from './types';
import type { ResponseGroup } from './conversation';
import { escapeHTML as esc } from './helpers';
import { icons } from './composer';

export type ChangedFile = FileChange & { messageId: string };

// Collapse repeated writes into one net change, retaining a durable tool-message
// reference for the file-opening API. Never infer paths from assistant output.
export function changedFiles(messages: Message[]): ChangedFile[] {
  const files = new Map<string, ChangedFile>();
  for (const message of messages) {
    if (message.role !== 'tool') continue;
    for (const change of message.fileChanges || []) {
      const first = files.get(change.path);
      files.set(change.path, { ...change, before: first ? first.before : change.before, messageId: message.id });
    }
  }
  return [...files.values()].filter(file => file.before !== file.after).map(file => ({ ...file, operation: (!file.before ? 'created' : !file.after ? 'deleted' : 'edited') as FileChange['operation'] })).sort((a, b) => a.displayPath.localeCompare(b.displayPath));
}

export function fileChangesFooter(group: ResponseGroup, open: (file: ChangedFile) => Promise<void>, editorEnabled = false): HTMLElement | null {
  const status = group.status || group.messages.at(-1)?.status;
  if (!status || ['running', 'waiting', 'queued'].includes(status)) return null;
  const files = changedFiles(group.messages), notices = [...new Set(group.messages.map(m => m.fileChangesNotice).filter(Boolean))];
  if (!files.length && !notices.length) return null;
  const footer = document.createElement('section'); footer.className = 'files-changed'; footer.setAttribute('aria-label', 'Files changed');
  footer.innerHTML = `<div class="files-changed-heading"><strong>Files changed</strong><span>${files.length} ${files.length === 1 ? 'file' : 'files'}</span></div><ul class="changed-file-list"></ul>`;
  const list = footer.querySelector('ul')!;
  for (const file of files) {
    const action = editorEnabled
      ? (file.operation === 'deleted' ? 'Show containing folder in code editor' : 'Open in code editor')
      : (file.operation === 'deleted' ? 'Show containing folder in File Explorer' : 'Show in File Explorer');
    const label = ({ created: 'Created', edited: 'Edited', deleted: 'Deleted' })[file.operation];
    const item = document.createElement('li');
    item.innerHTML = `<button type="button" class="file-change" data-file-change="${esc(group.key + ':' + file.path)}" title="${esc(action + '\n' + file.path)}" aria-label="${esc(action + ': ' + file.displayPath + ' (' + label + ')')}">${icons.file}<span class="changed-file-path">${esc(file.displayPath)}</span><span class="file-operation ${file.operation}">${label}</span></button>`;
    const button = item.querySelector('button')!;
    button.onclick = async () => { button.disabled = true; try { await open(file); } finally { button.disabled = false; } };
    list.append(item);
  }
  for (const text of notices) { const note = document.createElement('p'); note.className = 'file-changes-note'; note.textContent = text!; footer.append(note); }
  return footer;
}
