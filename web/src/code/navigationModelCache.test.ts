import { describe, expect, it } from "vitest";
import { NavigationModelCache } from "./navigationModelCache";

type Model = { id: string; disposed: boolean; dispose(): void };
const model = (id: string): Model => ({ id, disposed: false, dispose() { this.disposed = true; } });

describe("NavigationModelCache", () => {
  it("deduplicates loads and promotes a model without disposing it", async () => {
    const cache = new NavigationModelCache<Model>();
    let loads = 0;
    const created = model("target");
    const load = async () => { loads++; return created; };
    const [first, second] = await Promise.all([
      cache.ensure("file:///target.go", () => null, load),
      cache.ensure("file:///target.go", () => null, load),
    ]);
    expect(first).toBe(true);
    expect(second).toBe(true);
    expect(loads).toBe(1);
    expect(cache.take("file:///target.go")).toBe(created);
    expect(created.disposed).toBe(false);
  });

  it("bounds concurrent loads", async () => {
    const cache = new NavigationModelCache<Model>(2);
    let active = 0;
    let peak = 0;
    const release: Array<() => void> = [];
    const loads = Array.from({ length: 5 }, (_, index) => cache.ensure(`file:///${index}.go`, () => null, async () => {
      active++;
      peak = Math.max(peak, active);
      await new Promise<void>((resolve) => release.push(resolve));
      active--;
      return model(String(index));
    }));
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(active).toBe(2);
    while (release.length || active) {
      release.splice(0).forEach((resolve) => resolve());
      await new Promise((resolve) => setTimeout(resolve, 0));
    }
    await Promise.all(loads);
    expect(peak).toBe(2);
  });

  it("disposes invalidated and remaining models", async () => {
    const cache = new NavigationModelCache<Model>();
    const first = model("first");
    const second = model("second");
    await cache.ensure("first", () => null, async () => first);
    await cache.ensure("second", () => null, async () => second);
    cache.invalidate((key) => key === "first");
    expect(first.disposed).toBe(true);
    expect(second.disposed).toBe(false);
    cache.dispose();
    expect(second.disposed).toBe(true);
  });
});
