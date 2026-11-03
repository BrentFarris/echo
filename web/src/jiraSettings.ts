import type { Plugin } from './types';
import { escapeHTML as esc } from './helpers';

type API = <T = any>(path: string, method?: string, body?: unknown) => Promise<T>;

export function mountJiraSettings(root: HTMLElement, plugin: Plugin, api: API, changed: () => Promise<void>, notice: (value: unknown, error?: boolean) => void) {
  root.innerHTML = `<form class="form" data-jira-settings>
    <label class="check"><input name="enabled" type="checkbox" ${plugin.enabled ? 'checked' : ''}>Enable Jira</label>
    <label>Deployment type<select name="deployment"><option value="cloud">Jira Cloud</option><option value="datacenter">Jira Data Center</option></select></label>
    <label>Jira site URL<input name="url" type="url" value="${esc(plugin.config.url || '')}" placeholder="https://example.atlassian.net"></label>
    <label data-cloud>Email<input name="email" type="email" value="${esc(plugin.config.email || '')}"></label>
    <label data-cloud>Cloud ID (scoped tokens)<input name="cloudId" value="${esc(plugin.config.cloudId || '')}"></label>
    <button class="primary">Save settings</button>
    <small>One connection is shared on this machine. Use each bot's Jira switch to control access. Changing the connection clears its saved token. Cloud requires HTTPS; Data Center also supports an explicitly configured HTTP URL.</small>
  </form>
  <hr><form class="form" data-jira-credential><label>API token / personal access token<input name="secret" type="password" autocomplete="new-password" required maxlength="16384"></label>
    <p data-jira-status aria-live="polite">${plugin.config.credential ? 'Credential is saved' : 'No credential saved'}</p>
    <div class="button-row"><button class="primary">Save token</button><button type="button" data-jira-clear>Clear credential</button><button type="button" data-jira-test>Test connection</button></div>
    <small>Save the connection settings first. Cloud uses your account email and API token; scoped tokens also require a Cloud ID. Data Center uses a personal access token. Tokens stay in the host credential vault.</small>
  </form><p data-jira-identity aria-live="polite"></p>
  <p class="subtle">Bots can search with JQL and read fields, discussions, links and attachment metadata. Jira permissions apply. Example: “Search Jira for project = APP AND statusCategory != Done, then read APP-123 including its full discussion.”</p>`;
  const form = root.querySelector<HTMLFormElement>('[data-jira-settings]')!;
  const deployment = form.elements.namedItem('deployment') as HTMLSelectElement;
  deployment.value = String(plugin.config.deployment || 'cloud');
  const showCloud = () => { for (const label of root.querySelectorAll<HTMLElement>('[data-cloud]')) label.hidden = deployment.value !== 'cloud'; };
  deployment.onchange = showCloud; showCloud();
  const run = async (operation: () => Promise<void>) => {
    const buttons = [...root.querySelectorAll<HTMLButtonElement>('button')];
    buttons.forEach(button => { button.disabled = true; });
    try { await operation(); } catch (error) { notice(error, true); }
    finally { buttons.forEach(button => { button.disabled = false; }); }
  };
  form.onsubmit = event => {
    event.preventDefault();
    void run(async () => {
      const data = new FormData(form), cloud = deployment.value === 'cloud';
      await api('/plugins/jira', 'PATCH', { enabled: data.has('enabled'), config: { deployment: deployment.value, url: String(data.get('url')).trim(), email: cloud ? String(data.get('email')).trim() : '', cloudId: cloud ? String(data.get('cloudId')).trim() : '' } });
      await changed(); notice('Jira settings saved');
    });
  };
  const secretForm = root.querySelector<HTMLFormElement>('[data-jira-credential]')!;
  const status = root.querySelector<HTMLElement>('[data-jira-status]')!;
  const identity = root.querySelector<HTMLElement>('[data-jira-identity]')!;
  secretForm.onsubmit = event => {
    event.preventDefault();
    // Clear the password input before starting asynchronous work, including failures.
    const secret = String(new FormData(secretForm).get('secret')); secretForm.reset();
    void run(async () => { await api('/plugins/jira/credential', 'POST', { secret }); status.textContent = 'Credential is saved'; identity.textContent = ''; await changed(); notice('Jira token saved'); });
  };
  root.querySelector<HTMLButtonElement>('[data-jira-clear]')!.onclick = () => { void run(async () => { await api('/plugins/jira/credential', 'POST', { secret: '' }); secretForm.reset(); status.textContent = 'No credential saved'; identity.textContent = ''; await changed(); notice('Jira credential cleared'); }); };
  root.querySelector<HTMLButtonElement>('[data-jira-test]')!.onclick = () => { void run(async () => {
    identity.textContent = 'Testing saved connection…';
    try {
      const result = await api<{ displayName?: string; accountId?: string; name?: string }>('/plugins/jira/test', 'POST', {});
      identity.textContent = `Connected as ${result.displayName || result.name || result.accountId || 'Jira user'}`;
    } catch (error) { identity.textContent = 'Connection test failed. Check the saved settings and token.'; throw error; }
  }); };
}
