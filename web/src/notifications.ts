import { InstanceClient } from './instances';
import { notificationRoute } from './notificationRoutes';

export type ChatAlert = {
  id: string; kind: 'completion' | 'question'; homeNodeId: string; chatId: string;
  botId: string; messageId: string; occurredAt: string; title: string; preview: string; active: boolean;
};
export type NotificationPreferences = { completion: boolean; questions: boolean; completionSound: boolean; questionSound: boolean };
export const defaultNotificationPreferences: NotificationPreferences = { completion: true, questions: true, completionSound: true, questionSound: true };
export const soundFiles = { completion: '/sounds/completion.wav', question: '/sounds/question.wav' };
export function popupEnabled(p: NotificationPreferences, kind: ChatAlert['kind']) { return kind === 'question' ? p.questions : p.completion; }
export function soundEnabled(p: NotificationPreferences, kind: ChatAlert['kind']) { return kind === 'question' ? p.questionSound : p.completionSound; }
const deviceKey = 'echo-notification-device', preferencesKey = 'echo-notification-preferences';
function readPreferences(): NotificationPreferences {
  try { return { ...defaultNotificationPreferences, ...JSON.parse(localStorage.getItem(preferencesKey) || '{}') }; } catch { return { ...defaultNotificationPreferences }; }
}
function savedDevice(): string { try { return localStorage.getItem(deviceKey) || ''; } catch { return ''; } }
function remember(key: string, value: string) { try { localStorage.setItem(key, value); } catch { /* Session-only on storage-restricted browsers. */ } }
export function notificationPermission(): NotificationPermission | 'unsupported' {
  return typeof Notification === 'undefined' || !window.isSecureContext ? 'unsupported' : Notification.permission;
}

