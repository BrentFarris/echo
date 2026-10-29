import { escapeHTML as esc } from './helpers';
import type { Endpoint, Generation } from './types';

export const thinkingLevels = ['', 'none', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max'];
export function endpointGeneration(endpoint?: Endpoint): Generation {
  return { endpointId: endpoint?.id || '', model: endpoint?.model || '', thinking: '', temperature: endpoint?.temperature ?? .6 };
}

export function generationSettingsMarkup(prefix = ''): string {
  const id = (name: string) => esc(prefix + name);
  return `<details class="model-settings composer-menu" data-generation-settings><summary aria-haspopup="dialog" aria-expanded="false" title="Model Settings"><span id="${id('model-name')}">Choose a model</span><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" aria-hidden="true"><path d="m6 9 6 6 6-6"/></svg></summary>
    <div class="model-popup popover generation-popup" role="dialog" aria-label="Model Settings" popover="manual">
      <div class="popover-heading">Model</div>
      <label>Machine <select id="${id('model-machine')}" data-setting="machine" aria-label="Model machine"></select></label>
      <div id="${id('endpoint-options')}" class="endpoint-options" data-setting="endpoints" role="listbox" aria-label="Model connection"></div>
      <label class="custom-model">Model identifier<input id="${id('model')}" data-setting="model" placeholder="Model identifier" list="${id('model-options')}"></label>
      <datalist id="${id('model-options')}" data-setting="models"></datalist>
      <label class="slider-setting"><span class="range-label">Thinking <output id="${id('thinking-label')}" data-setting="thinking-label">Default</output></span><input id="${id('thinking')}" data-setting="thinking" aria-label="Thinking" type="range" min="0" max="7" value="0"></label>
      <label class="slider-setting"><span class="range-label">Temperature <output id="${id('temperature-label')}" data-setting="temperature-label">0.60</output></span><input id="${id('temperature')}" data-setting="temperature" aria-label="Temperature" type="range" min="0" max="2" step="0.01" value="0.6"></label>
      <button type="button" id="${id('manage-models')}" data-setting="manage" class="popover-footer">Manage models in Settings</button>
    </div>
  </details>`;
}

type SettingsState = { generation: Generation; endpoints: Endpoint[]; node: string; machines: { id: string; name: string }[] };
type Options = {
  state(): SettingsState;
  change(generation: Generation, commit: boolean): void;
  selectMachine?(node: string): void;
  manage(): void;
  discover(endpoint: Endpoint): Promise<string[]>;
  opened?(): void;
};

/** One set of controls for chat and editor. The top-layer panel escapes Monaco
 * clipping and floating-window stacking contexts, without duplicating IDs. */
export class GenerationSettings {
  private readonly abort = new AbortController();
  readonly popup: HTMLElement;
  private readonly summary: HTMLElement;
  private readonly models = new Map<string, string[]>();
  private disabled = false;
  constructor(private readonly menu: HTMLDetailsElement, private readonly options: Options) {
    this.popup = menu.querySelector<HTMLElement>('.model-popup')!;
    this.summary = menu.querySelector('summary')!;
    document.body.append(this.popup);
    const { signal } = this.abort;
    menu.addEventListener('toggle', () => {
      this.summary.setAttribute('aria-expanded', String(menu.open));
      if (menu.open && !this.disabled) {
        for (const other of document.querySelectorAll<HTMLDetailsElement>('.composer-menu[open]')) if (other !== menu) other.open = false;
        this.options.opened?.(); this.render(); this.popup.showPopover(); this.position();
      }
      else this.popup.hidePopover();
    }, { signal });
    const keydown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') { event.preventDefault(); event.stopPropagation(); this.close(); this.summary.focus(); return; }
      if (event.key === 'Tab' && menu.open) {
        const controls = [...this.popup.querySelectorAll<HTMLElement>('input:not(:disabled), select:not(:disabled), button:not(:disabled)')];
        if (event.target === this.summary && !event.shiftKey) { event.preventDefault(); controls[0]?.focus(); }
        else if (event.target === controls[0] && event.shiftKey) { event.preventDefault(); this.summary.focus(); }
        else if (event.target === controls.at(-1) && !event.shiftKey) { this.close(); this.summary.focus(); }
        else if (event.target === this.summary && event.shiftKey) this.close();
        return;
      }
      if ((event.target as HTMLElement).matches('input, select') || !['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) return;
      const items = [...this.popup.querySelectorAll<HTMLButtonElement>('[role=option]')];
      if (!items.length) return;
      event.preventDefault(); event.stopPropagation(); menu.open = true;
      const index = items.indexOf(document.activeElement as HTMLButtonElement);
      const next = event.key === 'Home' ? 0 : event.key === 'End' ? items.length - 1 : (index + (event.key === 'ArrowUp' ? -1 : 1) + items.length) % items.length;
      // The toggle event exposes the popover before focus moves into it.
      queueMicrotask(() => {
        if (signal.aborted || !menu.open) return;
        this.render();
        if (!this.popup.matches(':popover-open')) this.popup.showPopover();
        this.position(); this.popup.querySelectorAll<HTMLButtonElement>('[role=option]')[next]?.focus();
      });
    };
    menu.addEventListener('keydown', keydown, { signal });
    this.popup.addEventListener('keydown', keydown, { signal });
    this.summary.addEventListener('click', event => { if (this.disabled) event.preventDefault(); }, { signal });
    for (const type of ['pointerdown', 'focusin'] as const) document.addEventListener(type, event => {
      if (!menu.contains(event.target as Node) && !this.popup.contains(event.target as Node)) this.close();
    }, { signal });
    window.addEventListener('resize', () => this.position(), { signal });
    document.addEventListener('scroll', () => this.position(), { signal, capture: true });
    this.field<HTMLSelectElement>('machine').onchange = () => options.selectMachine?.(this.field<HTMLSelectElement>('machine').value);
    for (const field of ['model', 'thinking', 'temperature']) {
      const input = this.field<HTMLInputElement>(field);
      const change = (commit: boolean) => {
        const generation = { ...options.state().generation };
        if (field === 'model') generation.model = input.value;
        else if (field === 'thinking') generation.thinking = thinkingLevels[Number(input.value)];
        else generation.temperature = Number(input.value);
        options.change(generation, commit); this.render();
      };
      input.oninput = () => change(false); input.onchange = () => change(true);
    }
    this.field<HTMLButtonElement>('manage').onclick = () => { this.close(); options.manage(); };
    this.render();
  }
  private field<T extends HTMLElement>(name: string): T { return this.popup.querySelector<T>(`[data-setting="${name}"]`)!; }
  render(): void {
    const { generation, endpoints, machines, node } = this.options.state();
    const machine = this.field<HTMLSelectElement>('machine');
    const machineMarkup = machines.map(m => `<option value="${esc(m.id)}">${esc(m.name)}</option>`).join('');
    if (machine.innerHTML !== machineMarkup) machine.innerHTML = machineMarkup;
    machine.value = node; machine.disabled = this.disabled || !this.options.selectMachine;
    const list = this.field<HTMLElement>('endpoints');
    const markup = endpoints.map(endpoint => `<button type="button" role="option" aria-selected="${endpoint.id === generation.endpointId}" data-endpoint="${esc(endpoint.id)}" title="${esc(endpoint.name)} — ${esc(endpoint.model)}">${esc(endpoint.name)} <span>— ${esc(endpoint.model)}</span></button>`).join('') || '<p class="subtle">Add a model connection in Settings.</p>';
    if (list.innerHTML !== markup) {
      list.innerHTML = markup;
      for (const button of list.querySelectorAll<HTMLButtonElement>('button')) button.onclick = () => {
        const endpoint = this.options.state().endpoints.find(e => e.id === button.dataset.endpoint);
        if (!endpoint || this.disabled) return;
        this.options.change(endpointGeneration(endpoint), true); this.render();
        this.popup.querySelector<HTMLButtonElement>(`[data-endpoint="${CSS.escape(endpoint.id)}"]`)?.focus();
      };
    }
    const endpoint = endpoints.find(e => e.id === generation.endpointId);
    this.summary.querySelector('span')!.textContent = endpoint?.name || generation.model || 'Choose a model';
    const model = this.field<HTMLInputElement>('model');
    if (document.activeElement !== model) model.value = generation.model;
    this.field<HTMLInputElement>('thinking').value = String(Math.max(0, thinkingLevels.indexOf(generation.thinking)));
    const effort = generation.thinking || endpoint?.reasoningEffort;
    this.field('thinking-label').textContent = effort ? (generation.thinking ? '' : 'Default · ') + effort[0].toUpperCase() + effort.slice(1) : 'Default';
    this.field<HTMLInputElement>('temperature').value = String(generation.temperature);
    this.field('temperature-label').textContent = generation.temperature.toFixed(2);
    for (const control of this.popup.querySelectorAll<HTMLInputElement | HTMLButtonElement>('input, button')) control.disabled = this.disabled;
    const key = endpoint ? endpoint.id + ':' + endpoint.url : '';
    this.field('models').innerHTML = (this.models.get(key) || []).map(id => `<option value="${esc(id)}"></option>`).join('');
    if (endpoint && !this.models.has(key)) {
      this.models.set(key, []);
      void this.options.discover(endpoint).then(models => { if (!this.abort.signal.aborted) { this.models.set(key, models); this.render(); } }).catch(() => { /* Manual model identifiers remain available. */ });
    }
    this.position();
  }
  private position(): void {
    if (!this.menu.open || !this.popup.matches(':popover-open')) return;
    const anchor = this.summary.getBoundingClientRect();
    if (!anchor.width || !anchor.height) { this.close(); return; }
    const above = anchor.top - 14, below = innerHeight - anchor.bottom - 14;
    const height = Math.min(640, innerHeight * .7, Math.max(above, below));
    this.popup.style.maxHeight = `${Math.max(80, height)}px`;
    this.popup.style.left = `${Math.max(8, Math.min(anchor.left, innerWidth - this.popup.offsetWidth - 8))}px`;
    this.popup.style.top = `${above >= this.popup.offsetHeight || above >= below ? Math.max(8, anchor.top - this.popup.offsetHeight - 7) : anchor.bottom + 7}px`;
  }
  setDisabled(disabled: boolean): void { this.disabled = disabled; this.summary.setAttribute('aria-disabled', String(disabled)); if (disabled) this.close(); this.render(); }
  close(): void { this.menu.open = false; if (this.popup.matches(':popover-open')) this.popup.hidePopover(); }
  dispose(): void { this.close(); this.abort.abort(); this.popup.remove(); }
}
