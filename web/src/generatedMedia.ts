import { previewAttachment, formatBytes } from './composer';
import type { Message } from './types';
import { attachmentURL } from './instances';

// Generated results stay visible when the tool transcript folds into Show work.
export function generatedMedia(messages: Message[]): HTMLElement | undefined {
  const root = document.createElement('div'); root.className = 'generated-media'; root.setAttribute('aria-label', 'Generated media');
  const seen = new Set<string>();
  for (const message of messages) {
    if (message.role !== 'tool') continue;
    for (const a of message.attachments || []) {
      if (seen.has(a.id)) continue;
      seen.add(a.id);
      const url = attachmentURL(message, a);
      const card = document.createElement('figure');
      if (a.mediaType.startsWith('video/')) {
        const video = document.createElement('video'); video.src = url; video.controls = true; video.preload = 'metadata'; video.setAttribute('aria-label', a.name); card.append(video);
      } else if (a.mediaType.startsWith('image/')) {
        const button = document.createElement('button'); button.type = 'button'; button.setAttribute('aria-label', `Preview ${a.name}`);
        const image = document.createElement('img'); image.src = url; image.alt = a.name; button.append(image);
        button.onclick = () => previewAttachment(a.name, a.mediaType, url); card.append(button);
      }
      const caption = document.createElement('figcaption');
      const download = document.createElement('a'); download.href = url; download.download = a.name; download.textContent = `Download ${a.name}`;
      caption.append(download, ` · ${formatBytes(a.size)}`); card.append(caption); root.append(card);
    }
  }
  return seen.size ? root : undefined;
}
