import { escapeHTML as esc } from './helpers';
import type { Endpoint } from './types';

export const externalAgentNotice = 'External agent · Full access. Echo folder restrictions and plugin permissions do not apply.';
export function isExternalAgent(endpoint?: Pick<Endpoint, 'kind'>): boolean {
  return endpoint?.kind === 'codex-cli' || endpoint?.kind === 'claude-code-cli';
}
export function externalAgentForm(endpoint?: Endpoint): string {
  return `<form class="form external-agent-form">
    <p class="subtle">${externalAgentNotice}</p>
    <label>Name<input name="name" required value="${esc(endpoint?.name || '')}" placeholder="Codex"></label>
    <label>CLI<select name="kind" aria-label="CLI"><option value="codex-cli" ${endpoint?.kind !== 'claude-code-cli' ? 'selected' : ''}>Codex</option><option value="claude-code-cli" ${endpoint?.kind === 'claude-code-cli' ? 'selected' : ''}>Claude Code</option></select></label>
    <label>Executable path<input name="executable" value="${esc(endpoint?.executable || '')}" placeholder="Discover on PATH"><small>Optional absolute path to the native CLI executable on this machine.</small></label>
    <label>Starting directory<input name="workingDirectory" value="${esc(endpoint?.workingDirectory || '')}" placeholder="Bot’s first folder, otherwise your home folder"><small>This is a starting location, not an access restriction.</small></label>
    <label>Model override<input name="model" value="${esc(endpoint?.model || '')}" placeholder="CLI default"></label>
    <p class="subtle">Install and sign in using <code>codex login</code> or <code>claude auth login</code> under the account running Echo. Your CLI’s tools, plugins and project configuration apply. Subscription usage limits still apply.</p>
    <button class="primary">Save connection</button>
  </form>`;
}
export function externalAgentValues(form: HTMLFormElement): Partial<Endpoint> {
  const data = new FormData(form);
  return { kind: data.get('kind') as Endpoint['kind'], name: String(data.get('name') || '').trim(), executable: String(data.get('executable') || '').trim(), workingDirectory: String(data.get('workingDirectory') || '').trim(), model: String(data.get('model') || '').trim(), url: '', cloud: false, compact: false, contextTokens: 0 };
}
