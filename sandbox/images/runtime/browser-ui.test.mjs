import test, { before, after } from "node:test";
import assert from "node:assert/strict";
import { BrowserUI } from "./browser-ui.mjs";
const { chromium } = await import("playwright").catch(() => import("../../../web/node_modules/playwright/index.mjs"));

let browser;
before(async () => { browser = await chromium.launch({ headless: true }); });
after(async () => { await browser?.close(); });

async function fixture(t, html) {
  const page = await browser.newPage({ viewport: { width: 1200, height: 750 } });
  page.setDefaultTimeout(1500);
  t.after(() => page.close());
  const ui = new BrowserUI({ currentPage: async () => page, pageID: () => "tab-1", tabList: async () => [{ id: "tab-1" }],
    screenshot: async page => ({ dataBase64: (await page.screenshot()).toString("base64"), width: 1200, height: 750 }) });
  ui.track(page);
  await page.setContent(html);
  return { page, ui, observe: () => ui.observe(), act: (ref, action = "click", extra = {}) => ui.act({ ref, action, requestId: crypto.randomUUID(), ...extra }) };
}
const find = (observation, name, role) => observation.targets.find(target => target.name === name && (!role || target.role === role))?.ref;

test("unrelated mutations and row reordering preserve the intended node", async t => {
  const { page, ui, observe, act } = await fixture(t, `<p id="clock">0</p><table><tr id="one"><td>Invoice A</td><td><button onclick="window.clicked='A'">Pay</button></td></tr><tr id="two"><td>Invoice B</td><td><button onclick="window.clicked='B'">Pay</button></td></tr></table>`);
  const observation = await observe();
  const target = observation.targets.filter(target => target.name === "Pay")[1];
  assert.ok(target);
  await page.evaluate(() => { document.querySelector("#clock").textContent = "1"; document.querySelector("table tbody").prepend(document.querySelector("#two")); });
  const result = await act(target.ref);
  assert.equal(result.execution, "completed");
  assert.equal(result.verification.status, "unverified");
  assert.equal(await page.evaluate(() => window.clicked), "B");
  const searched = await ui.observe({ search: "Invoice B" });
  assert.equal(searched.targets.filter(target => target.name === "Pay").length,1);
  assert.ok(searched.accessibility.includes(`[ref=${searched.targets.find(target => target.name === "Pay").ref}]`));
});

test("changed row context is rejected before input", async t => {
  const { page, observe, act } = await fixture(t, `<table><tr><td id="invoice">Invoice A</td><td><button onclick="window.clicked=true">Pay</button></td></tr></table>`);
  const ref = find(await observe(), "Pay");
  await page.locator("#invoice").evaluate(el => el.textContent = "Invoice B");
  const result = await act(ref);
  assert.equal(result.execution, "not_started");
  assert.equal(result.error.code, "ui_target_changed");
  assert.equal(await page.evaluate(() => window.clicked), undefined);
});

test("unique detached control recovers once; duplicate replacement is rejected", async t => {
  const { page, observe, act } = await fixture(t, `<button onclick="window.count=(window.count||0)+1">Save</button>`);
  const ref = find(await observe(), "Save");
  await page.locator("button").evaluate(el => el.replaceWith(el.cloneNode(true)));
  const result = await act(ref);
  assert.equal(result.execution, "completed");
  assert.equal(result.recovery, "unique_semantic_replacement");
  await page.locator("button").evaluate(el => { el.replaceWith(el.cloneNode(true), el.cloneNode(true)); });
  assert.equal((await act(ref)).execution, "not_started");
  assert.equal(await page.evaluate(() => window.count), 1);
});

test("fill, check, select and focus have intrinsic verification", async t => {
  const { observe, act } = await fixture(t, `<label>Name<input></label><label><input type="checkbox">Agree</label><label>Color<select><option value="red">Red</option><option value="blue">Blue</option></select></label>`);
  const observation = await observe();
  for (const [ref, action, extra] of [
    [find(observation, "Name", "textbox"), "fill", { text: "Brent" }],
    [find(observation, "Agree", "checkbox"), "check", { checked: true }],
    [find(observation, "Color", "combobox"), "select", { values: ["blue"] }],
    [find(observation, "Name", "textbox"), "focus", {}],
  ]) {
    assert.ok(ref, action);
    const result = await act(ref, action, extra);
    assert.equal(result.execution, "completed", JSON.stringify(result));
    assert.equal(result.verification.status, "passed", JSON.stringify(result));
  }
});

test("closed shadow DOM is not guessed; open shadow DOM and iframe controls resolve", async t => {
  const { page, ui, act } = await fixture(t, `<div id="host"></div><iframe srcdoc='<button onclick="window.clicked=true">Frame action</button>'></iframe>`);
  await page.locator("iframe").contentFrame().getByRole("button").waitFor();
  await page.evaluate(() => { document.querySelector("#host").attachShadow({ mode: "open" }).innerHTML = '<button onclick="window.shadowClicked=true">Shadow action</button>'; });
  const observation = await ui.observe();
  const frameRef = find(observation, "Frame action"), shadowRef = find(observation, "Shadow action");
  assert.ok(frameRef, JSON.stringify(observation));
  assert.ok(shadowRef);
  assert.ok(find(await ui.observe({ scopeRef: frameRef }), "Frame action"));
  assert.equal((await act(frameRef)).execution, "completed");
  assert.equal((await act(shadowRef)).execution, "completed");
  assert.equal(await page.locator("iframe").contentFrame().locator("button").evaluate(() => window.clicked), true);
  assert.equal(await page.evaluate(() => window.shadowClicked), true);
  await page.locator("iframe").evaluate(el => el.srcdoc = "<button>New frame</button>");
  await page.locator("iframe").contentFrame().getByText("New frame").waitFor();
  assert.equal((await act(frameRef)).execution, "not_started");
});

