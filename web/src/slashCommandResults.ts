import { escapeHTML as esc } from './helpers';

type Result = { id: number; chat: string; label: string; pending: boolean; error: boolean; output: string; expanded: boolean };

/** Results are browser-local and stay with their originating chat. */
export class SlashCommandResults {
  private items: Result[] = [];
  private chat = '';
  private nextID = 0;
  constructor(private root: HTMLElement) {}
  show(chat: string) { this.chat = chat; this.render(); }
  start(chat: string, label: string) {
    const result: Result = { id: ++this.nextID, chat, label, pending: true, error: false, output: '', expanded: false };
    this.items.push(result); this.render();
    return (value: unknown, error = false) => {
      result.pending = false; result.error = error;
      result.output = value instanceof Error ? value.message : typeof value === 'string' ? value : JSON.stringify(value, null, 2) ?? 'Completed.';
      this.render();
    };
  }
  private render() {
    const visible = this.items.filter(item => item.chat === this.chat);
    this.root.hidden = !visible.length;
    this.root.innerHTML = visible.map(item => `<article data-command-result="${item.id}"><div><span role="${item.error ? 'alert' : 'status'}">${esc(item.label)} · ${item.pending ? 'Running…' : item.error ? 'Failed' : 'Completed'}</span>${!item.pending ? `<button type="button" data-dismiss-result aria-label="Dismiss ${esc(item.label)} result">×</button>` : ''}</div>${!item.pending ? `<details ${item.expanded ? 'open' : ''}><summary>${item.error ? 'Error details' : 'Result'}</summary><pre>${esc(item.output)}</pre></details>` : ''}</article>`).join('');
    for (const card of this.root.querySelectorAll<HTMLElement>('[data-command-result]')) {
      const item = this.items.find(item => item.id === Number(card.dataset.commandResult))!;
      card.querySelector<HTMLDetailsElement>('details')?.addEventListener('toggle', event => { item.expanded = (event.currentTarget as HTMLDetailsElement).open; });
      card.querySelector<HTMLButtonElement>('[data-dismiss-result]')?.addEventListener('click', () => { this.items = this.items.filter(result => result !== item); this.render(); });
    }
  }
}
