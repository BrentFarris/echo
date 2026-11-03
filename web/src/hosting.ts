import { escapeHTML as esc } from './helpers';

type DeviceSession = {
  id: string; userAgent: string; address: string; createdAt: string;
  lastSeen: string; expiresAt: string; current?: boolean; connected?: boolean;
};
type API = <T = unknown>(path: string, method?: string, body?: unknown) => Promise<T>;

function deviceName(agent: string): string {
  const browser = /Edg\//.test(agent) ? 'Edge' : /Firefox\/|FxiOS\//.test(agent) ? 'Firefox' : /Chrome\/|CriOS\//.test(agent) ? 'Chrome' : /Safari\//.test(agent) ? 'Safari' : 'Browser';
  const system = /Android/.test(agent) ? 'Android' : /iPhone/.test(agent) ? 'iPhone' : /iPad/.test(agent) ? 'iPad' : /Windows/.test(agent) ? 'Windows' : /Macintosh|Mac OS/.test(agent) ? 'Mac' : /Linux/.test(agent) ? 'Linux' : '';
  return agent ? `${browser}${system ? ` on ${system}` : ''}` : 'Previously connected device';
}

function date(value: string): string {
  return !value || value.startsWith('0001-') ? 'Unknown' : new Date(value).toLocaleString();
}

export function mountHosting(root: HTMLElement, api: API, notice: (value: unknown, error?: boolean) => void) {
  root.innerHTML = `<hr><form class="form" id="password-form"><label>Remote access password<input name="password" type="password" minlength="12" maxlength="72" required autocomplete="new-password"></label><button>Set password</button><small>A password is required before remote hosting can start. Changing it signs out other devices. TLS certificate and key fields enable HTTPS.</small></form>
    <hr><h3>Connected devices</h3><p class="subtle">Browsers stay signed in for 30 days after their last launch. Closing a session signs that browser out. Local access on the server can sign in automatically.</p><button type="button" data-refresh-sessions>Refresh devices</button><div data-sessions aria-live="polite"></div>`;
  const list = root.querySelector<HTMLElement>('[data-sessions]')!;
  let version = 0;
  const refresh = async () => {
    const request = ++version;
    try {
      const sessions = await api<DeviceSession[]>('/hosting/sessions');
      if (!root.isConnected || request !== version) return;
      list.innerHTML = sessions.length ? sessions.map(session => `<div class="list-card"><div class="grow"><strong>${esc(deviceName(session.userAgent))}${session.current ? ' · This device' : ''}</strong><p>${esc(session.address || 'Address unavailable')} · ${session.connected ? 'Connected now' : 'Saved session'}</p><small>Signed in: ${esc(date(session.createdAt))}<br>Last active: ${esc(date(session.lastSeen))}<br>Expires: ${esc(date(session.expiresAt))}</small><details><summary>Browser details</summary><p>${esc(session.userAgent || 'Unavailable for older sessions')}</p></details></div><button type="button" class="danger" data-close-session="${esc(session.id)}">Close session</button></div>`).join('') : '<p class="subtle">No saved sessions.</p>';
      for (const button of list.querySelectorAll<HTMLButtonElement>('[data-close-session]')) {
        button.onclick = async () => {
          button.disabled = true;
          try {
            const id = button.dataset.closeSession!;
            await api(`/hosting/sessions/${encodeURIComponent(id)}`, 'DELETE');
            if (sessions.find(session => session.id === id)?.current) { location.reload(); return; }
            notice('Session closed');
            await refresh();
          } catch (error) { notice(error, true); button.disabled = false; }
        };
      }
    } catch (error) {
      if (request !== version || !root.isConnected) return;
      list.textContent = 'Could not load connected devices. Use Refresh devices to try again.';
      notice(error, true);
    }
  };
  root.querySelector<HTMLButtonElement>('[data-refresh-sessions]')!.onclick = () => { void refresh(); };
  const form = root.querySelector<HTMLFormElement>('#password-form')!;
  form.onsubmit = async event => {
    event.preventDefault();
    const button = form.querySelector<HTMLButtonElement>('button')!;
    button.disabled = true;
    try {
      await api('/hosting/password', 'POST', { password: new FormData(form).get('password') });
      form.reset(); notice('Password saved. Other devices have been signed out.'); await refresh();
    } catch (error) { notice(error, true); }
    finally { button.disabled = false; }
  };
  void refresh();
}
