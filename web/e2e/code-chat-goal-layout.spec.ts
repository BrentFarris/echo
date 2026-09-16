import { expect, test } from "@playwright/test";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const directory = dirname(fileURLToPath(import.meta.url));

for (const size of [
  { viewport: 1440, dock: 320 },
  { viewport: 1440, dock: 700 },
  { viewport: 320, dock: 320 },
]) {
  test(`keeps goal controls reachable in a ${size.dock}px dock at ${size.viewport}px viewport`, async ({ page }) => {
    await page.setViewportSize({ width: size.viewport, height: 700 });
    await page.setContent(`
      <div class="code-chat-dock" style="width:${size.dock}px;height:600px;margin-left:auto">
        <section class="chat-panel code-chat-surface">
          <header class="code-chat-header"><strong>CODE CHAT</strong><button>+</button></header>
          <div class="chat-log">${"<p>Conversation history</p>".repeat(30)}</div>
          <div class="chat-goal-bar">
            <div class="chat-goal-main">
              <span class="chat-goal-status">Paused</span>
              <span class="chat-goal-objective" title="Long objective">${"Verify stream_reader_read with complete coverage ".repeat(10)}</span>
              <span class="chat-goal-stats">1234 steps · 12h 34m · 123 queued</span>
            </div>
            <div class="chat-goal-actions">
              ${["Pause", "Resume", "Edit", "New goal", "Clear"].map((name) => `<button type="button">${name}</button>`).join("")}
            </div>
          </div>
          <form class="chat-composer">
            <div class="chat-composer-main"><div contenteditable="true" data-chat-input>Queue guidance, then resume when ready</div></div>
            <div class="chat-composer-toolbar code-chat-toolbar">
              <div class="chat-composer-toolbar-left">
                <label class="code-chat-select-label"><select aria-label="Model"><option>DeepSeek v4 Flash</option></select></label>
                <label class="code-chat-select-label"><select aria-label="Mode"><option>Goal</option></select></label>
              </div>
              <div class="chat-composer-toolbar-right"><button class="send-button" type="button" aria-label="Send message">↑</button></div>
            </div>
          </form>
        </section>
      </div>
    `);
    await page.addStyleTag({ path: resolve(directory, "../css/app.css") });
    await page.addStyleTag({ path: resolve(directory, "../src/code/code.css") });
    await page.locator("button").evaluateAll((buttons) => buttons.forEach((button) => {
      button.addEventListener("click", () => { button.dataset.clicked = "true"; });
    }));

    for (const actions of [["Pause", "Edit", "Clear"], ["Resume", "Edit", "Clear"], ["Resume", "Edit", "New goal", "Clear"], ["New goal", "Clear"]]) {
      await page.locator(".chat-goal-actions button").evaluateAll((buttons, visible) => {
        buttons.forEach((button) => { (button as HTMLButtonElement).hidden = !visible.includes(button.textContent!); });
      }, actions);
      const dock = await page.locator(".code-chat-dock").boundingBox();
      for (const name of [...actions, "Send message"]) {
        const button = page.getByRole("button", { name, exact: true });
        const box = await button.boundingBox();
        expect(box).not.toBeNull();
        expect(box!.x).toBeGreaterThanOrEqual(dock!.x);
        expect(box!.x + box!.width).toBeLessThanOrEqual(dock!.x + dock!.width);
        expect(box!.y + box!.height).toBeLessThanOrEqual(dock!.y + dock!.height);
        await button.click();
        await expect(button).toHaveAttribute("data-clicked", "true");
      }
    }
    const before = await page.locator(".chat-log").boundingBox();
    await page.locator(".chat-goal-bar").evaluate((bar) => { (bar as HTMLElement).hidden = true; });
    const after = await page.locator(".chat-log").boundingBox();
    expect(after!.height).toBeGreaterThan(before!.height);
    await page.getByRole("button", { name: "Send message" }).click();
  });
}
