// device.ts — small device-capability helpers shared across the frontend.

/** True when the primary input is a coarse pointer (touchscreen) such as a
 *  phone or tablet. Detected via the same media query the CSS already uses
 *  for touch behaviors. Desktop machines with a real keyboard report fine, so
 *  this avoids the Shift+Enter-without-a-shift-key problem on mobile. */
export function isCoarsePointer(): boolean {
  if (typeof window.matchMedia !== "function") return false;
  return window.matchMedia("(pointer: coarse)").matches;
}
