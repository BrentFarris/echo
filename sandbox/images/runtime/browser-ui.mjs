import { randomUUID } from "node:crypto";
import { setTimeout as delay } from "node:timers/promises";

const fail = (code, message) => Object.assign(new Error(message), { code, status: 409 });
const active = signal => { if (signal?.aborted) throw fail("ui_canceled", "UI operation canceled; observe before continuing"); };
const bounded = (value, fallback, maximum) => Math.max(1, Math.min(Number(value) || fallback, maximum));

// Parse only the documented AI snapshot's reference/role/name fields. Locators
// and geometry always come from Playwright, never from model supplied selectors.
export function snapshotNodes(snapshot) {
  const nodes = [], ancestors = [];
  for (const line of snapshot.split("\n")) {
    const match = line.match(/^(\s*)- ([\w-]+)(?: ("(?:[^"\\]|\\.)*"))?/);
    if (!match) continue;
    const indent = match[1].length;
    while (ancestors.length && ancestors.at(-1).indent >= indent) ancestors.pop();
    const ref = line.match(/\[ref=((?:f\d+)?e\d+)\]/)?.[1];
    let name = "";
    try { name = match[3] ? JSON.parse(match[3]) : ""; } catch { /* malformed name stays empty */ }
    const node = { ariaRef: ref, role: match[2], name, ancestors: ancestors.slice(-5) };
    if (ref) nodes.push(node);
    const text = name || (node.role === "text" ? line.replace(/^\s*- text:\s*/, "").slice(0,240) : "");
    if (text) for (const ancestor of ancestors) {
      if (["row", "listitem", "group", "form", "dialog", "region"].includes(ancestor.role) && ancestor.summary.length < 8) ancestor.summary.push(text);
    }
    ancestors.push({ indent, role: node.role, label: name ? `${node.role} ${name}` : node.role, summary: [] });
  }
  return nodes.map(({ancestors,...node}) => ({...node,context:ancestors.map(a => [a.label,...new Set(a.summary)].join(" ").slice(0,600))}));
}

async function identity(locator, signal, omitOwnText = false) {
  return locator.evaluate((element, omitOwnText) => {
    const text = node => String(node.textContent || "").replace(/\s+/g, " ").trim().slice(0, 600);
    const attrs = node => [node.tagName, ...["id", "role", "aria-label", "name", "type", "href", "data-testid", "data-key"].map(key => node.getAttribute(key) || "")];
    const context = [];
    for (let parent = element.parentElement; parent && context.length < 5; parent = parent.parentElement || parent.getRootNode()?.host) {
      if (parent.matches("tr,li,[role=row],[role=listitem],dialog,[role=dialog],form,[data-key],[data-testid]")) {
        context.push([...attrs(parent), parent.matches("tr,li,[role=row],[role=listitem]") ? text(parent) : ""]);
      }
    }
    return JSON.stringify({ attrs: attrs(element), text: omitOwnText || element.matches("input,textarea,select,[contenteditable=true]") ? "" : text(element), context });
  }, omitOwnText, { timeout: 1500, signal });
}

async function stateOf(locator, signal) {
  return locator.evaluate(element => ({
    visible: !!(element.getClientRects().length && getComputedStyle(element).visibility !== "hidden"),
    enabled: !element.disabled && element.getAttribute("aria-disabled") !== "true",
    checked: typeof element.checked === "boolean" ? element.checked : element.hasAttribute("aria-checked") ? element.getAttribute("aria-checked") === "true" : undefined,
    selected: element.tagName === "SELECT" ? [...element.selectedOptions].map(option => option.value) : undefined,
    value: element.type !== "password" && "value" in element ? String(element.value).slice(0, 32768) : undefined,
    focused: element.getRootNode().activeElement === element,
    editable: !element.readOnly && (element.matches("input:not([type=checkbox]):not([type=radio]),textarea") || element.isContentEditable),
    text: element.type === "password" ? undefined : String(element.innerText || element.textContent || "").slice(0, 2000),
  }), undefined, { timeout: 1500, signal });
}

export class BrowserUI {
  constructor({ currentPage, pageID, tabList, screenshot }) {
    Object.assign(this, { currentPage, pageID, tabList, screenshot });
    this.session = randomUUID();
    this.references = new Map();
    this.pages = new WeakMap();
    this.snapshots = new Map();
    this.requests = new Map();
  }

  invalidate() { this.references.clear(); this.snapshots.clear(); }

  track(page) {
    if (this.pages.has(page)) return;
    const state = { epoch: 1, dialog: null };
    this.pages.set(page, state);
    page.on("framenavigated", () => { state.epoch++; });
    page.on("close", () => {
      state.epoch++;
      for (const [ref, item] of this.references) if (item.page === page) this.references.delete(ref);
    });
    page.on("dialog", dialog => { state.dialog = dialog; });
  }

  surface(page) {
    return { kind: "browser", id: this.pageID(page), url: page.url(), epoch: `${this.session}:${this.pages.get(page).epoch}` };
  }

  async resolve(ref, signal, recover = true, verifyText = false) {
    active(signal);
    const item = this.references.get(ref);
    if (!item || item.page.isClosed() || item.epoch !== this.surface(item.page).epoch || Date.now() - item.created > 10 * 60_000)
      throw fail("ui_stale_target", "Target expired or its frame navigated. Observe again.");
    let locator = item.locator;
    if (await locator.count() === 0) {
      if (!recover || item.recovered || !item.normalized || /\.(?:nth|first|last)\(/.test(String(item.normalized)) || await item.normalized.count() !== 1)
        throw fail("ui_stale_target", "Target detached; no unique replacement. Observe again.");
      locator = item.normalized;
      if (await identity(locator, signal) !== item.fingerprint) throw fail("ui_target_changed", "Replacement has different semantic context. Observe again.");
      // Bind the replacement to a fresh aria ref before acting, so positional
      // selectors produced by normalize cannot retarget after this check.
      const snap = await locator.ariaSnapshot({ mode: "ai", signal, timeout: 1500 });
      const replacement = snapshotNodes(snap)[0]?.ariaRef;
      if (!replacement) throw fail("ui_stale_target", "Replacement has no accessible identity");
      locator = item.page.locator(`aria-ref=${replacement}`);
      item.locator = locator;
      item.recovered = true;
    }
    if (await locator.count() !== 1 || await identity(locator, signal, verifyText) !== (verifyText ? item.structuralFingerprint : item.fingerprint))
      throw fail("ui_target_changed", "Target or its row context changed. Observe again.");
    return { ...item, locator };
  }

  async observe(params = {}, signal) {
    active(signal);
    if (params.list) return { surfaces: await this.tabList(), capabilities: ["browser-semantic", "browser-visual"], targets: [] };
    const page = await this.currentPage({ tabId: params.surfaceId || params.tabId });
    this.track(page);
    const surface = this.surface(page);
    const dialog = this.pages.get(page).dialog;
    if (dialog) return { observationId: randomUUID(), surface, timestamp: new Date().toISOString(), targets: [], dialog: { type: dialog.type(), message: dialog.message() }, capabilities: ["browser-dialog"] };
    surface.title = await page.title().catch(() => "");
    let snapshot, nodes, observationId;
    if (params.cursor) {
      const [id, offset] = String(params.cursor).split(":");
      const saved = this.snapshots.get(id);
      if (!saved || saved.surface.epoch !== surface.epoch || saved.surface.id !== surface.id || Date.now() - saved.created > 60_000)
        throw fail("ui_stale_observation", "Pagination expired. Observe again.");
      const root = saved.scopeRef ? (await this.resolve(saved.scopeRef, signal)).locator : page;
      if (await root.ariaSnapshot({ mode: "ai", boxes: true, signal, timeout: 10000 }) !== saved.snapshot)
        throw fail("ui_stale_observation", "Page changed during pagination. Observe again or narrow the search.");
      ({ snapshot, nodes, observationId } = saved);
      params = { ...params, offset: Number(offset) || 0 };
    } else {
      const root = params.scopeRef ? (await this.resolve(params.scopeRef, signal)).locator : page;
      snapshot = await root.ariaSnapshot({ mode: "ai", boxes: true, signal, timeout: 10000 });
      nodes = snapshotNodes(snapshot);
      if (params.search) {
        const search = String(params.search).toLowerCase();
        nodes = nodes.filter(node => [node.role, node.name, ...node.context].join(" ").toLowerCase().includes(search));
      }
      observationId = randomUUID();
      this.snapshots.set(observationId, { snapshot, nodes, observationId, surface, scopeRef: params.scopeRef, created: Date.now() });
      while (this.snapshots.size > 16) this.snapshots.delete(this.snapshots.keys().next().value);
    }
    const offset = params.offset || 0, limit = bounded(params.limit, 80, 200);
    const targets = [];
    for (const node of nodes.slice(offset, offset + limit)) {
      active(signal);
      const locator = page.locator(`aria-ref=${node.ariaRef}`);
      try {
        const fingerprint = await identity(locator, signal);
        const states = await stateOf(locator, signal);
        const normalized = await locator.normalize();
        const ref = `${observationId}/${node.ariaRef}`;
        const structuralFingerprint = await identity(locator, signal, true);
        this.references.set(ref, { page, locator, normalized, fingerprint, structuralFingerprint, epoch: surface.epoch, created: Date.now(), recovered: false });
        const semanticContext = JSON.parse(fingerprint).context.map(parts => parts.filter(Boolean).join(" "));
        targets.push({ ref, role: node.role, name: node.name, context: [...node.context, ...semanticContext].slice(-5), states,
          bounds: await locator.boundingBox(), actions: ["click", "focus", "press", "hover", "scroll", "drag", ...(states.editable ? ["fill"] : []), ...(states.checked !== undefined ? ["check"] : []), ...(states.selected ? ["select"] : [])] });
      } catch (error) { active(signal); /* inaccessible/transient snapshot node; visible text remains below */ }
    }
    while (this.references.size > 4096) this.references.delete(this.references.keys().next().value);
    const listedRefs = new Set(targets.map(target => target.ref));
    const annotated = snapshot.replace(/\[ref=((?:f\d+)?e\d+)\]/g, (_, ref) => {
      const scoped = `${observationId}/${ref}`;
      return listedRefs.has(scoped) ? `[ref=${scoped}]` : "[unlisted; search or paginate]";
    });
    const result = { observationId, surface, timestamp: new Date().toISOString(), targets, total: nodes.length,
      nextCursor: offset + limit < nodes.length ? `${observationId}:${offset + limit}` : undefined,
      // Full text is bounded and explicitly marked; pagination still covers all targets.
      accessibility: annotated.slice(0, 32000), textTruncated: annotated.length > 32000,
      dialog: this.pages.get(page).dialog ? { type: this.pages.get(page).dialog.type(), message: this.pages.get(page).dialog.message() } : undefined };
    if (params.screenshot) result.screenshot = await this.screenshot(page, signal);
    active(signal);
    if (surface.epoch !== this.surface(page).epoch) throw fail("ui_stale_observation", "Page navigated while observing. Observe again.");
    return result;
  }

  async verify(params, signal) {
    const page = await this.currentPage({ tabId: params.surfaceId });
    this.track(page);
    const predicate = params.expect;
    if (!predicate?.kind) throw fail("invalid_arguments", "An explicit verification predicate is required");
    const end = Date.now() + bounded(params.timeoutMs, 5000, 30000);
    let evidence;
    do {
      active(signal);
      let matched = false;
      const ref = predicate.ref || params.ref;
      if (predicate.kind === "url") { evidence = page.url(); matched = evidence === predicate.value; }
      else if (predicate.kind === "dialog") { evidence = this.pages.get(page).dialog?.message() || ""; matched = evidence === predicate.value; }
      else {
        if (!ref) throw fail("invalid_arguments", "This predicate requires an observed target ref");
        const item = this.references.get(ref);
        if (!item || item.page !== page) throw fail("ui_stale_target", "Verification target expired");
        const count = await item.locator.count();
        if (!count && predicate.kind === "hidden") { matched = true; evidence = "target detached"; }
        else if (count) {
          // A changed node must not satisfy a predicate intended for another row.
          const resolved = await this.resolve(ref, signal, false, predicate.kind === "text");
          const states = await stateOf(resolved.locator, signal);
          evidence = states;
          switch (predicate.kind) {
            case "visible": matched = states.visible; break;
            case "hidden": matched = !states.visible; break;
            case "text": matched = states.text === predicate.value; break;
            case "value": matched = states.value === predicate.value; break;
            case "checked": matched = states.checked === predicate.checked; break;
            case "selected": matched = JSON.stringify(states.selected) === JSON.stringify(predicate.values); break;
            case "focused": matched = states.focused; break;
            default: throw fail("invalid_arguments", "Unsupported browser verification predicate");
          }
        }
      }
      if (matched) return { status: "passed", method: "deterministic", evidence, timestamp: new Date().toISOString() };
      await delay(Math.min(100, Math.max(1, end - Date.now())), undefined, { signal });
    } while (Date.now() < end);
    return { status: "failed", method: "deterministic", evidence, timestamp: new Date().toISOString() };
  }

  async act(params, signal) {
    // Cache the promise before execution. A repeated transport request joins the
    // same operation, including unknown outcomes. The cache is runtime-local.
    const requestID = params.requestId;
    if (!requestID) throw fail("invalid_arguments", "UI actions require a request ID");
    const signature = JSON.stringify(params);
    const prior = this.requests.get(requestID);
    if (prior) {
      if (prior.signature !== signature) throw fail("ui_request_conflict", "Request ID was already used with different arguments");
      return prior.promise;
    }
    if (this.requests.size >= 10000) throw fail("ui_request_limit", "Runtime action journal is full; restart the runtime before more actions");
    const promise = this.execute(params, signal);
    this.requests.set(requestID, { signature, promise });
    return promise;
  }

  async execute(params, signal) {
    let started = false, page, item;
    const result = { execution: "not_started", verification: { status: "unverified", method: "none" }, backend: "browser", recovery: "none" };
    try {
      active(signal);
      page = await this.currentPage({ tabId: params.surfaceId });
      this.track(page);
      if (params.epoch && params.epoch !== this.surface(page).epoch) throw fail("ui_stale_target", "Surface navigated. Observe again.");
      const options = { signal, timeout: bounded(params.timeoutMs, 10000, 60000) };
      if (params.ref) {
        item = await this.resolve(params.ref, signal);
        if (item.page !== page) throw fail("ui_surface_mismatch", "Target belongs to a different tab");
        result.recovery = item.recovered ? "unique_semantic_replacement" : "none";
      }
      const locator = item?.locator;
      const action = params.action;
      const supported = ["click", "fill", "type", "check", "select", "focus", "press", "hover", "scroll", "drag", "dialog", "point"];
      if (!supported.includes(action)) throw fail("ui_unsupported_action", "Unsupported browser action");
      if (!locator && !["dialog", "point"].includes(action)) throw fail("invalid_arguments", "Action requires an observed target");
      if (locator && ["click", "check", "drag", "hover"].includes(action)) {
        await locator.click({ ...options, trial: true });
        await this.resolve(params.ref, signal, false);
      }
      let destination;
      if (action === "drag") {
        destination = await this.resolve(params.toRef, signal);
        if (destination.page !== page) throw fail("ui_surface_mismatch", "Drag destination belongs to another tab");
      }
      if (action === "point") {
        const viewport = page.viewportSize();
        if (!viewport || !Number.isFinite(params.x) || !Number.isFinite(params.y) || params.x < 0 || params.y < 0 || params.x >= viewport.width || params.y >= viewport.height)
          throw fail("invalid_arguments", "Point is outside the browser viewport");
        if (params.pointAction === "drag" && (!Number.isFinite(params.toX) || !Number.isFinite(params.toY) || params.toX < 0 || params.toY < 0 || params.toX >= viewport.width || params.toY >= viewport.height))
          throw fail("invalid_arguments", "Drag destination is outside the viewport");
        if (!["click", "hover", "type", "press", "scroll", "drag"].includes(params.pointAction || "click")) throw fail("invalid_arguments", "Unsupported physical action");
      }
      if ((action === "press" || params.pointAction === "press") && !params.key) throw fail("invalid_arguments", "A keyboard key is required");
      if (action === "dialog" && !this.pages.get(page).dialog) throw fail("ui_stale_target", "No dialog is open");
      active(signal);
      started = true;
      switch (action) {
        case "click": await locator.click({ ...options, button: params.button || "left", clickCount: params.clickCount || 1 }); break;
        case "fill": await locator.fill(String(params.text ?? ""), options); break;
        case "type": await locator.pressSequentially(String(params.text ?? ""), options); break;
        case "check": await locator.setChecked(params.checked !== false, options); break;
        case "select": await locator.selectOption(params.values || [], options); break;
        case "focus": await locator.focus(options); break;
        case "press": await locator.press(params.key, options); break;
        case "hover": await locator.hover(options); break;
        case "scroll": await locator.evaluate((element, delta) => element.scrollBy(delta.x, delta.y), { x: params.deltaX || 0, y: params.deltaY || 0 }, options); break;
        case "drag": await locator.dragTo(destination.locator, options); break;
        case "point": {
          const physical = params.pointAction || "click";
          await page.mouse.move(params.x, params.y);
          active(signal);
          if (physical === "drag") {
            await page.mouse.down();
            try { active(signal); await page.mouse.move(params.toX, params.toY, { steps: 10 }); }
            finally { await page.mouse.up(); }
          } else if (physical === "scroll") await page.mouse.wheel(params.deltaX || 0, params.deltaY || 0);
          else if (physical !== "hover") {
            const alreadyFocused = ["type", "press"].includes(physical) && await page.evaluate(({x,y}) => {
              let hit = document.elementFromPoint(x,y);
              while (hit?.shadowRoot) { const next = hit.shadowRoot.elementFromPoint(x,y); if (!next || next === hit) break; hit = next; }
              return hit && hit.getRootNode().activeElement === hit;
            }, {x:params.x,y:params.y});
            if (!alreadyFocused) await page.mouse.click(params.x, params.y, { button: params.button || "left", clickCount: params.clickCount || 1 });
            active(signal);
            if (physical === "type") await page.keyboard.insertText(String(params.text ?? ""));
            if (physical === "press") await page.keyboard.press(params.key);
          }
          break;
        }
        case "dialog": {
          const state = this.pages.get(page), dialog = state.dialog;
          if (params.accept) await dialog.accept(params.text); else await dialog.dismiss();
          state.dialog = null;
          break;
        }
      }
      result.execution = "completed";
      const intrinsic = { fill: { kind: "value", value: String(params.text ?? "") }, check: { kind: "checked", checked: params.checked !== false }, select: { kind: "selected", values: params.values || [] }, focus: { kind: "focused" } }[action];
      if (params.expect || intrinsic) {
        try { result.verification = await this.verify({ ...params, expect: params.expect || intrinsic, timeoutMs: params.verifyTimeoutMs || 5000 }, signal); }
        catch (error) { result.verification = { status: "unknown", method: "deterministic", code: error.code || "ui_verification_interrupted" }; }
      }
    } catch (error) {
      result.execution = started ? "unknown" : "not_started";
      result.error = { code: error.code || (signal?.aborted ? "ui_canceled" : "ui_action_failed"), message: error.code ? error.message : "Action did not complete; observe before deciding what to do next" };
    }
    if (page) result.surface = this.surface(page);
    result.timestamp = new Date().toISOString();
    return result;
  }
}
