import { marked } from 'marked';
// This document is embedded with sandbox=allow-scripts and an opaque origin.
let nonce = '';
addEventListener('message', async event => {
  if (event.source !== parent || !event.data) return;
  if (event.data.type === 'init') { nonce = event.data.nonce; return; }
  if (!nonce || event.data.nonce !== nonce || event.data.type !== 'render') return;
  const html = await marked.parse(String(event.data.text || ''));
  parent.postMessage({ type: 'rendered', nonce, id: event.data.id, html }, '*');
});
parent.postMessage({ type: 'renderer-ready' }, '*');
