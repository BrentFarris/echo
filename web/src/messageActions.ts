import { icons } from './composer';
import type { Message } from './types';

const waitMessage = 'Wait for the current response to finish before editing or deleting messages.';
type Options = {
  busy: (chatId: string) => boolean;
  notice: (error: unknown, failure?: boolean) => void;
  save: (message: Message, content: string, requestId: string) => Promise<void>;
  remove: (message: Message) => Promise<void>;
  fork: (message: Message, requestId: string) => Promise<void>;
};
type Entry = { message: Message; toolbar: HTMLDivElement; container: HTMLElement; body: HTMLElement; form?: HTMLFormElement; saving: boolean; forkRequestId?: string };

export function messageActions(role: string, act: (action: string, button: HTMLButtonElement) => void) {
  const toolbar = document.createElement('div'); toolbar.className = 'message-actions';
  toolbar.setAttribute('role', 'toolbar'); toolbar.setAttribute('aria-label', 'Message actions');
  const labels = { copy: role === 'user' ? 'Copy message' : 'Copy final response', fork: 'Fork chat from this message', edit: role === 'user' ? 'Edit message' : 'Edit response', trash: role === 'user' ? 'Delete message' : 'Delete response' };
  for (const key of ['copy', 'fork', 'edit', 'trash'] as const) {
    const button = document.createElement('button'); button.type = 'button'; button.className = 'message-action toolbar-icon ' + key;
    button.dataset.action = key; button.innerHTML = icons[key]; button.title = labels[key]; button.setAttribute('aria-label', labels[key]);
    button.onclick = event => { event.stopPropagation(); act(key, button); };
    toolbar.append(button);
  }
  return toolbar;
}

export function beginMessageEdit(message: Message, cancel: () => void, save: (content: string) => Promise<void>) {
  const form = document.createElement('form'); form.className = 'message-edit-form'; form.dataset.editMessage = message.id;
  const textarea = document.createElement('textarea'); textarea.className = 'message-edit-textarea'; textarea.value = message.content;
  textarea.setAttribute('aria-label', message.role === 'user' ? 'Edit message text' : 'Edit response text'); textarea.required = true;
  const help = document.createElement('p'); help.className = 'message-edit-help';
  help.textContent = message.role === 'user' ? 'Saving resubmits this message and removes its response and all later messages. Attachments are kept.' : 'Saving updates this response without generating a new one.';
  help.textContent += ' Ctrl/Cmd+Enter to save · Esc to cancel.';
  const actions = document.createElement('div'); actions.className = 'message-edit-actions';
  const cancelButton = document.createElement('button'); cancelButton.type = 'button'; cancelButton.textContent = 'Cancel'; cancelButton.onclick = cancel;
  const submit = document.createElement('button'); submit.type = 'submit'; submit.textContent = message.role === 'user' ? 'Save & Resubmit' : 'Save';
  actions.append(cancelButton, submit); form.append(textarea, help, actions);
  let pending = false;
  form.onsubmit = async event => {
    event.preventDefault(); if (pending || !textarea.value.trim()) return;
    pending = true; textarea.disabled = true; submit.disabled = true; cancelButton.disabled = true;
    try { await save(textarea.value); }
    finally { pending = false; textarea.disabled = false; submit.disabled = false; cancelButton.disabled = false; }
  };
  textarea.onkeydown = event => {
    if (event.key === 'Escape') { event.preventDefault(); event.stopPropagation(); if (!pending) cancel(); }
    if (event.key === 'Enter' && (event.ctrlKey || event.metaKey)) { event.preventDefault(); form.requestSubmit(); }
  };
  return form;
}

