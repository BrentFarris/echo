import { InstanceClient, instances, machineName } from './instances';
import { escapeHTML as esc } from './helpers';
import { compactTokens, exactTokens, setShowTurnStatistics, showTurnStatistics } from './turnStatistics';
import type { UsageTotals } from './types';
import './usage.css';

export type UsageMachine = { nodeId: string; name: string; online: boolean; usage?: UsageTotals; error?: string };
export const usageChanges = new EventTarget();
export function notifyUsageMachinesChanged() { usageChanges.dispatchEvent(new Event('change')); }
export async function fetchUsageMachines(): Promise<UsageMachine[]> {
  return Promise.all([...instances].map(async ([nodeId, instance]) => {
    const machine: UsageMachine = { nodeId, name: machineName(nodeId), online: instance.online };
    if (!machine.online) return { ...machine, error: 'Offline — excluded' };
    try { machine.usage = await new InstanceClient(nodeId).json<UsageTotals>('/usage', 'GET', undefined, AbortSignal.timeout(5000)); }
    catch (error) { machine.error = (error as { status?: number }).status === 404 ? 'Update required — excluded' : 'Unavailable — excluded'; }
    return machine;
  }));
}
export function aggregateUsage(machines: UsageMachine[], direction: 'input' | 'output') {
  const usable = machines.filter(m => m.online && m.usage && !m.error);
  const tokens = direction === 'input' ? 'inputTokens' : 'outputTokens', reports = direction === 'input' ? 'inputReports' : 'outputReports';
  return { total: usable.reduce((sum, m) => sum + m.usage![tokens], 0),
    partial: usable.length < machines.length || usable.some(m => m.usage![reports] < m.usage!.modelCalls),
    available: usable.length > 0 };
}
function breakdown(machines: UsageMachine[], direction: 'input' | 'output') {
  const aggregate = aggregateUsage(machines, direction), tokens = direction === 'input' ? 'inputTokens' : 'outputTokens', reports = direction === 'input' ? 'inputReports' : 'outputReports';
  return `<strong>${aggregate.available ? exactTokens(aggregate.total) : 'Unavailable'} ${direction} tokens${aggregate.partial ? ' (partial)' : ''}</strong>` + machines.map(m => {
    const u = m.online && !m.error ? m.usage : undefined;
    return `<div class="usage-machine"><strong>${esc(m.name)}</strong><span>${u ? `${exactTokens(u[tokens])}${u[reports] < u.modelCalls ? ' (partial)' : ''}` : esc(m.error || 'Offline — excluded')}</span>${u ? `<small>Tracking since ${esc(new Date(u.trackingStartedAt).toLocaleString())}<br>Counting since ${esc(new Date(u.resetAt).toLocaleString())}<br>Reported by ${exactTokens(u[reports])} of ${exactTokens(u.modelCalls)} model calls</small>` : ''}</div>`;
  }).join('');
}

// The overlay is fixed-positioned inside the dialog's top layer. Opening it
// never changes the dimensions of the label, card, or settings dialog.
function wireOverlay(root: HTMLElement, button: HTMLButtonElement, panel: HTMLElement) {
  let pinned = false, dismissed = false;
  const position = () => {
    const rect = button.getBoundingClientRect(), width = Math.min(360, window.innerWidth - 24);
    panel.style.width = `${width}px`;
    panel.style.left = `${Math.max(12, Math.min(rect.left, window.innerWidth - width - 12))}px`;
    panel.style.top = `${Math.max(12, Math.min(rect.bottom + 6, window.innerHeight - Math.min(panel.scrollHeight, window.innerHeight * .55) - 12))}px`;
  };
  const show = () => { if (dismissed) return; panel.hidden = false; button.setAttribute('aria-expanded', 'true'); position(); };
  const hide = () => { panel.hidden = true; button.setAttribute('aria-expanded', 'false'); };
  root.onmouseenter = () => { dismissed = false; show(); };
  root.onmouseleave = () => { if (!pinned && !root.contains(document.activeElement)) hide(); };
  button.onfocus = () => { dismissed = false; show(); };
  root.addEventListener('focusout', e => { if (!pinned && !root.contains(e.relatedTarget as Node)) hide(); });
  button.onclick = () => { pinned = !pinned; dismissed = !pinned; if (pinned) show(); else hide(); };
  root.onkeydown = e => { if (e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); pinned = false; dismissed = true; hide(); button.focus(); dismissed = true; hide(); } };
}

