import { expect, test } from 'vitest';
import { MediaGalleries } from './generatedMedia';
import type { Message } from './types';

const message = (type: string, id = type): Message => ({ id, chatId: 'chat', botId: 'echo', role: 'tool', author: 'Echo', content: '{}', status: 'completed', createdAt: '', attachments: [{ id, chatId: 'chat', name: '<script>.media', mediaType: type, size: 42 }] });
const expand = (root: HTMLElement) => { (root as HTMLDetailsElement).open = true; root.dispatchEvent(new Event('toggle')); };

test('collapsed summary loads no media; expansion uses thumbnails, original downloads and playable video', () => {
  const root = new MediaGalleries().render('turn', [message('image/png'), message('video/mp4'), message('image/gif')])!;
  expect(root.textContent).toBe('Media · 2 images + 1 video · 126 B');
  expect(root.querySelectorAll('img, video')).toHaveLength(0);
  expand(root);
  expect(root.querySelectorAll('figure')).toHaveLength(3);
  expect(root.querySelectorAll('img')).toHaveLength(2);
  expect(root.querySelector('img')!.getAttribute('src')).toBe('/api/v1/chats/chat/attachments/image%2Fpng?width=384');
  expect(root.querySelector('img')!.loading).toBe('lazy');
  expect(root.querySelector('video')!.controls).toBe(true);
  expect(root.querySelector('video')!.preload).toBe('metadata');
  expect(root.querySelector('script')).toBeNull();
  expect(root.querySelectorAll('a[download]')).toHaveLength(3);
  expect(root.querySelector('a')!.getAttribute('href')).toBe('/api/v1/chats/chat/attachments/image%2Fpng');
});

test('combines uploads and multiple rounds, deduplicates identities and retains mounted previews', () => {
  const galleries = new MediaGalleries(), first = message('image/png', 'first');
  const input = { ...message('image/png', 'upload'), role: 'user' };
  const root = galleries.render('turn', [first, first], input)!;
  expand(root);
  const image = root.querySelectorAll('img')[1];
  galleries.render('turn', [first, message('image/png', 'second')], input);
  expect(root.textContent).toContain('3 images');
  expect(root.querySelectorAll('img')[1]).toBe(image);
  expect(galleries.render('turn', [first, message('image/png', 'second')], input)).toBe(root);
  expect((root as HTMLDetailsElement).open).toBe(true);
  expect((galleries.render('other-bot', [first]) as HTMLDetailsElement).open).toBe(false);
  expect((new MediaGalleries().render('turn', [first]) as HTMLDetailsElement).open).toBe(false);
});

test('attachment IDs are distinct across machines and origin chats, with routed thumbnails', () => {
  const first = message('image/png', 'same');
  const remote = { ...first, attachments: [{ ...first.attachments![0], nodeId: 'b'.repeat(32), originChatId: 'worker' }] };
  const otherChat = { ...first, attachments: [{ ...first.attachments![0], originChatId: 'other-chat' }] };
  const root = new MediaGalleries().render('turn', [first, remote, otherChat])!;
  expand(root);
  expect(root.querySelectorAll('img')).toHaveLength(3);
  expect(root.querySelectorAll('img')[1].getAttribute('src')).toContain(`/swarm/instances/${'b'.repeat(32)}/chats/worker/attachments/same?width=384`);
});

test('thumbnail errors offer the original without falling back to downloading it', () => {
  const root = new MediaGalleries().render('turn', [message('image/png')])!;
  expand(root);
  root.querySelector('img')!.dispatchEvent(new Event('error'));
  expect(root.querySelector('img')).toBeNull();
  expect(root.querySelector('.media-unavailable')!.textContent).toContain('Open original');
  expect(root.querySelector('a')!.getAttribute('href')).not.toContain('width');
});

test('excludes nonmedia and unrelated user messages; pruning discards deleted turn state', () => {
  const galleries = new MediaGalleries(), image = message('image/png');
  expect(galleries.render('empty', [{ ...image, role: 'user' }, message('application/pdf')])).toBeUndefined();
  const root = galleries.render('turn', [image])!; expand(root);
  galleries.prune(new Set());
  expect((galleries.render('turn', [image]) as HTMLDetailsElement).open).toBe(false);
});
