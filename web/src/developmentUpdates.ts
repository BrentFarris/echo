import { InstanceClient, entryNode, instances, machineName } from './instances';

const notices = new Map<string, boolean>();
let pending: Promise<void> | undefined;
let timer: number | undefined;
let lastRefresh = 0;

export function setDevelopmentUpdate(node: string, available: boolean) {
  notices.set(node || entryNode, available);
  renderDevelopmentUpdates();
}

export function developmentUpdateNode(preferred = entryNode) {
  return notices.get(preferred) ? preferred : [...notices].find(([node, available]) => available && instances.get(node)?.online)?.[0] || preferred;
}

export function renderDevelopmentUpdates() {
  const any = [...notices.values()].some(Boolean);
  for (const button of document.querySelectorAll<HTMLElement>('[data-update-notice]')) {
    const available = button.dataset.updateSuppressed !== 'true' && (button.dataset.updateFallback === 'true' || (button.dataset.updateNotice === '*' ? any : notices.get(button.dataset.updateNotice || entryNode)));
    let dot = button.querySelector<HTMLElement>(':scope > .update-dot');
    if (available) {
      if (!dot) { dot = document.createElement('span'); dot.className = 'update-dot'; dot.setAttribute('aria-hidden', 'true'); button.append(dot); }
      dot.title = 'Update available';
      button.setAttribute('aria-description', 'Update available');
    } else { dot?.remove(); button.removeAttribute('aria-description'); }
  }
  for (const option of document.querySelectorAll<HTMLOptionElement>('[aria-label="Settings machine"] option')) {
    option.textContent = machineName(option.value) + (notices.get(option.value) ? ' · Update available' : '');
  }
}

// Poll only cached status. Git/network checks run on each server's hourly clock.
export function refreshDevelopmentUpdates(force = true): Promise<void> {
  if (pending) return pending;
  if (!force && Date.now() - lastRefresh < 15_000) { renderDevelopmentUpdates(); return Promise.resolve(); }
  lastRefresh = Date.now();
  for (const node of notices.keys()) if (!instances.has(node)) notices.delete(node);
  pending = Promise.all([...instances].map(async ([node, instance]) => {
    if (!instance.online) return;
    try {
      const value = await new InstanceClient(node).json<{ updateAvailable: boolean }>('/development/update-status', 'GET', undefined, AbortSignal.timeout(5000));
      if (instances.has(node)) notices.set(node, !!value.updateAvailable);
    } catch { /* Keep the last known notice through temporary disconnections. */ }
  })).then(() => renderDevelopmentUpdates()).finally(() => { pending = undefined; });
  return pending;
}

export function startDevelopmentUpdates() {
  if (timer !== undefined) return;
  void refreshDevelopmentUpdates();
  timer = window.setInterval(() => void refreshDevelopmentUpdates(), 15_000);
}
