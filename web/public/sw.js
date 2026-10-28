// Bump this version whenever the offline page changes. Only this public page is cached.
const OFFLINE_CACHE = 'echo-offline-v2';
const OFFLINE_URL = '/offline.html';

self.addEventListener('install', event => {
  event.waitUntil((async () => {
    const cache = await caches.open(OFFLINE_CACHE);
    await cache.add(new Request(OFFLINE_URL, { cache: 'reload' }));
    await self.skipWaiting();
  })());
});

self.addEventListener('activate', event => {
  event.waitUntil((async () => {
    for (const name of await caches.keys()) {
      if (name.startsWith('echo-offline-') && name !== OFFLINE_CACHE) await caches.delete(name);
    }
    await self.clients.claim();
  })());
});

self.addEventListener('fetch', event => {
  const url = new URL(event.request.url);
  // Always fetch live app code. APIs, attachments, plugins and renderer frames bypass the cache.
  if (event.request.method !== 'GET' || event.request.mode !== 'navigate' ||
      url.origin !== self.location.origin || !['/', '/index.html'].includes(url.pathname)) return;
  event.respondWith((async () => {
    try {
      return await fetch(event.request);
    } catch {
      const cache = await caches.open(OFFLINE_CACHE);
      return await cache.match(OFFLINE_URL) || Response.error();
    }
  })());
});

// Only alert IDs and timestamps are persisted here, never transcript content.
function rememberNotification(id) {
  return new Promise(resolve => {
    const request = indexedDB.open('echo-notification-ids', 1);
    request.onupgradeneeded = () => request.result.createObjectStore('seen');
    request.onerror = () => resolve(true);
    request.onsuccess = () => {
      const db = request.result, transaction = db.transaction('seen', 'readwrite'), store = transaction.objectStore('seen');
      let fresh = false;
      const read = store.get(id);
      read.onsuccess = () => { fresh = !read.result; store.put(Date.now(), id); };
      const cursor = store.openCursor();
      cursor.onsuccess = () => { const entry = cursor.result; if (!entry) return; if (entry.value < Date.now() - 7 * 86400000) entry.delete(); entry.continue(); };
      transaction.oncomplete = () => { db.close(); resolve(fresh); };
      transaction.onerror = () => { db.close(); resolve(true); };
    };
  });
}

function notificationDestination(value) {
  try { const url = new URL(value, self.location.origin); if (url.origin === self.location.origin && url.pathname === '/') return url.href; } catch { /* Ignore invalid targets. */ }
  return self.location.origin + '/';
}

async function tryPageSound(alert) {
  const clients = (await self.clients.matchAll({ type: 'window', includeUncontrolled: true })).filter(isEchoWindow);
  const client = clients.find(c => c.focused) || clients.find(c => c.visibilityState === 'visible') || clients[0];
  if (!client) return false;
  return new Promise(resolve => {
    const channel = new MessageChannel();
    const timer = setTimeout(() => { channel.port1.close(); resolve(false); }, 500);
    channel.port1.onmessage = event => { clearTimeout(timer); channel.port1.close(); resolve(event.data === true); };
    client.postMessage({ type: 'echo-notification-sound', alert }, [channel.port2]);
  });
}

function isEchoWindow(client) {
  try { const url = new URL(client.url); return url.origin === self.location.origin && ['/', '/index.html', '/offline.html'].includes(url.pathname); } catch { return false; }
}

self.addEventListener('push', event => {
  event.waitUntil((async () => {
    let payload;
    try { payload = event.data.json(); } catch { payload = {}; }
    const alert = payload.alert || {};
    const fresh = alert.id ? await rememberNotification(alert.id) : true;
    const played = fresh && !payload.silent && alert.kind && await tryPageSound(alert);
    // Web Push must always display a notification, even if a tab has become
    // visible since the server committed to push. Replayed IDs replace silently.
    await self.registration.showNotification(alert.title || 'Echo update', {
      body: alert.preview || 'Open Echo to review your chat.',
      tag: alert.id || 'echo-update', icon: '/icons/echo-192.png',
      silent: !!payload.silent || !fresh || !!played,
      data: { url: notificationDestination(payload.url || '/') }
    });
  })());
});

self.addEventListener('notificationclick', event => {
  event.notification.close();
  event.waitUntil((async () => {
    const url = notificationDestination(event.notification.data?.url || '/');
    const clients = (await self.clients.matchAll({ type: 'window', includeUncontrolled: true })).filter(isEchoWindow);
    const client = clients.find(c => c.url.startsWith(self.location.origin + '/') && c.focused)
      || clients.find(c => c.url.startsWith(self.location.origin + '/'));
    if (client) {
      await client.focus();
      // Reuse the app so unsent drafts and editor contents survive navigation.
      const accepted = await new Promise(resolve => {
        const channel = new MessageChannel();
        const timer = setTimeout(() => { channel.port1.close(); resolve(false); }, 750);
        channel.port1.onmessage = () => { clearTimeout(timer); channel.port1.close(); resolve(true); };
        client.postMessage({ type: 'echo-notification-click', url }, [channel.port2]);
      });
      if (!accepted) await client.navigate(url);
    } else await self.clients.openWindow(url);
  })());
});
