import { describe, expect, it, vi } from "vitest";
import { waitForReplacementServer } from "./rebuildRelaunch";

function response(instanceId: string, ok = true): Response {
  return {
    ok,
    json: async () => ({ ok: true, data: { instanceId } }),
  } as Response;
}

describe("replacement server wait", () => {
  it("waits through connection failures and the old process identity", async () => {
    const fetcher = vi.fn()
      .mockRejectedValueOnce(new Error("server offline"))
      .mockResolvedValueOnce(response("old-instance"))
      .mockResolvedValueOnce(response("new-instance"));
    const delay = vi.fn(async () => undefined);

    await waitForReplacementServer("old-instance", { fetcher, delay, intervalMs: 1, timeoutMs: 5_000 });

    expect(fetcher).toHaveBeenCalledTimes(3);
    expect(delay).toHaveBeenCalledTimes(2);
    expect(fetcher).toHaveBeenLastCalledWith("/api/health", { method: "GET", cache: "no-store" });
  });

  it("times out with an actionable message", async () => {
    await expect(waitForReplacementServer("old-instance", { timeoutMs: 0 }))
      .rejects.toThrow("Check the rebuild log");
  });
});
