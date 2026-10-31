import { test, expect } from '@playwright/test';
import { mkdir, writeFile, readFile } from 'node:fs/promises';
import path from 'node:path';

test('Go protocol providers rename multiple buffers without saving and show unsaved diagnostics', async ({ page }) => {
  const errors: string[] = []; page.on('pageerror', error => errors.push(error.message));
  await page.goto('/'); await page.request.post('/api/v1/session', { data: {} });
  const state = await (await page.request.get('/api/v1/state')).json();
  const root = path.resolve('../.test-data/go-language', String(Date.now())); await mkdir(root, { recursive: true });
  const main = 'package main\n\nfunc main() { greet() }\n'; const helper = 'package main\n\nfunc greet() {}\n';
  await writeFile(path.join(root, 'main.go'), main); await writeFile(path.join(root, 'helper.go'), helper);
  await writeFile(path.join(root, 'go.mod'), 'module example.com/browser\ngo 1.26.0\n');
  await page.request.post('/api/v1/bots', { data: { ...state.bots.find((b: any) => b.id === 'echo'), roots: [root] } });
  await page.request.patch('/api/v1/plugins/monaco-editor', { data: { enabled: true, config: {} } });
  await page.request.patch('/api/v1/plugins/go', { data: { enabled: true, config: { languageServices: true } } });
  await page.route('**/plugins/go/bots/*/language/status?*', route => route.fulfill({ json: { state: 'ready', directory: root, version: 'fixture' } }));
  const methods: string[] = [];
  await page.routeWebSocket('**/plugins/go/bots/*/language/session?*', socket => {
    socket.send(JSON.stringify({ method: 'echo/status', params: { state: 'ready' } }));
    const versions = new Map<string, number>();
    socket.onMessage(async data => {
      const message = JSON.parse(String(data)); methods.push(message.method); const p = message.params;
      if (message.method === 'textDocument/didOpen' || message.method === 'textDocument/didChange') {
        versions.set(p.textDocument.uri, p.textDocument.version);
        const text = p.textDocument.text ?? p.contentChanges[0].text;
        socket.send(JSON.stringify({ method: 'textDocument/publishDiagnostics', params: { uri: p.textDocument.uri, version: p.textDocument.version,
          diagnostics: text.includes('broken') ? [{ range: { start: { line: 2, character: 0 }, end: { line: 2, character: 4 } }, severity: 1, message: 'Unsaved fixture error' }] : [] } }));
      }
      if (!message.id) return;
      let result: any = null;
      if (message.method === 'textDocument/prepareRename') result = { range: { start: { line: 2, character: 14 }, end: { line: 2, character: 19 } }, placeholder: 'greet' };
      if (message.method === 'textDocument/rename') {
        const uri = p.textDocument.uri; const helperURI = uri.replace(/main\.go$/, 'helper.go'); const rootId = new URL(uri).hostname;
        const revisions: Record<string, string> = {};
        for (const [target, file] of [[uri, 'main.go'], [helperURI, 'helper.go']]) {
          const snapshot = await (await page.request.get(`/api/v1/plugins/monaco-editor/bots/echo/fs/file?${new URLSearchParams({ rootId, path: file })}`)).json(); revisions[target] = snapshot.revision;
        }
        result = { edit: { changes: {
          [uri]: [{ range: { start: { line: 2, character: 14 }, end: { line: 2, character: 19 } }, newText: p.newName }],
          [helperURI]: [{ range: { start: { line: 2, character: 5 }, end: { line: 2, character: 10 } }, newText: p.newName }],
        } }, revisions };
      }
      if (message.method === 'textDocument/documentSymbol') result = [{ name: 'main', kind: 12, range: { start: { line: 2, character: 0 }, end: { line: 2, character: 22 } }, selectionRange: { start: { line: 2, character: 5 }, end: { line: 2, character: 9 } } }];
      socket.send(JSON.stringify({ id: message.id, result }));
    });
  });
  await page.reload(); await page.locator('#apps-button').click();
  await page.locator('[data-app-id="monaco-editor"]').getByRole('button', { name: 'Open Monaco Editor', exact: true }).click();
  const editor = page.locator('.editor-bot-session:not([hidden])');
  await expect(editor.locator('.code-app-shell')).toHaveAttribute('aria-busy', 'false');
  await editor.locator('.code-tree-row').filter({ hasText: 'main.go' }).first().dblclick();
  await expect(editor.locator('[data-status=go]')).toHaveText('Go: ready');
  await editor.locator('[data-monaco-host] .view-lines').click({ position: { x: 80, y: 10 } });
  await page.keyboard.press('Control+Home'); await page.keyboard.press('ArrowDown'); await page.keyboard.press('ArrowDown'); await page.keyboard.press('Home');
  for (let i = 0; i < 16; i++) await page.keyboard.press('ArrowRight');
  await page.keyboard.press('F2');
  const rename = editor.locator('.rename-box input'); await expect(rename).toBeVisible(); await rename.fill('welcome'); await page.keyboard.press('Enter');
  await expect(editor.getByRole('tab', { name: /main\.go/ }).locator('.code-tab-dirty')).toHaveClass(/is-visible/);
  await expect(editor.getByRole('tab', { name: /helper\.go/ }).locator('.code-tab-dirty')).toHaveClass(/is-visible/);
  expect(await readFile(path.join(root, 'main.go'), 'utf8')).toBe(main); expect(await readFile(path.join(root, 'helper.go'), 'utf8')).toBe(helper);
  await editor.getByRole('tab', { name: /main\.go/ }).click(); await page.keyboard.press('Control+End'); await page.keyboard.type('// broken');
  await expect(editor.locator('.squiggly-error')).not.toHaveCount(0);
  expect(methods).toContain('textDocument/rename'); expect(methods).toContain('textDocument/didChange'); expect(errors).toEqual([]);
});
