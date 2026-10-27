import type { Bot, Plugin } from './types';

export function pluginAvailable(plugin: Plugin | undefined, bot: Bot | undefined): boolean {
  return !!plugin && !!bot && plugin.enabled && !plugin.pending &&
    (plugin.scope !== 'echo' || bot.id === 'echo') && (plugin.scope === 'app' || bot.plugins[plugin.id] !== false);
}

export function appStatus(plugin: Plugin, echo: Bot | undefined): string {
  if (plugin.pending) return 'Awaiting permission';
  if (!plugin.enabled) return 'Disabled';
  if (!pluginAvailable(plugin, echo)) return 'Disabled for Echo';
  return '';
}

export function registeredApps(plugins: Plugin[]): Plugin[] {
  return plugins.filter(p => !!p.app).sort((a, b) => a.name.localeCompare(b.name) || a.id.localeCompare(b.id));
}

export function dockedApps(plugins: Plugin[], dock: string[], echo: Bot | undefined): Plugin[] {
  const apps = new Map(registeredApps(plugins).map(p => [p.id, p]));
  return [...new Set(dock)].map(id => apps.get(id)).filter((p): p is Plugin => !!p && pluginAvailable(p, echo));
}

export function appIcon(plugin: Plugin): HTMLElement {
  const icon = document.createElement('span'); icon.className = 'app-icon'; icon.setAttribute('aria-hidden', 'true');
  const initials = plugin.name.trim().split(/\s+/).slice(0, 2).map(word => Array.from(word)[0] || '').join('').toUpperCase() || '?';
  icon.textContent = initials;
  const source = plugin.app?.icon;
  if (source && new TextEncoder().encode(source).length <= 256 * 1024 && /^data:image\/(svg\+xml|png|jpeg|webp)(;charset=utf-8)?(;base64)?,.+$/is.test(source)) {
    const img = document.createElement('img'); img.alt = ''; img.src = source;
    img.onerror = () => { icon.textContent = initials; };
    icon.replaceChildren(img);
  }
  return icon;
}

type Actions = {
  open: (plugin: Plugin) => Promise<void>;
  dock: (id: string, docked: boolean) => Promise<void>;
  settings: (plugin: Plugin, botDisabled: boolean) => void;
  error: (error: unknown) => void;
};

// Reconcile only when app data changes, preserving focus and dock scroll on chat events.
export class Apps {
  private signature = '';
  private pending = new Set<string>();
  private data?: { plugins: Plugin[]; ids: string[]; echo: Bot | undefined };
  constructor(private catalog: HTMLElement, private dock: HTMLElement, private actions: Actions) {
    dock.addEventListener('wheel', event => {
      if (dock.scrollWidth <= dock.clientWidth || event.ctrlKey || event.shiftKey) return;
      const delta = Math.abs(event.deltaX) > Math.abs(event.deltaY) ? event.deltaX : event.deltaY;
      if (!delta) return;
      const before = dock.scrollLeft; dock.scrollLeft += delta;
      if (dock.scrollLeft !== before) event.preventDefault();
    }, { passive: false });
    dock.addEventListener('keydown', event => {
      const buttons = [...dock.querySelectorAll<HTMLButtonElement>('button')], index = buttons.indexOf(document.activeElement as HTMLButtonElement);
      if (index < 0 || !['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return;
      event.preventDefault();
      const next = event.key === 'Home' ? 0 : event.key === 'End' ? buttons.length - 1 : Math.max(0, Math.min(buttons.length - 1, index + (event.key === 'ArrowRight' ? 1 : -1)));
      buttons[next].focus(); buttons[next].scrollIntoView({ block: 'nearest', inline: 'nearest' });
    });
  }

  render(plugins: Plugin[], ids: string[], echo: Bot | undefined) {
    this.data = { plugins, ids, echo };
    const apps = registeredApps(plugins);
    const signature = JSON.stringify([apps.map(p => [p.id, p.name, p.app, p.enabled, p.pending, p.scope]), ids, echo?.plugins, !!echo, [...this.pending]]);
    if (signature === this.signature) return;
    this.signature = signature;
    const active = document.activeElement as HTMLElement | null;
    const focusKey = active?.dataset.appControl;
    const focusRoot = active && this.dock.contains(active) ? this.dock : this.catalog;
    const scroll = this.dock.scrollLeft;
    const grid = document.createElement('div'); grid.className = 'apps-grid';
    for (const plugin of apps) {
      const status = appStatus(plugin, echo), pinned = ids.includes(plugin.id);
      const card = document.createElement('article'); card.className = 'app-card'; card.dataset.appId = plugin.id;
      const heading = document.createElement('div'); heading.className = 'app-card-heading';
      const name = document.createElement('h2'); name.textContent = plugin.name;
      heading.append(appIcon(plugin), name); card.append(heading);
      if (status) { const label = document.createElement('p'); label.className = 'app-status subtle'; label.textContent = status; card.append(label); }
      const buttons = document.createElement('div'); buttons.className = 'app-card-actions';
      const open = this.button('Open', `open:${plugin.id}`, () => this.actions.open(plugin)); open.disabled = !!status;
      open.setAttribute('aria-label', `Open ${plugin.name}`);
      const pin = this.button(pinned ? 'Undock' : 'Dock', `dock:${plugin.id}`, async () => {
        if (this.pending.has(plugin.id)) return;
        this.pending.add(plugin.id); pin.disabled = true;
        try { await this.actions.dock(plugin.id, !pinned); }
        finally { this.pending.delete(plugin.id); this.signature = ''; if (this.data) this.render(this.data.plugins, this.data.ids, this.data.echo); }
      });
      pin.setAttribute('aria-label', `${pinned ? 'Undock' : 'Dock'} ${plugin.name}`);
      pin.disabled = this.pending.has(plugin.id) || (!pinned && !!status);
      const settings = this.button('Settings', `settings:${plugin.id}`, () => this.actions.settings(plugin, status === 'Disabled for Echo'));
      settings.setAttribute('aria-label', `Settings for ${plugin.name}`);
      buttons.append(open, pin, settings); card.append(buttons); grid.append(card);
    }
    this.catalog.replaceChildren();
    if (apps.length) this.catalog.append(grid);
    else { const empty = document.createElement('p'); empty.className = 'apps-empty subtle'; empty.textContent = 'No apps yet. Ask Echo to create an app, such as a calculator.'; this.catalog.append(empty); }
    this.dock.replaceChildren();
    for (const plugin of dockedApps(plugins, ids, echo)) {
      const button = this.button('', `open:${plugin.id}`, () => this.actions.open(plugin));
      button.className = 'app-dock-button'; button.title = plugin.name; button.setAttribute('aria-label', `Open ${plugin.name}`);
      button.append(appIcon(plugin)); this.dock.append(button);
    }
    this.dock.hidden = !this.dock.childElementCount; this.dock.scrollLeft = scroll;
    if (focusKey) [...focusRoot.querySelectorAll<HTMLElement>('[data-app-control]')].find(el => el.dataset.appControl === focusKey)?.focus({ preventScroll: true });
  }

  private button(label: string, key: string, action: () => unknown): HTMLButtonElement {
    const button = document.createElement('button'); button.type = 'button'; button.textContent = label; button.dataset.appControl = key;
    button.onclick = () => { Promise.resolve().then(action).catch(this.actions.error); };
    return button;
  }
}
