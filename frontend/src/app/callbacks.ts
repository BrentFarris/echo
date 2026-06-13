
import type { AppCallbacks } from "./types";

let callbacks: AppCallbacks | null = null;

export function setAppCallbacks(next: AppCallbacks) {
  callbacks = next;
}

export function getAppCallbacks(): AppCallbacks {
  if (!callbacks) {
    throw new Error("Echo app callbacks were used before initialization.");
  }
  return callbacks;
}
