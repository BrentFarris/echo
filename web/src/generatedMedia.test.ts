import { expect, test } from 'vitest';
import { generatedMedia } from './generatedMedia';
import type { Message } from './types';

const message = (type: string): Message => ({ id: type, chatId: 'chat', botId: 'echo', role: 'tool', author: 'Echo', content: '{}', status: 'completed', createdAt: '', attachments: [{ id: type, chatId: 'chat', name: '<script>.media', mediaType: type, size: 42 }] });

test('generated image, video and GIF previews provide safe download links', () => {
  const root = generatedMedia([message('image/png'), message('video/mp4'), message('image/gif')])!;
  expect(root.querySelectorAll('figure')).toHaveLength(3);
  expect(root.querySelectorAll('img')).toHaveLength(2);
  expect(root.querySelector('video')!.controls).toBe(true);
  expect(root.querySelector('video')!.preload).toBe('metadata');
  expect(root.querySelector('script')).toBeNull();
  expect(root.querySelectorAll('a[download]')).toHaveLength(3);
  expect(root.querySelector('a')!.getAttribute('href')).toBe('/api/v1/chats/chat/attachments/image%2Fpng');
  expect(root.textContent).toContain('Download <script>.media');
});

test('does not duplicate media or expose user attachments as generated output', () => {
  const m = message('image/png');
  expect(generatedMedia([m, m])!.querySelectorAll('img')).toHaveLength(1);
  expect(generatedMedia([{ ...m, role: 'user' }])).toBeUndefined();
  expect(generatedMedia([{ ...m, attachments: [] }])).toBeUndefined();
});
