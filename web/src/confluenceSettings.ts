import type { Plugin } from './types';
import { escapeHTML as esc } from './helpers';

type API = <T = any>(path: string, method?: string, body?: unknown) => Promise<T>;

export function mountConfluenceSettings(root: HTMLElement, plugin: Plugin, api: API, changed: () => Promise<void>, notice: (value: unknown, error?: boolean) => void) {
  root.innerHTML = `<form class="form" data-confluence-settings>
    <label class="check"><input name="enabled" type="checkbox" ${plugin.enabled ? 'checked' : ''}>Enable Confluence</label>
    <label>Deployment type<select name="deployment"><option value="cloud">Confluence Cloud</option><option value="datacenter">Confluence Data Center</option></select></label>
    <label>Confluence site/base URL<input name="url" type="url" value="${esc(plugin.config.url || '')}" placeholder="https://example.atlassian.net"></label>
    <label data-cloud>Cloud account email<input name="email" type="email" value="${esc(plugin.config.email || '')}"></label>
    <label data-cloud>Cloud token type<select name="tokenType"><option value="classic">Classic API token</option><option value="scoped">Scoped API token</option></select></label>
    <label data-scoped>Cloud ID<input name="cloudId" value="${esc(plugin.config.cloudId || '')}"></label>
    <button class="primary">Save settings</button>
    <small>One connection is shared on this machine. Control access with each bot's Confluence switch. Changing the connection clears its saved token. Cloud requires HTTPS; Data Center supports a configured HTTP(S) base, including paths such as /confluence.</small>
  </form><hr>
  <form class="form" data-confluence-credential>
    <label>API token / personal access token<input name="secret" type="password" autocomplete="new-password" required maxlength="16384"></label>
    <p data-confluence-status aria-live="polite">${plugin.config.credential ? 'Token is saved' : 'No token saved'}</p>
    <div class="button-row"><button class="primary">Save token</button><button type="button" data-confluence-clear>Clear token</button><button type="button" data-confluence-test>Test connection</button></div>
    <small>Save settings first. Cloud uses your email and API token; scoped tokens also require a Cloud ID. Data Center uses a personal access token. Tokens stay in the host credential vault.</small>
  </form><p data-confluence-result aria-live="polite"></p>
  <p class="subtle">Ask “Find the last three pages Brent Farris authored.” Bots can find people by visible full/public name and use their returned ID to search authored pages. Cloud email-to-ID lookup is unavailable. The current-user tool identifies the saved token account, which may be a service account.</p>
  <p class="subtle">Bots can search with CQL and read full pages, blog posts and comments as Markdown or storage markup. Attachments provide metadata and links. Example: “Search Confluence for type = page AND space = ENG, then read the full onboarding page.” Confluence account permissions and token read scopes apply.</p>`;
  const form = root.querySelector<HTMLFormElement>('[data-confluence-settings]')!;
  const deployment = form.elements.namedItem('deployment') as HTMLSelectElement;
  const tokenType = form.elements.namedItem('tokenType') as HTMLSelectElement;
  deployment.value = String(plugin.config.deployment || 'cloud'); tokenType.value = String(plugin.config.tokenType || 'classic');
  const showFields = () => {
    const cloud = deployment.value === 'cloud';
    for (const label of root.querySelectorAll<HTMLElement>('[data-cloud]')) { label.hidden = !cloud; label.style.display = cloud ? '' : 'none'; }
    const scoped = root.querySelector<HTMLElement>('[data-scoped]')!;
    scoped.hidden = !cloud || tokenType.value !== 'scoped'; scoped.style.display = scoped.hidden ? 'none' : '';
  };
  deployment.onchange = showFields; tokenType.onchange = showFields; showFields();
  const run = async (operation: () => Promise<void>) => {
    const buttons = [...root.querySelectorAll<HTMLButtonElement>('button')]; buttons.forEach(button => { button.disabled = true; });
    try { await operation(); } catch (error) { notice(error, true); } finally { buttons.forEach(button => { button.disabled = false; }); }
  };
  form.onsubmit = event => {
    event.preventDefault();
    void run(async () => {
      const data = new FormData(form), cloud = deployment.value === 'cloud', scoped = cloud && tokenType.value === 'scoped';
      await api('/plugins/confluence', 'PATCH', { enabled: data.has('enabled'), config: { deployment: deployment.value, url: String(data.get('url')).trim(), email: cloud ? String(data.get('email')).trim() : '', tokenType: cloud ? tokenType.value : 'classic', cloudId: scoped ? String(data.get('cloudId')).trim() : '' } });
      await changed(); notice('Confluence settings saved');
    });
  };
  const secretForm = root.querySelector<HTMLFormElement>('[data-confluence-credential]')!;
  const status = root.querySelector<HTMLElement>('[data-confluence-status]')!;
  const result = root.querySelector<HTMLElement>('[data-confluence-result]')!;
  secretForm.onsubmit = event => {
    event.preventDefault(); const secret = String(new FormData(secretForm).get('secret')); secretForm.reset();
    void run(async () => { await api('/plugins/confluence/credential', 'POST', { secret }); status.textContent = 'Token is saved'; result.textContent = ''; await changed(); notice('Confluence token saved'); });
  };
  root.querySelector<HTMLButtonElement>('[data-confluence-clear]')!.onclick = () => { void run(async () => { await api('/plugins/confluence/credential', 'POST', { secret: '' }); secretForm.reset(); status.textContent = 'No token saved'; result.textContent = ''; await changed(); notice('Confluence token cleared'); }); };
  root.querySelector<HTMLButtonElement>('[data-confluence-test]')!.onclick = () => { void run(async () => {
    result.textContent = 'Testing saved connection…';
    try { await api('/plugins/confluence/test', 'POST', {}); result.textContent = 'Connected to Confluence. CQL search is available.'; }
    catch (error) { result.textContent = 'Connection test failed. Check the saved settings, plugin enablement and token.'; throw error; }
  }); };
}
