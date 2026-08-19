type FetchLike = (input: RequestInfo | URL, init?: RequestInit) => Promise<Response>;

export interface ReplacementWaitOptions {
  fetcher?: FetchLike;
  intervalMs?: number;
  timeoutMs?: number;
  delay?: (milliseconds: number) => Promise<void>;
}

const defaultDelay = (milliseconds: number) => new Promise<void>((resolve) => {
  window.setTimeout(resolve, milliseconds);
});

// Wait for a healthy Echo process with a different startup identity. Checking
// identity instead of requiring an observed outage also handles very fast
// replacements and Vite's development proxy.
export async function waitForReplacementServer(previousInstanceId: string, options: ReplacementWaitOptions = {}): Promise<void> {
  const fetcher = options.fetcher || window.fetch.bind(window);
  const intervalMs = options.intervalMs ?? 500;
  const timeoutMs = options.timeoutMs ?? 120_000;
  const delay = options.delay || defaultDelay;
  const deadline = Date.now() + timeoutMs;

  while (Date.now() < deadline) {
    try {
      const response = await fetcher("/api/health", { method: "GET", cache: "no-store" });
      if (response.ok) {
        const payload = await response.json();
        const instanceId = payload?.data?.instanceId;
        if (instanceId && instanceId !== previousInstanceId) return;
      }
    } catch {
      // A connection failure is the expected middle of a successful restart.
    }
    await delay(intervalMs);
  }

  throw new Error("Echo did not return within two minutes. Check the rebuild log for details.");
}

export function reloadForReplacementServer(): void {
  window.location.reload();
}
