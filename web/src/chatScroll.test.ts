import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { createChatScrollFollower, type ChatScrollFollower } from "./chatScroll";

type ScrollMetrics = {
  height: number;
  client: number;
  position: number;
  writes: number;
  userScrollTo(value: number): void;
};

class TestResizeObserver {
  static instances: TestResizeObserver[] = [];
  readonly observed = new Set<Element>();

  constructor(private readonly callback: ResizeObserverCallback) {
    TestResizeObserver.instances.push(this);
  }

  observe(target: Element): void { this.observed.add(target); }
  unobserve(target: Element): void { this.observed.delete(target); }
  disconnect(): void { this.observed.clear(); }
  trigger(): void { this.callback([], this as unknown as ResizeObserver); }
}

function setScrollMetrics(element: HTMLElement, initial: Partial<ScrollMetrics> = {}): ScrollMetrics {
  const metrics: ScrollMetrics = {
    height: initial.height ?? 1000,
    client: initial.client ?? 200,
    position: initial.position ?? 0,
    writes: 0,
    userScrollTo(value: number) { metrics.position = value; },
  };
  Object.defineProperties(element, {
    scrollHeight: { configurable: true, get: () => metrics.height },
    clientHeight: { configurable: true, get: () => metrics.client },
    scrollTop: {
      configurable: true,
      get: () => metrics.position,
      set: (value: number) => {
        metrics.writes++;
        metrics.position = Math.max(0, Math.min(Number(value) || 0, Math.max(0, metrics.height - metrics.client)));
      },
    },
  });
  return metrics;
}

function key(element: HTMLElement, value: string, options: KeyboardEventInit = {}): void {
  element.dispatchEvent(new KeyboardEvent("keydown", { key: value, bubbles: true, ...options }));
}

function touch(element: HTMLElement, type: string, clientY: number): void {
  const event = new Event(type, { bubbles: true });
  Object.defineProperty(event, "touches", { value: type === "touchend" ? [] : [{ clientY }] });
  element.dispatchEvent(event);
}

