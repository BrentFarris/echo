import { afterEach, describe, expect, it, vi } from "vitest";
import { installPrimaryNavShortcuts, primaryNavShortcut } from "./primaryNavShortcuts";

afterEach(() => { document.body.innerHTML = ""; });

describe("primary navigation shortcuts", () => {
  it.each([
    ["Digit1", "1", "chat"],
    ["Digit2", "2", "code"],
    ["Digit3", "3", "search"],
    ["Digit4", "4", "git"],
    ["Digit5", "5", "sandbox"],
    ["Digit0", "0", "settings"],
    ["Slash", "/", "map"],
  ])("maps Ctrl+%s to %s", (code, key, nav) => {
    const event = new KeyboardEvent("keydown", { code, key, ctrlKey: true });
    expect(primaryNavShortcut(event)).toBe(nav);
  });

  it("ignores modified, repeated, and composing key presses", () => {
    expect(primaryNavShortcut(new KeyboardEvent("keydown", { code: "Digit1", ctrlKey: true, shiftKey: true }))).toBeNull();
    expect(primaryNavShortcut(new KeyboardEvent("keydown", { code: "Digit1", ctrlKey: true, altKey: true }))).toBeNull();
    expect(primaryNavShortcut(new KeyboardEvent("keydown", { code: "Digit1", ctrlKey: true, metaKey: true }))).toBeNull();
    expect(primaryNavShortcut(new KeyboardEvent("keydown", { code: "Digit1", ctrlKey: true, repeat: true }))).toBeNull();
    expect(primaryNavShortcut(new KeyboardEvent("keydown", { code: "Digit1", ctrlKey: true, isComposing: true }))).toBeNull();
  });

  it("clicks the mounted view control and consumes the shortcut", () => {
    document.body.innerHTML = '<button data-nav="search">Search</button>';
    const click = vi.fn();
    document.querySelector("button")?.addEventListener("click", click);
    const dispose = installPrimaryNavShortcuts();
    const event = new KeyboardEvent("keydown", { code: "Digit3", key: "3", ctrlKey: true, bubbles: true, cancelable: true });

    document.dispatchEvent(event);

    expect(click).toHaveBeenCalledOnce();
    expect(event.defaultPrevented).toBe(true);
    dispose();
  });

  it("leaves browser shortcuts alone when the current view has no matching control", () => {
    const dispose = installPrimaryNavShortcuts();
    const event = new KeyboardEvent("keydown", { code: "Digit5", key: "5", ctrlKey: true, bubbles: true, cancelable: true });

    document.dispatchEvent(event);

    expect(event.defaultPrevented).toBe(false);
    dispose();
  });
});