export function mountGeneral(root: HTMLElement, changed: () => void) {
  root.innerHTML = `<section class="general-settings"><h3>General</h3><label class="check"><input type="checkbox" name="showTurnStatistics" ${showTurnStatistics() ? 'checked' : ''}> Show turn statistics</label><p class="subtle">Display a small summary beneath completed agent responses. Applies to this browser; token tracking always continues.</p><h4>Token usage</h4><p class="subtle">Online machines · Provider-reported usage since tracking began or each machine’s last reset. Hover or select a total for details.</p><div class="usage-totals">${(['input', 'output'] as const).map(direction => `<div class="usage-total"><span>${direction === 'input' ? 'Input' : 'Output'} tokens</span><button class="usage-number" data-direction="${direction}" aria-expanded="false" aria-controls="${direction}-usage-details" aria-label="${direction === 'input' ? 'Input' : 'Output'} token details">—</button><div class="usage-overlay" id="${direction}-usage-details" role="region" aria-label="${direction} token breakdown" hidden></div></div>`).join('')}</div><p class="subtle" data-usage-status role="status">Loading totals…</p></section>`;
  root.querySelector<HTMLInputElement>('input')!.onchange = event => { setShowTurnStatistics((event.target as HTMLInputElement).checked); changed(); };
  for (const card of root.querySelectorAll<HTMLElement>('.usage-total')) wireOverlay(card, card.querySelector('button')!, card.querySelector('.usage-overlay')!);
  let fetching = false, pending = false, disposed = false;
  const refresh = async () => {
    if (disposed) return;
    if (fetching) { pending = true; return; }
    fetching = true;
    try {
      const machines = await fetchUsageMachines();
      if (disposed || !root.isConnected) return;
      for (const direction of ['input', 'output'] as const) {
        const result = aggregateUsage(machines, direction);
        root.querySelector(`[data-direction=${direction}]`)!.textContent = result.available ? compactTokens(result.total) : '—';
        root.querySelector(`#${direction}-usage-details`)!.innerHTML = breakdown(machines, direction);
      }
      root.querySelector('[data-usage-status]')!.textContent = machines.some(m => m.error) ? 'Partial total — offline or unavailable machines are excluded.' : aggregateUsage(machines, 'input').partial || aggregateUsage(machines, 'output').partial ? 'Partial total — some model calls did not report token usage.' : 'Totals include chat, inline editing, and context summaries.';
    } finally { fetching = false; if (pending) { pending = false; void refresh(); } }
  };
  const update = () => { void refresh(); };
  const timer = window.setInterval(update, 5000);
  const observer = new MutationObserver(() => {
    if (!root.isConnected || !root.querySelector('.general-settings')) { disposed = true; clearInterval(timer); usageChanges.removeEventListener('change', update); observer.disconnect(); }
  });
  observer.observe(document.body, { childList: true, subtree: true });
  usageChanges.addEventListener('change', update); update();
}

type ResetPlan = { requestId: string; targets: UsageMachine[] };
let pendingReset: ResetPlan | undefined;
export async function resetUsageMachines(plan: ResetPlan) {
  return Promise.all(plan.targets.map(async machine => {
    try { await new InstanceClient(machine.nodeId).json('/usage/reset', 'POST', { requestId: plan.requestId }, AbortSignal.timeout(10000)); return { machine, success: true }; }
    catch (error) { return { machine, success: false, error: error instanceof Error ? error.message : String(error) }; }
  }));
}
export function mountUsageReset(root: HTMLElement, refreshMachines: () => Promise<unknown>) {
  root.innerHTML = '<h4>Token tracking</h4><button class="danger" data-reset-usage>Reset token totals…</button><p class="subtle">Resets reachable machines, regardless of the selected settings machine. Offline machines retain their counts and contribute them again when they reconnect. Chat statistics are preserved.</p><p data-reset-status role="status" aria-live="polite"></p>';
  const button = root.querySelector<HTMLButtonElement>('button')!, status = root.querySelector<HTMLElement>('[data-reset-status]')!;
  const label = () => { button.textContent = pendingReset ? 'Retry unsuccessful resets…' : 'Reset token totals…'; };
  label();
  button.onclick = async () => {
    button.disabled = true;
    try {
      await refreshMachines();
      const machines = await fetchUsageMachines();
      const targets = pendingReset?.targets || machines.filter(m => m.online && m.usage && !m.error);
      const skipped = machines.filter(m => !targets.some(t => t.nodeId === m.nodeId));
      if (!targets.length) { status.textContent = 'No machines are available to reset.'; return; }
      if (!window.confirm(`${pendingReset ? 'Retry resetting' : 'Reset'} token totals on these machines?\n\n${targets.map(m => m.name).join('\n')}${skipped.length ? '\n\nSkipped (counts retained):\n' + skipped.map(m => m.name).join('\n') : ''}\n\nHistorical chat statistics will remain unchanged.`)) return;
      const plan = pendingReset || { requestId: crypto.randomUUID(), targets };
      pendingReset = plan;
      const results = await resetUsageMachines(plan);
      const failed = results.filter(r => !r.success);
      pendingReset = failed.length ? { requestId: plan.requestId, targets: failed.map(r => r.machine) } : undefined;
      status.textContent = results.map(r => `${r.machine.name}: ${r.success ? 'reset' : `failed — ${r.error}`}`).concat(skipped.map(m => `${m.name}: skipped; counts retained`)).join('\n');
      notifyUsageMachinesChanged();
    } catch (error) { status.textContent = error instanceof Error ? error.message : String(error); }
    finally { button.disabled = false; label(); }
  };
}
