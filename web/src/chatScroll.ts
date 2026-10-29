const bottomTolerance = 2;
const inputDirectionTolerance = 0.5;

export type ChatScrollFollower = {
  contentChanged(): void;
  /** Restore a thread's follow state after restoring its scroll position. */
  reset(following?: boolean): void;
  isFollowing(): boolean;
  /** Re-engages tail following and jumps to the bottom, e.g. after the user sends a message. */
  forceFollow(): void;
  dispose(): void;
};

function isAtBottom(element: HTMLElement): boolean {
  return element.scrollHeight - element.clientHeight - element.scrollTop <= bottomTolerance;
}

function isUpwardScrollKey(event: KeyboardEvent): boolean {
  return event.key === "ArrowUp"
    || event.key === "PageUp"
    || event.key === "Home"
    || (event.key === " " && event.shiftKey);
}

function isDownwardScrollKey(event: KeyboardEvent): boolean {
  return event.key === "ArrowDown"
    || event.key === "PageDown"
    || event.key === "End"
    || (event.key === " " && !event.shiftKey);
}

/**
 * Keeps a chat transcript at its tail until the user expresses upward scroll
 * intent. The controller never prevents native scrolling and never writes a
 * scroll position while in manual mode.
 */
export function createChatScrollFollower(element: HTMLElement): ChatScrollFollower {
  let following = false;
  let disposed = false;
  let frame = 0;
  let settleFrame = 0;
  let contentGeneration = 0;
  let layoutChangePending = false;
  let lastScrollTop = element.scrollTop;
  let lastTouchY: number | null = null;
  let pointerActive = false;
  let observedTail: Element | null = null;

  const cancelFrame = () => {
    if (frame) window.cancelAnimationFrame(frame);
    if (settleFrame) window.cancelAnimationFrame(settleFrame);
    frame = 0;
    settleFrame = 0;
    layoutChangePending = false;
  };

  const stopFollowing = () => {
    following = false;
    cancelFrame();
  };

  const contentChanged = () => {
    if (disposed || !following) return;
    contentGeneration++;
    layoutChangePending = true;
    if (frame) return;
    frame = window.requestAnimationFrame(() => {
      frame = 0;
      if (disposed || !following) return;
      element.scrollTop = element.scrollHeight;
      lastScrollTop = element.scrollTop;
      const settledGeneration = contentGeneration;
      if (settleFrame) window.cancelAnimationFrame(settleFrame);
      settleFrame = window.requestAnimationFrame(() => {
        settleFrame = 0;
        if (contentGeneration === settledGeneration) layoutChangePending = false;
      });
    });
  };

  const forceFollow = () => {
    if (disposed) return;
    following = true;
    element.scrollTop = element.scrollHeight;
    lastScrollTop = element.scrollTop;
    contentChanged();
  };

  const onScroll = () => {
    const current = element.scrollTop;
    const atBottom = isAtBottom(element);
    if (current < lastScrollTop) {
      // An upward wheel/key/touch gesture has already disabled following. A
      // pointer-held upward movement covers scrollbar drags and selection
      // autoscroll. Any remaining movement is a browser clamp after streamed
      // content changed height; retain following and correct the tail on the
      // next frame even when rounding temporarily leaves a small gap.
      if (!following || pointerActive || !layoutChangePending) stopFollowing();
      else if (!atBottom) contentChanged();
      lastScrollTop = current;
      return;
    }
    // A queued scroll event from our last write must not undo upward intent.
    if (current > lastScrollTop && atBottom) following = true;
    lastScrollTop = current;
  };

  const onWheel = (event: WheelEvent) => {
    if (event.deltaY < 0) {
      stopFollowing();
    } else if (event.deltaY > 0 && isAtBottom(element)) {
      following = true;
    }
  };

  const onKeyDown = (event: KeyboardEvent) => {
    if (event.defaultPrevented || (event.target instanceof Element
      && event.target.closest('input, textarea, select, [contenteditable]:not([contenteditable="false"])'))) return;
    if (isUpwardScrollKey(event)) {
      stopFollowing();
    } else if (event.key === 'End') {
      // End explicitly requests the tail. Re-engage before the browser's
      // asynchronous scroll event, which can arrive after another delta grows it.
      forceFollow();
    } else if (isDownwardScrollKey(event) && isAtBottom(element)) {
      following = true;
    }
  };

  const onTouchStart = (event: TouchEvent) => {
    lastTouchY = event.touches[0]?.clientY ?? null;
  };

  const onTouchMove = (event: TouchEvent) => {
    const nextY = event.touches[0]?.clientY;
    if (nextY == null) return;
    if (lastTouchY != null && nextY > lastTouchY + inputDirectionTolerance) stopFollowing();
    lastTouchY = nextY;
  };

  const clearTouch = () => { lastTouchY = null; };
  const onPointerDown = () => { pointerActive = true; };
  const clearPointer = () => { pointerActive = false; };

  const resizeObserver = typeof ResizeObserver === "function"
    ? new ResizeObserver(() => contentChanged())
    : null;

  const syncObservedTail = () => {
    if (!resizeObserver) return;
    const next = element.lastElementChild;
    if (next === observedTail) return;
    if (observedTail) resizeObserver.unobserve(observedTail);
    observedTail = next;
    if (observedTail) resizeObserver.observe(observedTail);
  };

  resizeObserver?.observe(element);
  syncObservedTail();
  const mutationObserver = new MutationObserver(() => {
    syncObservedTail();
    contentChanged();
  });
  mutationObserver.observe(element, { childList: true, characterData: true, subtree: true });

  element.addEventListener("scroll", onScroll, { passive: true });
  element.addEventListener("wheel", onWheel, { passive: true });
  element.addEventListener("keydown", onKeyDown);
  element.addEventListener("touchstart", onTouchStart, { passive: true });
  element.addEventListener("touchmove", onTouchMove, { passive: true });
  element.addEventListener("touchend", clearTouch, { passive: true });
  element.addEventListener("touchcancel", clearTouch, { passive: true });
  element.addEventListener("pointerdown", onPointerDown, { passive: true });
  window.addEventListener("pointerup", clearPointer, { passive: true });
  window.addEventListener("pointercancel", clearPointer, { passive: true });

  const reset = (follow = isAtBottom(element)) => {
    cancelFrame();
    lastScrollTop = element.scrollTop;
    following = follow;
    syncObservedTail();
  };

  reset();
  return {
    contentChanged,
    reset,
    isFollowing: () => following,
    forceFollow,
    dispose: () => {
      if (disposed) return;
      disposed = true;
      cancelFrame();
      mutationObserver.disconnect();
      resizeObserver?.disconnect();
      element.removeEventListener("scroll", onScroll);
      element.removeEventListener("wheel", onWheel);
      element.removeEventListener("keydown", onKeyDown);
      element.removeEventListener("touchstart", onTouchStart);
      element.removeEventListener("touchmove", onTouchMove);
      element.removeEventListener("touchend", clearTouch);
      element.removeEventListener("touchcancel", clearTouch);
      element.removeEventListener("pointerdown", onPointerDown);
      window.removeEventListener("pointerup", clearPointer);
      window.removeEventListener("pointercancel", clearPointer);
    },
  };
}
