import { test, expect } from '@playwright/test';
import { mkdir, writeFile, readFile } from 'node:fs/promises';
import path from 'node:path';
test.use({ actionTimeout: 10_000 });

test('Go debugger supports gutter, scopes, watches, stepping and reload recovery', async ({ page }) => {
 test.skip(process.env.ECHO_TEST_DELVE !== '1', 'Native Delve acceptance requires ECHO_TEST_DELVE=1');
 test.setTimeout(150_000);
 const errors: string[] = []; page.on('pageerror', error => errors.push(error.message));
 await page.goto('/'); await page.request.post('/api/v1/session', { data: {} });
 const state = await (await page.request.get('/api/v1/state')).json();
 const root = path.resolve('../.test-data/debugger', String(Date.now())); await mkdir(path.join(root, '.vscode'), { recursive: true });
 const source = 'package main\nimport ("fmt"; "time")\nfunc twice(n int) int {\n result := n * 2\n return result\n}\nfunc main() {\n value := 21\n fmt.Println(twice(value))\n time.Sleep(time.Minute)\n}\n';
 await writeFile(path.join(root, 'main.go'), source);
 await writeFile(path.join(root, 'go.mod'), 'module example.com/debugger\ngo 1.26.0\n');
 await writeFile(path.join(root, '.vscode/launch.json'), '{// shared with VS Code\n"version":"0.2.0","configurations":[{"name":"Go acceptance","type":"go","request":"launch","mode":"debug","program":"${workspaceFolder}",}],}');
 await page.request.post('/api/v1/bots', { data: { ...state.bots.find((b: any) => b.id === 'echo'), roots: [root] } });
 await page.request.patch('/api/v1/plugins/monaco-editor', { data: { enabled: true, config: {} } });
 await page.request.patch('/api/v1/plugins/terminal', { data: { enabled: true, config: {} } });
 await page.request.patch('/api/v1/plugins/go', { data: { enabled: true, config: { languageServices: false, debugging: true } } });
 const debugState = await (await page.request.get('/api/v1/plugins/monaco-editor/bots/echo/debug/state')).json();
 await page.request.put('/api/v1/plugins/monaco-editor/bots/echo/debug/state', { data: { expectedRevision: debugState.state.revision, state: { sourceBreakpoints: [], watches: [] } } });
 await page.reload(); await page.locator('#apps-button').click();
 await page.locator('[data-app-id="monaco-editor"]').getByRole('button', { name: 'Open Monaco Editor', exact: true }).click();
 const editor = page.locator('.editor-bot-session:not([hidden])');
 await expect(editor.locator('.code-app-shell')).toHaveAttribute('aria-busy', 'false');
 await editor.locator('.code-tree-row').filter({ hasText: 'main.go' }).first().dblclick();
 await editor.getByRole('button', { name: 'Run and Debug', exact: true }).click();
 await expect(editor.getByLabel('Debug configuration')).toContainText('Go acceptance');
 // Click the glyph margin alongside line 9, rather than setting state through an API.
 const line = editor.locator('[data-monaco-host] .line-numbers').filter({ hasText: /^9$/ });
 const box = await line.boundingBox(); const host = await editor.locator('[data-monaco-host]').boundingBox();
 expect(box).toBeTruthy(); expect(host).toBeTruthy();
 await page.mouse.click(host!.x + 10, box!.y + box!.height / 2);
 await expect(editor.locator('.echo-debug-breakpoint')).not.toHaveCount(0);
 // A dirty named buffer must be saved before launching.
 await editor.locator('[data-monaco-host] .view-lines').click(); await page.keyboard.press('Control+Home'); await page.keyboard.type('// breakpoint movement\n');
 await expect.poll(async () => (await (await page.request.get('/api/v1/plugins/monaco-editor/bots/echo/debug/state')).json()).state.sourceBreakpoints[0]?.line).toBe(10);
 await page.keyboard.press('Control+End'); await page.keyboard.type('// saved by debugger');
 await editor.getByRole('button', { name: 'Start Debugging', exact: true }).click();
 await expect(editor.getByRole('button', { name: 'Continue', exact: true })).toBeVisible({ timeout: 75_000 });
 expect(await readFile(path.join(root, 'main.go'), 'utf8')).toContain('// saved by debugger');
 await expect(editor.locator('.debug-variable-name').filter({ hasText: /^value$/ })).toBeVisible();
 await expect(editor.locator('.debug-variable-value').filter({ hasText: /^21$/ })).toBeVisible();
 const valuePoint = await editor.locator('.view-line').filter({ hasText: 'fmt.Println(twice(value))' }).evaluate(line => {
  const walk = document.createTreeWalker(line, NodeFilter.SHOW_TEXT);
  let node; while ((node = walk.nextNode())) { const index = node.textContent?.indexOf('value') ?? -1; if (index >= 0) { const range = document.createRange(); range.setStart(node, index); range.setEnd(node, index + 5); const box = range.getBoundingClientRect(); return { x: box.x + box.width / 2, y: box.y + box.height / 2 }; } }
  throw new Error('value token missing');
 });
 await page.mouse.move(valuePoint.x, valuePoint.y);
 await expect(editor.locator('.monaco-hover:visible')).toContainText('21');
 await page.mouse.move(0, 0);
 await editor.getByRole('button', { name: 'Add Watch Expression', exact: true }).click();
 await page.getByRole('dialog').locator('input').fill('value + 1'); await page.getByRole('dialog').getByRole('button', { name: 'OK', exact: true }).click();
 await expect(editor.locator('.debug-watch-row')).toContainText('22');
 const before = await (await page.request.get('/api/v1/plugins/monaco-editor/bots/echo/debug/snapshot')).json();
 const sessionID = before.snapshot.sessions.find((s: any) => s.status === 'stopped').id;
 await page.reload();
 await expect(editor.locator('.code-app-shell')).toHaveAttribute('aria-busy', 'false');
 await expect(editor.getByRole('button', { name: 'Continue', exact: true })).toBeVisible();
 await editor.getByRole('button', { name: 'Run and Debug', exact: true }).click();
 await expect(editor.locator('.debug-watch-row')).toContainText('22');
 const after = await (await page.request.get('/api/v1/plugins/monaco-editor/bots/echo/debug/snapshot')).json();
 expect(after.snapshot.sessions.find((s: any) => s.status === 'stopped').id).toBe(sessionID);
 await editor.getByRole('button', { name: 'Step Into', exact: true }).click();
 await expect(editor.locator('.debug-frame.is-selected')).toContainText('main.twice');
 await expect(editor.locator('.debug-variable-name').filter({ hasText: /^n$/ })).toBeVisible();
 await editor.getByRole('button', { name: 'Step Out', exact: true }).click();
 await expect(editor.locator('.debug-frame.is-selected')).toContainText('main.main');
 await editor.getByRole('button', { name: 'Stop', exact: true }).click();
 await expect(editor.getByRole('toolbar', { name: 'Debug controls' })).toHaveCount(0);
 // Changing the shared launch file takes effect without recreating an Echo profile.
 const normal = await (await page.request.get('/api/v1/plugins/terminal/bots/echo/sessions')).json();
 await writeFile(path.join(root, '.vscode/launch.json'), JSON.stringify({ version: '0.2.0', configurations: [{ name: 'Go acceptance', type: 'go', request: 'launch', mode: 'debug', program: '${workspaceFolder}', console: 'integratedTerminal' }] }));
 await editor.getByRole('button', { name: 'Start Debugging', exact: true }).click();
 await expect(editor.getByRole('button', { name: 'Continue', exact: true })).toBeVisible({ timeout: 30_000 });
 await expect(page.getByRole('tab', { name: 'Debug Terminal', exact: true })).toBeVisible();
 await editor.getByRole('button', { name: 'Continue', exact: true }).click();
 await expect.poll(async () => {
  const result = await (await page.request.get('/api/v1/plugins/terminal/bots/echo/sessions')).json();
  return (result.sessions || []).filter((s: any) => s.kind === 'debug').flatMap((s: any) => s.output || []).map((chunk: any) => Buffer.from(chunk.data, 'base64').toString()).join('');
 }).toContain('42');
 await page.reload(); await expect(editor.locator('.code-app-shell')).toHaveAttribute('aria-busy', 'false');
 await expect(page.getByRole('tab', { name: 'Debug Terminal', exact: true })).toBeVisible();
 await editor.getByRole('button', { name: 'Stop', exact: true }).click();
 await expect(editor.getByRole('toolbar', { name: 'Debug controls' })).toHaveCount(0);
 await expect.poll(async () => {
  const result = await (await page.request.get('/api/v1/plugins/terminal/bots/echo/sessions')).json();
  return result.sessions.find((s: any) => s.kind === 'debug').status;
 }).toBe('exited');
 const ended = await (await page.request.get('/api/v1/plugins/terminal/bots/echo/sessions')).json();
 const shell = normal.sessions?.find((s: any) => s.kind === 'default');
 if (shell) expect(ended.sessions.find((s: any) => s.id === shell.id).status).toBe(shell.status);
 // A failed normal save must never launch or restart a process.
 await editor.locator('[data-monaco-host] .view-lines').click(); await page.keyboard.press('Control+End'); await page.keyboard.type('// pending save');
 await page.route('**/plugins/monaco-editor/bots/*/fs/file', route => route.request().method() === 'PUT' ? route.fulfill({status:500,json:{error:'fixture save failure'}}) : route.continue());
 const sessionsBeforeFailedSave = (await (await page.request.get('/api/v1/plugins/monaco-editor/bots/echo/debug/snapshot')).json()).snapshot.sessions.length;
 await editor.getByRole('button', { name: 'Run and Debug', exact: true }).click();
 await editor.getByRole('button', { name: 'Start Debugging', exact: true }).click();
 await expect(page.getByText('fixture save failure', { exact: true })).toBeVisible();
 expect((await (await page.request.get('/api/v1/plugins/monaco-editor/bots/echo/debug/snapshot')).json()).snapshot.sessions.length).toBe(sessionsBeforeFailedSave);
 expect(errors).toEqual([]);
});