export class ChatNotifications {
  private client = new InstanceClient(''); // Device registrations never follow the settings machine selector.
  private preferences = readPreferences();
  private device = '';
  private push = false;
  private started = false;
  private polling = false;
  private processing = new Set<string>();
  private sounds: Partial<Record<ChatAlert['kind'], HTMLAudioElement>> = {};
  private notices = new Map<string, HTMLElement>();
  constructor(private options: { viewing: (alert: ChatAlert) => boolean; navigate: (hash: string) => void; notice: (message: string, error?: boolean) => void }) {
    const unlock = () => {
      for (const kind of ['completion', 'question'] as const) {
        const audio = this.audio(kind); if (!audio) continue;
        audio.muted = true;
        void audio.play().then(() => { audio.pause(); audio.currentTime = 0; audio.muted = false; }).catch(() => { audio.muted = false; });
      }
    };
    document.addEventListener('pointerdown', unlock, { once: true });
    document.addEventListener('keydown', unlock, { once: true });
    document.addEventListener('visibilitychange', () => { if (!document.hidden) void this.poll(); });
    window.addEventListener('focus', () => void this.poll());
    window.addEventListener('storage', e => { if (e.key === preferencesKey) this.preferences = readPreferences(); });
    navigator.serviceWorker?.addEventListener('message', event => {
      if (event.data?.type === 'echo-notification-click' && typeof event.data.url === 'string') {
        const url = new URL(event.data.url, location.origin);
        if (url.origin === location.origin && url.pathname === '/') { event.ports[0]?.postMessage(true); this.options.navigate(url.hash); }
      }
      if (event.data?.type === 'echo-notification-sound') {
        const alert = event.data.alert as ChatAlert;
        void this.play(alert.kind).then(played => event.ports[0]?.postMessage(played));
      }
    });
  }
  private audio(kind: ChatAlert['kind']) {
    if (!this.sounds[kind] && typeof Audio === 'function') { const audio = new Audio(soundFiles[kind]); audio.preload = 'auto'; this.sounds[kind] = audio; }
    return this.sounds[kind];
  }
  async play(kind: ChatAlert['kind']): Promise<boolean> {
    if (!soundEnabled(this.preferences, kind)) return false;
    const audio = this.audio(kind); if (!audio) return false;
    try { audio.muted = false; audio.currentTime = 0; await audio.play(); return true; } catch { return false; }
  }
  private async save(subscription?: PushSubscription | null) {
    const result = await this.client.json<{ id: string; push: boolean; preferences: NotificationPreferences }>('/notifications/devices', 'POST', {
      id: this.device || savedDevice(), preferences: this.preferences, ...(subscription === undefined ? {} : { subscription: subscription?.toJSON() ?? null })
    });
    this.device = result.id; this.push = result.push; remember(deviceKey, this.device);
  }
  async start() {
    if (this.started) return; this.started = true;
    const register = async () => {
      await this.save();
      if (notificationPermission() !== 'granted' && this.push) await this.save(null);
      if (notificationPermission() === 'granted' && 'serviceWorker' in navigator) {
        const registration = await navigator.serviceWorker.getRegistration('/');
        const subscription = await registration?.pushManager?.getSubscription();
        if (subscription) await this.save(subscription);
        else if (this.push && registration) await this.save(null);
      }
    };
    try { if (navigator.locks) await navigator.locks.request('echo-notification-registration', register); else await register(); }
    catch { this.options.notice('Notifications could not connect. They will retry while Echo is open.', true); }
    void this.poll(); window.setInterval(() => void this.poll(), 1000);
  }
  receive(alert: ChatAlert) {
    if (!alert.active) { this.notices.get(alert.id)?.remove(); this.notices.delete(alert.id); return; }
    void this.deliver(alert);
  }
  private async poll() {
    if (!this.started || this.polling) return; this.polling = true;
    try {
      if (!this.device) await this.save();
      const alerts = await this.client.json<ChatAlert[]>(`/notifications/devices/${this.device}/pending`);
      for (const alert of alerts) await this.deliver(alert);
    } catch (error) {
      if ((error as { status?: number }).status === 404) this.device = '';
    } finally { this.polling = false; }
  }
  private async deliver(alert: ChatAlert) {
    if (!this.device || !alert.active || this.processing.has(alert.id)) return;
    // A suspended/hidden push-enabled client leaves delivery to the service worker.
    if (document.hidden && this.push && popupEnabled(this.preferences, alert.kind)) return;
    this.processing.add(alert.id);
    try {
      // Give a focused Echo window first choice when several windows receive
      // the event together; its viewing check decides whether to suppress it.
      if (!document.hasFocus()) await new Promise(resolve => setTimeout(resolve, 250));
      const { claimed } = await this.client.json<{ claimed: boolean }>(`/notifications/devices/${this.device}/claim`, 'POST', { alert: alert.id });
      if (!claimed) return;
      const played = await this.play(alert.kind);
      if (!popupEnabled(this.preferences, alert.kind) || this.options.viewing(alert)) return;
      if ((!document.hasFocus() || document.hidden) && notificationPermission() === 'granted') {
        const registration = await navigator.serviceWorker?.getRegistration('/');
        if (registration) {
          await registration.showNotification(alert.title, { body: alert.preview, tag: alert.id, icon: '/icons/echo-192.png', silent: played || !soundEnabled(this.preferences, alert.kind), data: { url: '/' + notificationRoute(alert.homeNodeId, alert) } });
          return;
        }
        // Development mode intentionally has no service worker.
        try { const notification = new Notification(alert.title, { body: alert.preview, tag: alert.id, silent: played }); notification.onclick = () => { notification.close(); window.focus(); this.options.navigate(notificationRoute(alert.homeNodeId, alert)); }; return; } catch { /* Mobile requires a service worker. */ }
      }
      this.toast(alert);
    } catch { /* A missed claim remains pending for the next poll or push. */ }
    finally { this.processing.delete(alert.id); }
  }
  private toast(alert: ChatAlert) {
    let container = document.querySelector<HTMLElement>('.chat-notifications');
    if (!container) { container = document.createElement('div'); container.className = 'chat-notifications'; container.setAttribute('aria-live', 'polite'); document.body.append(container); }
    const item = document.createElement('div'); item.className = 'chat-notification';
    const open = document.createElement('button'); open.className = 'chat-notification-open';
    const title = document.createElement('strong'); title.textContent = alert.title;
    const body = document.createElement('span'); body.textContent = alert.preview; open.append(title, body);
    const close = document.createElement('button'); close.textContent = '×'; close.setAttribute('aria-label', 'Dismiss notification');
    const remove = () => { item.remove(); this.notices.delete(alert.id); };
    close.onclick = remove; open.onclick = () => { remove(); this.options.navigate(notificationRoute(alert.homeNodeId, alert)); };
    item.append(open, close); container.append(item); this.notices.set(alert.id, item);
    if (alert.kind === 'completion') window.setTimeout(remove, 12000);
  }
  async mount(root: HTMLElement) {
    root.innerHTML = `<h3>Notifications on this device</h3><p class="subtle">These choices apply to this browser or installed Echo app, across all connected machines.</p><div class="notification-options"></div><p class="notification-status" role="status"></p><div class="button-row"><button class="primary" data-enable>Enable notifications on this device</button><button data-disable>Disable background push</button></div><div class="button-row"><button data-test="completion">Test completion notification</button><button data-test="question">Test question notification</button></div><div class="button-row"><button data-sound="completion">Play completion sound</button><button data-sound="question">Play question sound</button></div><p class="subtle">Mobile push requires HTTPS and a running Echo server with internet access. On iPhone/iPad, add Echo to your Home Screen and enable notifications from the installed app. When Echo is closed or suspended, notification sounds are controlled by your device; the two original Echo sounds play while the page can play audio.</p>`;
    const status = root.querySelector<HTMLElement>('.notification-status')!;
    const update = () => {
      const permission = notificationPermission();
      status.textContent = permission === 'unsupported' ? 'System notifications are unavailable here. Use HTTPS (or localhost on this computer). In-app alerts remain available.' : permission === 'denied' ? 'Notifications are blocked. Allow them in your browser or device settings, then enable this device again.' : this.push ? 'Background push is enabled on this device.' : 'Background push is not enabled. In-app alerts and sounds are available.';
    };
    for (const [key, label] of Object.entries({ completion: 'Completion notifications', questions: 'Question notifications', completionSound: 'Completion sound', questionSound: 'Question sound' })) {
      const field = document.createElement('label'); field.className = 'check';
      const input = document.createElement('input'); input.type = 'checkbox'; input.checked = this.preferences[key as keyof NotificationPreferences];
      input.onchange = () => { this.preferences[key as keyof NotificationPreferences] = input.checked; remember(preferencesKey, JSON.stringify(this.preferences)); void this.save().catch(() => this.options.notice('Could not save notification preferences. Try again.', true)); };
      field.append(input, document.createTextNode(label)); root.querySelector('.notification-options')!.append(field);
    }
    root.querySelector<HTMLButtonElement>('[data-enable]')!.onclick = async () => {
      const button = root.querySelector<HTMLButtonElement>('[data-enable]')!; button.disabled = true;
      try {
        if (notificationPermission() === 'unsupported' || !('serviceWorker' in navigator) || !('PushManager' in window)) throw new Error('Use a supported browser over HTTPS; on iPhone/iPad open the installed Home Screen app.');
        // Request permission synchronously from this click, before fetching keys.
        const permission = await Notification.requestPermission(); if (permission !== 'granted') { update(); return; }
        const config = await this.client.json<{ available: boolean; publicKey: string; error?: string }>('/notifications/config');
        if (!config.available) throw new Error(config.error);
        const registration = await navigator.serviceWorker.getRegistration('/');
        if (!registration?.active) throw new Error('Echo needs its active service worker. Reload the installed/production app and try again.');
        const bytes = Uint8Array.from(atob(config.publicKey.replace(/-/g, '+').replace(/_/g, '/')), c => c.charCodeAt(0));
        const subscription = await registration.pushManager.getSubscription() || await registration.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: bytes });
        await this.save(subscription); update();
      } catch (error) { status.textContent = error instanceof Error ? error.message : String(error); }
      finally { button.disabled = false; }
    };
    root.querySelector<HTMLButtonElement>('[data-disable]')!.onclick = async () => {
      try { await this.save(null); const registration = await navigator.serviceWorker?.getRegistration('/'); await (await registration?.pushManager?.getSubscription())?.unsubscribe(); update(); }
      catch { this.options.notice('Could not disable background push. Try again.', true); }
    };
    root.querySelectorAll<HTMLButtonElement>('[data-test]').forEach(button => button.onclick = async () => {
      button.disabled = true;
      try { await this.client.json(`/notifications/devices/${this.device}/test`, 'POST', { kind: button.dataset.test }); status.textContent = 'Test sent to the push service. Check this device’s notifications.'; }
      catch (error) { status.textContent = error instanceof Error ? error.message : String(error); } finally { button.disabled = false; }
    });
    root.querySelectorAll<HTMLButtonElement>('[data-sound]').forEach(button => button.onclick = () => { void this.play(button.dataset.sound as ChatAlert['kind']); });
    update();
  }
}