test("more than 400 controls can be searched and paginated", async t => {
  const { ui } = await fixture(t, Array.from({ length: 501 }, (_, i) => `<button>Item ${i}</button>`).join(""));
  const last = await ui.observe({ search: "Item 500" });
  assert.ok(find(last, "Item 500"));
  let observation = await ui.observe({ limit: 200 }), count = observation.targets.filter(t => t.role === "button").length;
  while (observation.nextCursor) {
    observation = await ui.observe({ limit: 200, cursor: observation.nextCursor });
    count += observation.targets.filter(t => t.role === "button").length;
  }
  assert.equal(count, 501);
});

test("cancel while covered does not click later; repeated request IDs execute once", async t => {
  const { ui, page, observe } = await fixture(t, `<button onclick="window.count=(window.count||0)+1">Submit</button><div id="cover" style="position:fixed;inset:0;z-index:99"></div>`);
  const ref = find(await observe(), "Submit");
  const controller = new AbortController();
  const params = { ref, action: "click", requestId: "canceled", timeoutMs: 5000 };
  const pending = ui.act(params, controller.signal);
  setTimeout(() => controller.abort(), 150);
  const canceled = await pending;
  assert.equal(canceled.execution, "not_started");
  await page.locator("#cover").evaluate(el => el.remove());
  await new Promise(resolve => setTimeout(resolve, 300));
  assert.equal(await page.evaluate(() => window.count), undefined);
  assert.equal((await ui.act(params)).execution, "not_started");
  const accepted = { ...params, requestId: "accepted" };
  const results = await Promise.all([ui.act(accepted), ui.act(accepted)]);
  assert.ok(results.every(result => result.execution === "completed"));
  assert.equal(await page.evaluate(() => window.count), 1);
  await assert.rejects(ui.act({ ...accepted, action: "focus" }), { code: "ui_request_conflict" });
});

test("disabled controls fail before activation; delayed controls wait once", async t => {
  const { page, observe, act } = await fixture(t, `<button disabled onclick="window.count=(window.count||0)+1">Submit</button>`);
  const ref = find(await observe(), "Submit");
  assert.equal((await act(ref, "click", { timeoutMs: 150 })).execution, "not_started");
  setTimeout(() => page.locator("button").evaluate(el => el.disabled = false), 150);
  assert.equal((await act(ref, "click", { timeoutMs: 2000 })).execution, "completed");
  assert.equal(await page.evaluate(() => window.count), 1);
});

test("dialog observation and explicit dismissal", async t => {
  const { page, ui, observe } = await fixture(t, `<button onclick="alert('Confirm')">Open</button>`);
  await page.evaluate(() => { setTimeout(() => alert("Confirm"), 0); });
  await new Promise(resolve => setTimeout(resolve, 100));
  // A modal blocks DOM evaluation; inspect the dialog without traversing DOM.
  assert.equal(ui.pages.get(page).dialog.message(), "Confirm");
  assert.equal((await observe()).dialog.message, "Confirm");
  const result = await ui.act({ action: "dialog", accept: false, requestId: "dialog" });
  assert.equal(result.execution, "completed");
  assert.equal((await observe()).dialog, undefined);
});

test("fresh text verification, hover, keyboard and drag", async t => {
  const { page, ui, observe, act } = await fixture(t, `<p role="status">Pending</p><input aria-label="Note"><div draggable="true" role="button" aria-label="Drag source" style="width:100px;height:80px;background:red">Drag</div><div role="button" aria-label="Drop zone" style="margin-top:100px;width:200px;height:100px;background:blue" ondragover="event.preventDefault()" ondrop="window.dropped=true">Drop</div>`);
  const observation = await observe();
  const status = observation.targets.find(target => target.role === "status");
  assert.ok(status);
  await page.getByRole("status").evaluate(el => el.textContent = "Done");
  assert.equal((await ui.verify({ ref: status.ref, expect: { kind: "text", value: "Done" }, timeoutMs: 200 })).status, "passed");
  const note = find(observation, "Note");
  assert.equal((await act(note, "type", { text: "hello" })).execution, "completed");
  assert.equal((await act(note, "press", { key: "End" })).execution, "completed");
  assert.equal((await act(note, "hover")).execution, "completed");
  const result = await act(find(observation, "Drag source"), "drag", { toRef: find(observation, "Drop zone") });
  assert.equal(result.execution, "completed", JSON.stringify(result));
  assert.equal(await page.evaluate(() => window.dropped), true);
});

test("physical point type and scroll stay in viewport coordinates", async t => {
  const { page, ui } = await fixture(t, `<input aria-label="Note" style="position:absolute;left:200px;top:100px;width:150px;height:40px"><div style="height:3000px"></div>`);
  const result = await ui.act({ requestId: "point-type", action: "point", pointAction: "type", x: 220, y: 120, text: "point text" });
  assert.equal(result.execution, "completed");
  assert.equal(await page.getByRole("textbox").inputValue(), "point text");
  assert.equal((await ui.act({requestId:"point-select",action:"point",pointAction:"press",x:220,y:120,key:"Control+A"})).execution,"completed");
  assert.equal((await ui.act({requestId:"point-replace",action:"point",pointAction:"type",x:220,y:120,text:"replacement"})).execution,"completed");
  assert.equal(await page.getByRole("textbox").inputValue(),"replacement");
  const bad = await ui.act({ requestId: "point-outside", action: "point", pointAction: "click", x: 1400, y: 120 });
  assert.equal(bad.execution, "not_started");
});
