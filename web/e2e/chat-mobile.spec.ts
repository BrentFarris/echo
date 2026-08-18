import { expect, test } from "@playwright/test";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const directory = dirname(fileURLToPath(import.meta.url));

test("keeps the chat send button reachable in a narrow mobile composer", async ({ page }) => {
  await page.setViewportSize({ width: 320, height: 700 });
  await page.setContent(`
    <div id="app">
      <div class="app-shell">
        <div data-region="main">
          <main class="main-content">
            <section class="workspace-panel">
              <section class="work-panel chat-panel" aria-label="Chat">
                <div class="chat-log"></div>
                <form class="chat-composer">
                  <div class="chat-composer-main">
                    <div class="chat-composer-editor" contenteditable="true" data-chat-input></div>
                  </div>
                  <div class="chat-composer-toolbar">
                    <div class="chat-composer-toolbar-left">
                      <button class="chat-toolbar-icon" type="button" aria-label="Attach file"></button>
                      <button class="chat-toolbar-icon" type="button" aria-label="Voice input"></button>
                      <button class="model-selector chat-toolbar-model" type="button">
                        <span class="model-selector-label">A very long configured model display name</span>
                        <span class="model-selector-chevron">⌄</span>
                      </button>
                      <button class="model-selector mode-selector chat-toolbar-mode" type="button">
                        <span class="model-selector-label">A very long custom agent mode name</span>
                        <span class="model-selector-chevron">⌄</span>
                      </button>
                      <span class="chat-toolbar-separator"></span>
                      <button class="chat-toolbar-icon" type="button" aria-label="More options"></button>
                    </div>
                    <div class="chat-composer-toolbar-right">
                      <button class="send-button" type="button" aria-label="Send message"></button>
                    </div>
                  </div>
                </form>
              </section>
            </section>
          </main>
        </div>
        <div data-region="terminal"><section class="terminal-dock"></section></div>
        <nav class="mobile-bottom-nav" aria-label="Mobile primary navigation"></nav>
      </div>
    </div>
  `);
  await page.addStyleTag({ path: resolve(directory, "../css/app.css") });

  const bounds = await page.getByRole("button", { name: "Send message" }).evaluate((button) => {
    const rect = button.getBoundingClientRect();
    return { left: rect.left, right: rect.right, viewportWidth: window.innerWidth };
  });

  expect(bounds.left).toBeGreaterThanOrEqual(0);
  expect(bounds.right).toBeLessThanOrEqual(bounds.viewportWidth);
});
