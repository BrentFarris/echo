import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const api = vi.hoisted(() => ({
  get: vi.fn(async () => ({ settings: {} })),
}));

vi.mock("../js/api.js", () => ({ get: api.get }));

describe("plan question sounds", () => {
  let module: typeof import("./planQuestionSound");
  let play: ReturnType<typeof vi.fn>;

  beforeEach(async () => {
    vi.resetModules();
    api.get.mockReset();
    api.get.mockResolvedValue({ settings: {} });
    play = vi.fn(async () => undefined);
    class FakeAudio {
      preload = "";
      cloneNode() { return new FakeAudio(); }
      play() { return (play as () => Promise<void>)(); }
    }
    vi.stubGlobal("Audio", FakeAudio);
    module = await import("./planQuestionSound");
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it("plays by default when no setting is provided", () => {
    module.playPlanQuestionSound();
    expect(play).toHaveBeenCalledOnce();
  });

  it("plays when sounds are explicitly enabled", () => {
    module.updatePlanQuestionSoundSettings({ disablePlanQuestionSounds: false });
    module.playPlanQuestionSound();
    expect(play).toHaveBeenCalledOnce();
  });

  it("does not play when disabled", async () => {
    module.updatePlanQuestionSoundSettings({ disablePlanQuestionSounds: true });
    module.playPlanQuestionSound();
    expect(play).not.toHaveBeenCalled();
  });
});