describe("chat scroll following", () => {
  let frames: Map<number, FrameRequestCallback>;
  let nextFrame: number;
  let followers: ChatScrollFollower[];

  const flushFrames = () => {
    const pending = [...frames.values()];
    frames.clear();
    for (const callback of pending) callback(performance.now());
  };

  beforeEach(() => {
    frames = new Map();
    nextFrame = 1;
    followers = [];
    TestResizeObserver.instances = [];
    vi.stubGlobal("ResizeObserver", TestResizeObserver);
    vi.spyOn(window, "requestAnimationFrame").mockImplementation((callback) => {
      const id = nextFrame++;
      frames.set(id, callback);
      return id;
    });
    vi.spyOn(window, "cancelAnimationFrame").mockImplementation((id) => { frames.delete(id); });
  });

  afterEach(() => {
    for (const follower of followers) follower.dispose();
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
    document.body.replaceChildren();
  });

  const create = (element: HTMLElement) => {
    const follower = createChatScrollFollower(element);
    followers.push(follower);
    return follower;
  };

  it("coalesces content growth and follows the tail when already at the bottom", () => {
    const log = document.createElement("div");
    const metrics = setScrollMetrics(log, { position: 800 });
    const follower = create(log);

    metrics.height = 1200;
    follower.contentChanged();
    follower.contentChanged();
    expect(frames).toHaveLength(1);
    flushFrames();

    expect(metrics.position).toBe(1000);
    expect(metrics.writes).toBe(1);
    expect(follower.isFollowing()).toBe(true);
  });

  it("does not jump when long history opens away from the bottom, but follows content that initially fits", () => {
    const longLog = document.createElement("div");
    const longMetrics = setScrollMetrics(longLog, { position: 0 });
    const longFollower = create(longLog);
    longMetrics.height = 1200;
    longFollower.contentChanged();
    flushFrames();
    expect(longMetrics.position).toBe(0);
    expect(longMetrics.writes).toBe(0);

    const shortLog = document.createElement("div");
    const shortMetrics = setScrollMetrics(shortLog, { height: 150, client: 200, position: 0 });
    const shortFollower = create(shortLog);
    shortMetrics.height = 300;
    shortFollower.contentChanged();
    flushFrames();
    expect(shortMetrics.position).toBe(100);
  });

  it("cancels a pending follow immediately on upward wheel intent", () => {
    const log = document.createElement("div");
    const metrics = setScrollMetrics(log, { position: 800 });
    const follower = create(log);
    metrics.height = 1200;
    follower.contentChanged();

    log.dispatchEvent(new WheelEvent("wheel", { deltaY: -1, bubbles: true }));
    flushFrames();
    expect(follower.isFollowing()).toBe(false);
    expect(metrics.position).toBe(800);
    expect(metrics.writes).toBe(0);
  });

  it("treats scrollbar movement as manual until a downward scroll reaches the bottom", () => {
    const log = document.createElement("div");
    const metrics = setScrollMetrics(log, { position: 800 });
    const follower = create(log);

    log.dispatchEvent(new Event("pointerdown", { bubbles: true }));
    metrics.userScrollTo(700);
    log.dispatchEvent(new Event("scroll"));
    window.dispatchEvent(new Event("pointerup"));
    expect(follower.isFollowing()).toBe(false);

    metrics.userScrollTo(760);
    log.dispatchEvent(new Event("scroll"));
    expect(follower.isFollowing()).toBe(false);
    metrics.userScrollTo(800);
    log.dispatchEvent(new Event("scroll"));
    expect(follower.isFollowing()).toBe(true);

    metrics.height = 1100;
    follower.contentChanged();
    flushFrames();
    expect(metrics.position).toBe(900);
  });

  it("cancels for every upward keyboard gesture and lets End re-enable at the tail", () => {
    const log = document.createElement("div");
    const metrics = setScrollMetrics(log, { position: 800 });
    const follower = create(log);

    for (const [value, options] of [
      ["ArrowUp", {}], ["PageUp", {}], ["Home", {}], [" ", { shiftKey: true }],
    ] as Array<[string, KeyboardEventInit]>) {
      follower.reset();
      key(log, value, options);
      expect(follower.isFollowing(), value).toBe(false);
    }

    key(log, "End");
    expect(follower.isFollowing()).toBe(true);
    metrics.height = 1100;
    follower.contentChanged();
    flushFrames();
    expect(metrics.position).toBe(900);
  });

  it("Page Down re-enables only after native scrolling reaches the bottom", () => {
    const log = document.createElement("div");
    const metrics = setScrollMetrics(log, { position: 400 });
    const follower = create(log);
    expect(follower.isFollowing()).toBe(false);

    key(log, "PageDown");
    metrics.userScrollTo(700);
    log.dispatchEvent(new Event("scroll"));
    expect(follower.isFollowing()).toBe(false);
    metrics.userScrollTo(800);
    log.dispatchEvent(new Event("scroll"));
    expect(follower.isFollowing()).toBe(true);
  });

  it("cancels on an upward touch gesture", () => {
    const log = document.createElement("div");
    const metrics = setScrollMetrics(log, { position: 800 });
    const follower = create(log);
    metrics.height = 1100;
    follower.contentChanged();

    touch(log, "touchstart", 100);
    touch(log, "touchmove", 120);
    flushFrames();
    expect(follower.isFollowing()).toBe(false);
    expect(metrics.writes).toBe(0);
  });

  it("keeps following when streamed layout shrinkage clamps the browser upward", () => {
    const log = document.createElement("div");
    const metrics = setScrollMetrics(log, { height: 1200, position: 1000 });
    const follower = create(log);

    follower.contentChanged();
    metrics.height = 1104;
    metrics.userScrollTo(900);
    log.dispatchEvent(new Event("scroll"));
    expect(follower.isFollowing()).toBe(true);
    flushFrames();
    expect(metrics.position).toBe(904);
  });

  it("treats an otherwise unclassified upward scroll event as user control", () => {
    const log = document.createElement("div");
    const metrics = setScrollMetrics(log, { position: 800 });
    const follower = create(log);

    metrics.userScrollTo(700);
    log.dispatchEvent(new Event("scroll"));
    expect(follower.isFollowing()).toBe(false);
    metrics.height = 1100;
    follower.contentChanged();
    flushFrames();
    expect(metrics.position).toBe(700);
  });

  it("follows delayed resize-driven growth and stops all work after disposal", () => {
    const log = document.createElement("div");
    const metrics = setScrollMetrics(log, { position: 800 });
    const tail = document.createElement("div");
    log.append(tail);
    const follower = create(log);
    const observer = TestResizeObserver.instances[0];
    expect(observer.observed.has(log)).toBe(true);
    expect(observer.observed.has(tail)).toBe(true);

    metrics.height = 1200;
    observer.trigger();
    flushFrames();
    expect(metrics.position).toBe(1000);

    metrics.height = 1300;
    follower.contentChanged();
    follower.dispose();
    flushFrames();
    expect(metrics.position).toBe(1000);
    expect(observer.observed.size).toBe(0);
  });
});
