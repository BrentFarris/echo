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
