import { expect, test, type Page } from "@playwright/test";
import { createServer, type Server, type ServerResponse } from "node:http";
import { mkdirSync, readFileSync, writeFileSync, unlinkSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

let provider: Server, port = 0;
const requests: any[] = [];
const held = new Set<ServerResponse>();

test.beforeAll(async () => {
  provider = createServer(async (request, response) => {
    let raw = "";
    for await (const chunk of request) raw += chunk.toString();
    if (request.method === 'GET') { response.writeHead(200, { 'Content-Type':'application/json' }); response.end(JSON.stringify({ data:[{ id:'inline-fake' },{ id:'inline-chosen' }] })); return; }
    const body = JSON.parse(raw);
    requests.push(body);
    response.writeHead(200, { "Content-Type": "text/event-stream" });
    const event = (delta: unknown, finish_reason: string | null = null) => response.write(`data: ${JSON.stringify({ choices: [{ delta, finish_reason }] })}\n\n`);
    const finish = (reason = "stop") => { event({}, reason); response.end("data: [DONE]\n\n"); };
    const last = body.messages.at(-1);
    if (last.role === "tool") { event({ content: "Preview ready. You can accept it or send a follow-up." }); finish(); return; }
    const prompt = String(last.content);
    if (prompt.includes("hold")) {
      event({ content: "Working on your request…" });
      held.add(response); response.on("close", () => held.delete(response)); return;
    }
    if (prompt.includes("explain")) { event({ content: "The selected code defines a value. The referenced helper provides context." }); finish(); return; }
    const context = body.messages.findLast((message: any) => String(message.content).startsWith("Current editor context (JSON data):\n"));
    if (!context) { event({ content: "Side chat response." }); finish(); return; }
    const editor = JSON.parse(context.content.split("(JSON data):\n")[1]);
    const edit = prompt.includes("follow-up") ? { oldText: "const value = 2;", newText: "const value = 3;" }
      : prompt.includes("write empty") ? { oldText: "", newText: "Hello 😀\n" }
      : prompt.includes("emoji") ? { oldText: "😀", newText: "😃" }
      : prompt.includes("delete all") ? { oldText: editor.content, newText: "" }
      : { oldText: "const value = 1;", newText: "const value = 2;" };
    // Real provider tool-call streaming, through Echo's real inline endpoint.
    const argumentsJSON = JSON.stringify({ edits: [edit] });
    event({ tool_calls: [{ index: 0, id: "inline-edit", type: "function", function: { name: "propose_inline_edits", arguments: argumentsJSON.slice(0, 12) } }] });
    event({ tool_calls: [{ index: 0, function: { arguments: argumentsJSON.slice(12) } }] });
    finish("tool_calls");
  });
  await new Promise<void>(resolve => provider.listen(0, "127.0.0.1", resolve));
  port = (provider.address() as { port: number }).port;
});
test.afterAll(async () => {
  held.forEach(response => response.end());
  provider.closeAllConnections();
  await new Promise<void>(resolve => provider.close(() => resolve()));
});

const original = 'const value = 1;\r\n// Unicode 😀\r\nexport { value };\r\n';
const owner = (page: Page) => page.locator('.editor-bot-session:not([hidden])');
const zone = (page: Page) => owner(page).locator('.inline-chat-zone');
const input = (page: Page) => zone(page).getByRole('textbox', { name: 'Inline message' });
const editor = (page: Page) => owner(page).locator('[data-monaco-host]').getByRole('textbox', { name: 'Editor content' });
const lines = (page: Page) => owner(page).locator('[data-monaco-host] .view-lines');
async function api(page: Page, path: string, body?: unknown, method = body === undefined ? 'GET' : 'POST') {
  const response = await page.request.fetch(path, { method, data: body });
  expect(response.ok(), await response.text()).toBeTruthy(); return response.json();
}
async function launch(page: Page, plugin = 'monaco-editor') {
  await page.locator('#apps-button').click();
  await page.locator(`[data-app-id="${plugin}"]`).getByRole('button', { name: 'Open Monaco Editor', exact: true }).click();
  await expect(owner(page).locator('.code-app-shell')).toHaveAttribute('aria-busy', 'false');
}
async function openFile(page: Page, name: string) {
  await owner(page).locator('.code-tree-row').filter({ hasText: name }).first().dblclick();
  await expect(owner(page).locator('.code-tab.is-active')).toContainText(name);
}
async function openChat(page: Page) {
  await editor(page).focus(); await page.keyboard.press('Control+i'); await expect(input(page)).toBeFocused();
  await zone(page).locator('.model-settings summary').click();
  const popup = page.locator('.generation-popup:popover-open');
  await popup.getByRole('option', { name: /Inline test/ }).click();
  await popup.press('Escape'); await input(page).focus();
}
async function send(page: Page, prompt: string) { await input(page).fill(prompt); await input(page).press('Enter'); }

let botA: string, botB: string, mainPath: string;
test.beforeEach(async ({ page }, info) => {
  requests.length = 0;
  await page.goto('/'); await expect(page.locator('#apps-button')).toBeVisible();
  await api(page, '/api/v1/session', {});
  const root = join(dirname(fileURLToPath(import.meta.url)), '../../.test-data/inline', `${info.workerIndex}-${Date.now()}`);
  mkdirSync(root, { recursive: true }); mainPath = join(root, 'main.ts');
  writeFileSync(mainPath, original); writeFileSync(join(root, 'helper.ts'), "export const helper = 'reference-content';\n");
  botA = (await api(page, '/api/v1/bots', { handle: 'inline-first', name: 'Inline First', prompt: 'Use selected bot conventions.', roots: [root] })).id;
  botB = (await api(page, '/api/v1/bots', { handle: 'inline-second', name: 'Inline Second', roots: [root] })).id;
  await api(page, '/api/v1/endpoints', { endpoint: { id: 'inline-test', name: 'Inline test', url: `http://127.0.0.1:${port}/v1`, model: 'inline-fake', contextTokens: 65536 } });
  await api(page, '/api/v1/plugins/monaco-editor', { enabled: true, config: {} }, 'PATCH');
  await page.reload(); await launch(page);
  await page.getByLabel('Editor bot', { exact: true }).selectOption(botA);
  await expect(owner(page).locator('.code-app-shell')).toHaveAttribute('aria-busy', 'false');
  await openFile(page, 'main.ts');
});
test.afterEach(async ({ page }) => {
  held.forEach(response => response.end());
  await page.request.delete(`/api/v1/bots/${botA}`); await page.request.delete(`/api/v1/bots/${botB}`);
  await page.request.delete("/api/v1/endpoints/inline-test");
});

test('legacy inline review, references, follow-ups, rejection, acceptance and undo', async ({ page }, info) => {
  test.setTimeout(90_000);
  const errors: string[] = []; page.on('pageerror', error => errors.push(error.message));
  await editor(page).focus(); await page.keyboard.press('Control+End'); await page.keyboard.insertText('// unsaved note\n');
  await page.keyboard.press('Control+Home'); await page.keyboard.press('Shift+End'); await openChat(page);
  await input(page).fill('explain @helper');
  await expect(zone(page).getByRole('option').first()).toBeVisible(); await input(page).press('Enter');
  await expect(zone(page).locator('.chat-mention-chip')).toHaveText('helper.ts'); await input(page).press('Enter');
  await expect(zone(page)).toContainText('The selected code defines a value');
  expect(JSON.stringify(requests.at(-1))).toContain('reference-content'); expect(JSON.stringify(requests.at(-1))).toContain('unsaved note');
  expect(requests.at(-1).messages[0].content).toContain('Use selected bot conventions.');
  await send(page, 'change'); await expect(owner(page).locator('.inline-chat-added-lines')).toContainText('const value = 2;');
  await send(page, 'follow-up'); await expect(owner(page).locator('.inline-chat-added-lines')).toContainText('const value = 3;');
  await expect(lines(page)).toContainText('const value = 1;'); expect(readFileSync(mainPath, 'utf8')).toBe(original);
  await page.emulateMedia({ reducedMotion: 'reduce', colorScheme: 'light' }); await page.screenshot({ path: info.outputPath('inline-review-light.png') });
  await page.emulateMedia({ colorScheme: 'dark' }); await page.screenshot({ path: info.outputPath('inline-review-dark.png') });
  await zone(page).getByRole('button', { name: 'Cancel and close' }).click();
  await expect(zone(page)).toHaveCount(0); await expect(lines(page)).toContainText('unsaved note');
  await openChat(page); await send(page, 'change'); await expect(zone(page).getByRole('button', { name: 'Accept changes' })).toBeEnabled();
  await zone(page).getByRole('button', { name: 'Accept changes' }).click(); await expect(zone(page)).toHaveCount(0);
  await expect(lines(page)).toContainText('const value = 2;'); expect(readFileSync(mainPath, 'utf8')).toBe(original);
  await page.keyboard.press('Control+z'); await expect(lines(page)).toContainText('const value = 1;'); await expect(lines(page)).toContainText('unsaved note');
  await openChat(page); await send(page, 'change'); await expect(zone(page).getByRole('button', { name: 'Accept changes' })).toBeEnabled();
  await editor(page).focus(); await page.keyboard.press('Control+End'); await page.keyboard.insertText('// newer manual edit\n');
  await expect(zone(page).getByRole('button', { name: 'Accept changes' })).toBeDisabled();
  await zone(page).getByRole('button', { name: 'Regenerate from current file' }).click(); await expect(zone(page).getByRole('button', { name: 'Accept changes' })).toBeEnabled();
  await zone(page).getByRole('button', { name: 'Accept changes' }).click(); await expect(zone(page)).toHaveCount(0); await page.keyboard.press('Control+s');
  await expect.poll(() => readFileSync(mainPath, 'utf8')).toContain('const value = 2;\r\n'); expect(readFileSync(mainPath, 'utf8')).toContain('newer manual edit');
  expect(errors).toEqual([]);
});

test('per-bot model settings, tab lifetime, presentation, stop and reload', async ({ page }, info) => {
  test.setTimeout(90_000);
  const mainModel = await page.locator('#model-name').textContent(); await openChat(page);
  await zone(page).locator('.model-settings summary').click(); const popup = page.locator('.generation-popup:popover-open');
  await expect(popup.getByLabel('Model machine')).toBeDisabled(); await popup.getByLabel('Model identifier').fill('inline-chosen');
  await popup.getByLabel('Thinking', { exact: true }).fill('5'); await popup.getByLabel('Temperature', { exact: true }).fill('0.23');
  await page.screenshot({ path: info.outputPath('inline-model-popup.png') });
  await popup.press('Escape'); await send(page, 'explain'); await expect(zone(page)).toContainText('The selected code defines a value');
  expect(requests.at(-1)).toMatchObject({ model: 'inline-chosen', reasoning_effort: 'high', temperature: .23 }); await expect(page.locator('#model-name')).toHaveText(mainModel!);
  await send(page, 'change'); await expect(zone(page).getByRole('button', { name: 'Accept changes' })).toBeEnabled();
  await input(page).fill('retained draft');
  await page.getByLabel('Editor bot', { exact: true }).selectOption(botB); await openFile(page, 'main.ts'); await openChat(page);
  await zone(page).locator('.model-settings summary').click(); await expect(popup.getByLabel('Model identifier')).toHaveValue('inline-fake'); await popup.press('Escape');
  await page.getByLabel('Editor bot', { exact: true }).selectOption(botA); await expect(input(page)).toHaveText('retained draft');
  await send(page, 'hold'); await expect(zone(page).getByRole('button', { name: 'Stop response' })).toBeVisible();
  await openFile(page, 'helper.ts'); await expect(zone(page)).toHaveCount(0); await openFile(page, 'main.ts'); await expect(zone(page).getByRole('button', { name: 'Stop response' })).toBeVisible();
  await page.getByRole('button', { name: 'Open in Window', exact: true }).click(); await expect(zone(page)).toBeVisible();
  await page.getByRole('button', { name: 'Open Embedded', exact: true }).click();
  await zone(page).getByRole('button', { name: 'Stop response' }).click(); await expect(zone(page)).toContainText('Stopped'); await expect.poll(() => held.size).toBe(0);
  await expect(owner(page).locator('.inline-chat-added-lines')).toContainText('const value = 2;'); await expect(zone(page).getByRole('button', { name: 'Accept changes' })).toBeEnabled();
  await page.reload(); await expect(owner(page).locator('.code-app-shell')).toHaveAttribute('aria-busy', 'false'); await expect(zone(page)).toHaveCount(0);
  await editor(page).focus(); await page.keyboard.press('Control+i'); await zone(page).locator('.model-settings summary').click();
  await expect(popup.getByLabel('Model identifier')).toHaveValue('inline-chosen'); await expect(popup.getByLabel('Thinking', { exact: true })).toHaveValue('5'); await expect(popup.getByLabel('Temperature', { exact: true })).toHaveValue('0.23');
});

test('untitled Unicode and empty-file proposals stay undoable', async ({ page }) => {
  await editor(page).focus(); await page.keyboard.press('Control+n'); await openChat(page); await send(page, 'write empty');
  await expect(zone(page).getByRole('button', { name: 'Accept changes' })).toBeEnabled(); await zone(page).getByRole('button', { name: 'Accept changes' }).click(); await expect(lines(page)).toContainText('Hello 😀');
  await openChat(page); await send(page, 'emoji'); await expect(zone(page).getByRole('button', { name: 'Accept changes' })).toBeEnabled();
  await zone(page).getByRole('button', { name: 'Accept changes' }).click(); await expect(lines(page)).toContainText('Hello 😃');
  await page.keyboard.press('Control+z'); await expect(lines(page)).toContainText('Hello 😀'); await openChat(page); await send(page, 'delete all');
  await expect(zone(page).getByRole('button', { name: 'Accept changes' })).toBeEnabled(); await zone(page).getByRole('button', { name: 'Accept changes' }).click(); await expect(lines(page)).not.toContainText('Hello');
  await page.keyboard.press('Control+z'); await expect(lines(page)).toContainText('Hello 😀');
});

test('remote inline execution uses the editor bot and model on its owning machine', async ({ page }) => {
  test.setTimeout(60_000);
  const remote = await api(page, '/api/v1/swarm/peers', { url: 'http://127.0.0.1:3744', password: 'echo-test-password' });
  const base = `/api/v1/swarm/instances/${remote.nodeId}`;
  let remoteBot = '';
  try {
    remoteBot = (await api(page, `${base}/bots`, { handle: 'inline-remote', name: 'Inline Remote', prompt: 'Remote inline conventions.', roots: [dirname(mainPath)] })).id;
    await api(page, `${base}/endpoints`, { endpoint: { id: 'inline-test', name: 'Inline test remote', url: `http://127.0.0.1:${port}/v1`, model: 'inline-fake', contextTokens: 65536 } });
    await api(page, `${base}/plugins/monaco-editor`, { enabled: true, config: {} }, 'PATCH');
    await page.reload(); await page.getByRole('button', { name: 'Close plugin', exact: true }).click();
    await launch(page, `${remote.nodeId}:monaco-editor`);
    await page.getByLabel('Editor bot', { exact: true }).selectOption(`${remote.nodeId}:${remoteBot}`);
    await openFile(page, 'main.ts'); await openChat(page);
    const dispatched = page.waitForRequest(request => request.url().includes(`${base}/plugins/monaco-editor/bots/${remoteBot}/inline-chat`));
    await send(page, 'change');
    expect((await dispatched).postDataJSON().generation.endpointId).toBe('inline-test');
    await expect(zone(page).getByRole('button', { name: 'Accept changes' })).toBeEnabled();
    expect(requests.at(-1).messages[0].content).toContain('Remote inline conventions.');
    expect(readFileSync(mainPath, 'utf8')).toBe(original);
    await zone(page).getByRole('button', { name: 'Accept changes' }).click(); await expect(zone(page)).toHaveCount(0);
    await expect(lines(page)).toContainText('const value = 2;');
  } finally {
    if (remoteBot) await page.request.delete(`${base}/bots/${remoteBot}`);
    await page.request.delete(`${base}/endpoints/inline-test`);
    await page.request.delete(`/api/v1/swarm/peers/${remote.nodeId}`);
  }
});


test('shared model popup matches chat, supports keyboard access and stays inside the viewport', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.getByRole('button', { name: 'Open in Window', exact: true }).click();
  const popup = page.locator('.generation-popup:popover-open');
  const appearance = () => popup.evaluate(element => Object.fromEntries(['.popover-heading', 'label', '[role=option][aria-selected=true]', '.range-label', '.popover-footer'].map(selector => {
    const style = getComputedStyle(element.querySelector(selector)!);
    return [selector, [style.fontFamily, style.fontSize, style.lineHeight, style.padding, style.borderRadius, style.backgroundColor]];
  })));
  await page.locator('.composer .model-settings summary').click(); const chatAppearance = await appearance(); await popup.press('Escape');
  await openChat(page); const trigger = zone(page).locator('.model-settings summary');
  await trigger.focus(); await trigger.press('Enter'); await expect(popup).toBeVisible();
  expect(await appearance()).toEqual(chatAppearance);
  await trigger.press('Tab'); await expect(popup.locator('.endpoint-options [role=option]').first()).toBeFocused();
  await page.keyboard.press('Shift+Tab'); await expect(trigger).toBeFocused();
  await trigger.press('ArrowDown'); await expect(popup.locator('.endpoint-options [role=option]').first()).toBeFocused();
  await page.keyboard.press('End'); await expect(popup.locator('.endpoint-options [role=option]').last()).toBeFocused();
  await popup.press('Escape'); await expect(trigger).toBeFocused(); await expect(popup).toHaveCount(0);
  await page.getByRole('button', { name: 'Open Embedded', exact: true }).click();
  await page.setViewportSize({ width: 390, height: 640 });
  await trigger.click(); await expect(popup).toBeVisible();
  const bounds = (await popup.boundingBox())!;
  expect(bounds.x).toBeGreaterThanOrEqual(0); expect(bounds.x + bounds.width).toBeLessThanOrEqual(390);
  expect(bounds.y).toBeGreaterThanOrEqual(0); expect(bounds.y + bounds.height).toBeLessThanOrEqual(640);
});

test('disk changes, tab switches, renames and deletion invalidate old proposals', async ({ page }) => {
  await openChat(page); await send(page, 'change'); const accept = zone(page).getByRole('button', { name: 'Accept changes' });
  await expect(accept).toBeEnabled();
  writeFileSync(mainPath, original + '// externally edited\r\n');
  await expect(accept).toBeDisabled();
  await openFile(page, 'helper.ts'); await openFile(page, 'main.ts'); await expect(accept).toBeDisabled();
  await zone(page).getByRole('button', { name: 'Regenerate from current file' }).click(); await expect(accept).toBeEnabled();
  await owner(page).locator('.code-tree-row').filter({ hasText: 'main.ts' }).first().click(); await page.keyboard.press('F2');
  await owner(page).locator('.code-tree input').fill('renamed.ts'); await page.keyboard.press('Enter');
  await expect(accept).toBeDisabled();
  await zone(page).getByRole('button', { name: 'Regenerate from current file' }).click(); await expect(accept).toBeEnabled();
  unlinkSync(join(dirname(mainPath), 'renamed.ts'));
  await expect(accept).not.toBeEnabled();
  expect(await lines(page).textContent()).not.toContain('const value = 2;');
});
