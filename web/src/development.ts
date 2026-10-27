import { InstanceClient, entryNode } from './instances';
import { escapeHTML as esc } from './helpers';
import './development.css';
import { mountRollouts } from './developmentRollouts';

type API = <T = any>(path: string, method?: string, body?: unknown) => Promise<T>;
export interface DevelopmentOperation {
  id: string; action: string; status: 'running' | 'succeeded' | 'failed' | 'restarting' | 'stopping';
  stage: string; error?: string; logPath: string; logTail: string; checkoutUpdated: boolean;
}
interface DevelopmentState {
  repositoryPath: string; suggestedPath: string; repositoryValid: boolean; repositoryError?: string;
  branch: string; upstream: string; updateError?: string; instanceId: string;
  comparison?: { state: string; ahead: number; behind: number };
  operation?: DevelopmentOperation;
}

const delay = (ms: number) => new Promise<void>(resolve => window.setTimeout(resolve, ms));

export async function waitForReplacementServer(previous: string, options: {
  fetcher?: typeof fetch; delay?: (ms: number) => Promise<void>; timeoutMs?: number;
} = {}): Promise<void> {
  const fetcher = options.fetcher || window.fetch.bind(window);
  const deadline = Date.now() + (options.timeoutMs ?? 120_000);
  while (Date.now() < deadline) {
    try {
      const response = await fetcher('/api/v1/health', { cache: 'no-store', signal: AbortSignal.timeout(3000) });
      if (response.ok) {
        const health = await response.json();
        if (health.instanceId && health.instanceId !== previous) return;
      }
    } catch { /* A connection outage is expected during replacement. */ }
    await (options.delay || delay)(500);
  }
  throw new Error('Echo did not return within two minutes. Check the operation and launcher logs in the server’s data directory, then start Echo manually if needed.');
}