// Keep forms and copy feedback alive across transcript refreshes. The final
// message's editor mounts inside its article; its toolbar belongs to the group.
export class MessageActionUI {
  private entries = new Map<string, Entry>();
  private copyTimers = new WeakMap<HTMLButtonElement, number>();
  constructor(private options: Options) {
    document.addEventListener('click', event => {
      if (!window.matchMedia('(hover: none), (pointer: coarse)').matches) return;
      const target = event.target as HTMLElement;
      const container = target.closest<HTMLElement>('[data-message-actions]');
      for (const entry of this.entries.values()) if (entry.container !== container) entry.container.classList.remove('is-open');
      if (![...this.entries.values()].some(entry => entry.container === container)) return;
      if (!container || target.closest('button, a, input, textarea, summary, form, video, [role="button"]') || window.getSelection()?.toString()) return;
      container.classList.toggle('is-open');
    });
  }
  mount(container: HTMLElement, body: HTMLElement, message: Message) {
    let entry = this.entries.get(message.id);
    if (entry && entry.message.content !== message.content && !entry.saving) { this.close(entry); this.entries.delete(message.id); entry = undefined; }
    if (!entry) {
      entry = { message: { ...message }, container, body, saving: false, toolbar: messageActions(message.role, (action, button) => { void this.act(entry!, action, button); }) };
      this.entries.set(message.id, entry);
    }
    const open = entry.container.classList.contains('is-open');
    entry.container = container; entry.body = body; entry.message = { ...message };
    container.dataset.messageActions = message.id; container.tabIndex = 0; container.classList.toggle('is-open', open);
    container.append(entry.toolbar);
    for (const button of entry.toolbar.querySelectorAll('button')) button.setAttribute('aria-disabled', String(this.options.busy(message.chatId) || entry.saving));
    container.classList.toggle('is-editing', !!entry.form); body.classList.toggle('is-editing', !!entry.form);
    if (entry.form) body.append(entry.form);
  }
  prune(ids: Set<string>) {
    for (const [id, entry] of this.entries) if (!ids.has(id)) { this.close(entry); this.entries.delete(id); }
  }
  private close(entry: Entry) {
    entry.form?.remove(); entry.form = undefined;
    entry.container.classList.remove('is-editing'); entry.body.classList.remove('is-editing');
  }
  private async act(entry: Entry, action: string, button: HTMLButtonElement) {
    if (entry.saving) return;
    if (this.options.busy(entry.message.chatId)) { this.options.notice(action === 'fork' ? 'Wait for the current response to finish before forking the chat.' : waitMessage, true); return; }
    try {
      if (action === 'fork') {
        entry.forkRequestId ||= crypto.randomUUID(); entry.saving = true;
        entry.toolbar.setAttribute('aria-busy', 'true');
        button.title = 'Creating fork…'; button.setAttribute('aria-label', button.title);
        this.mount(entry.container, entry.body, entry.message);
        try { await this.options.fork(entry.message, entry.forkRequestId); entry.forkRequestId = undefined; }
        finally {
          entry.saving = false; entry.toolbar.removeAttribute('aria-busy');
          button.title = 'Fork chat from this message'; button.setAttribute('aria-label', button.title);
          if (entry.container.isConnected) this.mount(entry.container, entry.body, entry.message);
        }
      } else if (action === 'copy') {
        await navigator.clipboard.writeText(entry.message.content);
        const label = entry.message.role === 'user' ? 'Copy message' : 'Copy final response';
        window.clearTimeout(this.copyTimers.get(button));
        button.innerHTML = icons.check; button.classList.add('is-copied'); button.title = 'Copied'; button.setAttribute('aria-label', 'Copied');
        this.copyTimers.set(button, window.setTimeout(() => { button.innerHTML = icons.copy; button.classList.remove('is-copied'); button.title = label; button.setAttribute('aria-label', label); }, 1600));
      } else if (action === 'trash') {
        const prompt = entry.message.role === 'user' ? 'Delete this user message? Its text, attachments, and future AI context will be removed.' : 'Delete this AI message? Its content and any tool results belonging to its calls will be removed from future AI context.';
        if (!window.confirm(prompt)) return;
        entry.saving = true;
        try { await this.options.remove(entry.message); } finally { entry.saving = false; }
      } else if (action === 'edit' && !entry.form) {
        let requestId = crypto.randomUUID(), submittedContent: string | undefined;
        entry.form = beginMessageEdit(entry.message, () => { this.close(entry); button.focus(); }, async content => {
          if (this.options.busy(entry.message.chatId)) { this.options.notice(waitMessage, true); return; }
          if (submittedContent !== undefined && submittedContent !== content) requestId = crypto.randomUUID();
          submittedContent = content; entry.saving = true;
          try { await this.options.save(entry.message, content, requestId); this.close(entry); }
          catch (error) { this.options.notice(error, true); }
          finally { entry.saving = false; if (entry.container.isConnected) this.mount(entry.container, entry.body, entry.message); }
        });
        this.mount(entry.container, entry.body, entry.message);
        entry.form.querySelector('textarea')!.focus();
      }
    } catch (error) { this.options.notice(error, true); }
  }
}
