import { previewAttachment, formatBytes } from './composer';
import type { Attachment, Message } from './types';
import { attachmentURL, attachmentPreviewURL } from './instances';

type Media = { attachment: Attachment; message: Message; url: string };
type Gallery = { root: HTMLDetailsElement; summary: HTMLElement; grid: HTMLElement; items: Map<string, Media>; figures: Map<string, HTMLElement>; messageIds: Set<string> };

// Retain disclosures and figures across rounds, final-answer promotion and bot
// tab changes. Nothing loads until the user expands this turn's gallery.
export class MediaGalleries {
  private galleries = new Map<string, Gallery>();

  clear() { this.galleries.clear(); }

  prune(messageIds: Set<string>) {
    for (const [key, gallery] of this.galleries) if (![...gallery.messageIds].some(id => messageIds.has(id))) this.galleries.delete(key);
  }

  render(key: string, messages: Message[], input?: Message): HTMLElement | undefined {
    const items = new Map<string, Media>();
    for (const message of [...(input ? [input] : []), ...messages.filter(m => m.role === 'tool')]) {
      for (const attachment of message.attachments || []) {
        if (!/^(image|video)\//.test(attachment.mediaType)) continue;
        const url = attachmentURL(message, attachment);
        if (!items.has(url)) items.set(url, { attachment, message, url });
      }
    }
    if (!items.size) { this.galleries.delete(key); return; }
    let gallery = this.galleries.get(key);
    if (!gallery) {
      const root = document.createElement('details'); root.className = 'media-disclosure'; root.setAttribute('aria-label', 'Turn media');
      const summary = document.createElement('summary');
      const grid = document.createElement('div'); grid.className = 'generated-media';
      root.append(summary, grid);
      gallery = { root, summary, grid, items, figures: new Map(), messageIds: new Set() };
      const retained = gallery;
      root.addEventListener('toggle', () => { if (root.open) this.populate(retained); });
      this.galleries.set(key, gallery);
    }
    gallery.items = items;
    gallery.messageIds = new Set(messages.map(message => message.id));
    const all = [...items.values()], images = all.filter(item => item.attachment.mediaType.startsWith('image/')).length, videos = all.length - images;
    const counts = [images ? `${images} image${images === 1 ? '' : 's'}` : '', videos ? `${videos} video${videos === 1 ? '' : 's'}` : ''].filter(Boolean);
    gallery.summary.textContent = `Media · ${counts.join(' + ')} · ${formatBytes(all.reduce((n, item) => n + item.attachment.size, 0))}`;
    for (const [url, figure] of gallery.figures) if (!items.has(url)) { figure.remove(); gallery.figures.delete(url); }
    if (gallery.root.open) this.populate(gallery);
    return gallery.root;
  }

  private populate(gallery: Gallery) {
    for (const [url, { attachment: a, message }] of gallery.items) {
      if (gallery.figures.has(url)) continue;
      const card = document.createElement('figure');
      if (a.mediaType.startsWith('video/')) {
        const video = document.createElement('video'); video.src = url; video.controls = true; video.preload = 'metadata'; video.playsInline = true; video.setAttribute('aria-label', a.name); card.append(video);
      } else {
        const button = document.createElement('button'); button.type = 'button'; button.setAttribute('aria-label', `Preview ${a.name}`);
        const image = document.createElement('img'); image.loading = 'lazy'; image.decoding = 'async'; image.alt = a.name;
        image.onerror = () => { const placeholder = document.createElement('span'); placeholder.className = 'media-unavailable'; placeholder.textContent = 'Preview unavailable · Open original'; image.replaceWith(placeholder); };
        image.src = attachmentPreviewURL(message, a); button.append(image);
        button.onclick = () => previewAttachment(a.name, a.mediaType, url); card.append(button);
      }
      const caption = document.createElement('figcaption');
      const download = document.createElement('a'); download.href = url; download.download = a.name; download.textContent = `Download ${a.name}`;
      caption.title = message.role === 'user' ? 'Uploaded by you' : message.author;
      caption.append(download, ` · ${formatBytes(a.size)}`); card.append(caption);
      gallery.figures.set(url, card); gallery.grid.append(card);
    }
  }
}