const controllers = new Map<string, ReturnType<typeof createDevelopment>>();
export async function mountDevelopment(target: HTMLElement, api: API, client = new InstanceClient(), changed: () => Promise<void> = async () => {}) {
  let controller = controllers.get(client.nodeId); if (!controller) { controller = createDevelopment(client, changed); controllers.set(client.nodeId, controller); }
  await controller.mount(target, api);
}
function createDevelopment(client: InstanceClient, changed: () => Promise<void>) {
const replaced = async () => { if (!client.nodeId || client.nodeId === entryNode) window.location.reload(); else { stopped = false; current = await client.json('/development'); showMessage('Remote Echo reconnected.'); await changed(); } };
let current: DevelopmentState | undefined;
let root: HTMLElement | undefined;
let monitoring: string | undefined;
let localBusy = false;
let message = '';
let messageError = false;
let stopped = false;
let rolloutBusy = false;
let rolloutControls: Awaited<ReturnType<typeof mountRollouts>> | undefined;

function showMessage(text: string, error = false) { message = text; messageError = error; update(); }
function isBusy() { return rolloutBusy || localBusy || stopped || !!current?.operation && ['running', 'restarting', 'stopping'].includes(current.operation.status); }

function update() {
  if (!root?.isConnected || !current) return;
  const input = root.querySelector<HTMLInputElement>('[name=repositoryPath]');
  if (!input) return;
  const dirty = input.value.trim() !== current.repositoryPath;
  const busy = isBusy();
  input.disabled = busy;
  root.querySelector<HTMLButtonElement>('[data-save]')!.disabled = busy;
  for (const button of root.querySelectorAll<HTMLButtonElement>('[data-development-action]')) {
    const action = button.dataset.developmentAction!;
    const needsSource = !['relaunch', 'terminate'].includes(action);
    button.disabled = busy || needsSource && (!current.repositoryValid || dirty) || ['check-updates', 'update-relaunch'].includes(action) && !!current.updateError;
    button.setAttribute('aria-busy', String(busy));
  }
  rolloutControls?.setUnavailable(localBusy || stopped || current.operation && ['running', 'restarting', 'stopping'].includes(current.operation.status) ? 'Wait for the current development operation to finish.' : !current.repositoryValid || dirty ? 'Save a valid Echo repository path on the selected machine first.' : current.updateError || '');
  root.querySelector('[data-repository-status]')!.textContent = current.repositoryError || (dirty ? 'Save this path before building or checking for updates.' : !current.repositoryPath ? 'Save an Echo checkout to enable builds and updates.' : 'Repository saved.');
  root.querySelector('[data-tracking]')!.textContent = [current.branch && `Branch: ${current.branch}`, current.upstream && `Upstream: ${current.upstream}`, current.updateError].filter(Boolean).join(' · ');
  const comparison = current.comparison;
  root.querySelector('[data-comparison]')!.textContent = comparison ? `${comparison.state === 'up-to-date' ? 'Up to date' : comparison.state === 'behind' ? 'Update available' : comparison.state === 'ahead' ? 'Local branch is ahead' : 'Branches have diverged'} (${comparison.ahead} ahead, ${comparison.behind} behind).${comparison.state === 'diverged' ? ' Resolve the divergence in Git before updating.' : ''}` : '';
  const op = current.operation;
  const status = root.querySelector<HTMLElement>('[data-development-status]')!;
  status.textContent = message || op?.error || op?.stage || '';
  status.classList.toggle('error', messageError || !message && op?.status === 'failed');
  root.querySelector<HTMLDetailsElement>('[data-development-logs]')!.hidden = !op;
  root.querySelector('[data-log-path]')!.textContent = op ? `Server log: ${op.logPath}` : '';
  root.querySelector('[data-log-tail]')!.textContent = op?.logTail || '';
}

async function monitor(api: API, operation: DevelopmentOperation, instance: string) {
  if (monitoring === operation.id) return;
  monitoring = operation.id;
  try {
    let op = operation;
    let disconnectedAt = 0;
    while (true) {
      if (current) current.operation = op;
      update();
      if (op.status === 'failed' || op.status === 'succeeded') {
        current = await api<DevelopmentState>('/development');
        update(); return;
      }
      if (op.status === 'stopping') {
        stopped = true; showMessage('Echo is shutting down. You can close this browser tab.'); return;
      }
      if (op.status === 'restarting') break;
      await delay(500);
      try {
        op = await api<DevelopmentOperation>(`/development/operations/${op.id}`);
        if (disconnectedAt) { disconnectedAt = 0; message = ''; messageError = false; }
      } catch {
        if (op.action === 'terminate') { stopped = true; showMessage('Echo is shutting down. You can close this browser tab.'); return; }
        // Recover a transient outage while building, and detect a replacement
        // even if the old process exited before we saw its restarting status.
        try {
          const response = await client.fetch('/health', { cache: 'no-store', signal: AbortSignal.timeout(3000) });
          const health = response.ok ? await response.json() : null;
          if (health?.instanceId && health.instanceId !== instance) { await replaced(); return; }
        } catch { /* Keep retrying the operation while disconnected. */ }
        disconnectedAt ||= Date.now();
        if (Date.now() - disconnectedAt >= 120_000) throw new Error('Cannot reconnect to Echo. Check the operation and launcher logs on the server, then reopen Development settings to recover its status.');
        showMessage('Connection lost; waiting for Echo…');
      }
    }
    showMessage('Waiting for Echo to restart…');
    await waitForReplacementServer(instance, { fetcher: ((path, options) => client.fetch(String(path), options)) as typeof fetch });
    await replaced();
  } catch (error) {
    showMessage(error instanceof Error ? error.message : String(error), true);
    if (current?.operation) current.operation = { ...current.operation, status: 'failed', error: message };
  } finally { monitoring = undefined; update(); }
}

async function mount(target: HTMLElement, api: API) {
  root = target;
  target.innerHTML = '<p role="status">Loading Development settings…</p>';
  try {
    const state = await api<DevelopmentState>('/development');
    if (!target.isConnected) return;
    current = state;
    if (!monitoring) { message = ''; messageError = false; }
    target.innerHTML = `<section class="development-settings"><h3>Development</h3>
      <form class="form"><label>Echo Git repository path<input name="repositoryPath" type="text" spellcheck="false" value="${esc(state.repositoryPath || state.suggestedPath)}" placeholder="C:\\path\\to\\echo"></label>
      <p class="subtle">Enter a path on the computer running Echo, including when using a remote browser.</p><button data-save class="primary">Save repository path</button></form>
      <p class="subtle" data-repository-status></p><p class="subtle" data-tracking></p>
      <div data-rollout-controls></div><p data-comparison></p>
      <div class="button-row"><button data-development-action="build-relaunch">Build &amp; Relaunch</button><button data-development-action="relaunch">Relaunch</button><button class="danger" data-development-action="terminate">Terminate Echo</button></div>
      <p class="subtle">These three controls affect only the selected machine. Restarting and stopping with these controls interrupt active work.</p>
      <p role="status" aria-live="polite" data-development-status></p><details data-development-logs hidden><summary>Operation log</summary><p data-log-path></p><pre data-log-tail></pre></details></section>`;
    target.querySelector<HTMLInputElement>('[name=repositoryPath]')!.oninput = update;
    target.querySelector('form')!.onsubmit = event => {
      event.preventDefault();
      const repositoryPath = target.querySelector<HTMLInputElement>('[name=repositoryPath]')!.value.trim();
      localBusy = true; showMessage('Saving repository path…');
      void api<DevelopmentState>('/development', 'PUT', { repositoryPath }).then(state => {
        current = state; target.querySelector<HTMLInputElement>('[name=repositoryPath]')!.value = state.repositoryPath; showMessage('Repository path saved.');
      }).catch(error => showMessage(error.message, true)).finally(() => { localBusy = false; update(); });
    };
    const confirmations: Record<string, string> = {
      'update-relaunch': 'Update and relaunch Echo? This will fast-forward the configured upstream, build Echo, and interrupt active work when restarting. Conflicting local edits will stop the update.',
      'build-relaunch': 'Build and relaunch Echo? This builds your current checkout, including local edits, and interrupts active work when restarting.',
      relaunch: 'Relaunch Echo? This restarts the current executable without building and interrupts active work.',
      terminate: 'Terminate Echo? This interrupts active work and stops Echo without restarting it.',
    };
    for (const button of target.querySelectorAll<HTMLButtonElement>('[data-development-action]')) button.onclick = () => {
      const action = button.dataset.developmentAction!;
      if (confirmations[action] && !window.confirm(confirmations[action])) return;
      localBusy = true; showMessage('Starting…');
      void api<{ operation: DevelopmentOperation; instanceId: string }>(`/development/${action}`, 'POST', {}).then(result => {
        message = ''; messageError = false;
        if (current) { current.operation = result.operation; if (action === 'check-updates' || action === 'update-relaunch') current.comparison = undefined; }
        void monitor(api, result.operation, result.instanceId);
      }).catch(error => showMessage(error.message, true)).finally(() => { localBusy = false; update(); });
    };
    update();
    rolloutControls = await mountRollouts(target.querySelector<HTMLElement>('[data-rollout-controls]')!, client, busy => { rolloutBusy = busy; update(); }, async () => { current = await api<DevelopmentState>('/development'); await changed(); update(); });
    update();
    if (state.operation && ['running', 'restarting', 'stopping'].includes(state.operation.status)) void monitor(api, state.operation, state.instanceId);
  } catch (error) { target.innerHTML = `<p role="alert" class="error">${esc(error instanceof Error ? error.message : String(error))}</p>`; }
}

return { mount };
}
