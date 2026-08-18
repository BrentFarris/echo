import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({ api: vi.fn(), get: vi.fn() }));
vi.mock("../../js/api.js", () => mocks);

import {
  createSavedCommand, deleteSavedCommand, listSavedCommands, resizeTerminal,
  restartTerminal, startTerminal, stopTerminal, syncTerminal, updateSavedCommand, writeTerminal,
} from "./terminalApi";

beforeEach(() => {
  mocks.api.mockReset();
  mocks.get.mockReset();
});

describe("terminal API", () => {
  it("maps the complete shared-session lifecycle", async () => {
    const snapshot = { workspaceId: "a/b", id: "session", output: [] };
    mocks.api.mockResolvedValue(snapshot);
    await startTerminal("a/b", 80, 24);
    await syncTerminal("a/b", "session", 12);
    await writeTerminal("a/b", "session", "echo hello\r");
    await resizeTerminal("a/b", "session", 120, 40);
    await stopTerminal("a/b", "session");
    await restartTerminal("a/b", "session", 100, 30);

    expect(mocks.api.mock.calls).toEqual([
      ["/api/workspaces/a%2Fb/terminal/sessions", { method: "POST", body: { cols: 80, rows: 24 } }],
      ["/api/workspaces/a%2Fb/terminal/sessions/session?afterSequence=12", { method: "GET" }],
      ["/api/workspaces/a%2Fb/terminal/sessions/session/input", { method: "POST", body: { data: "echo hello\r" } }],
      ["/api/workspaces/a%2Fb/terminal/sessions/session/size", { method: "PUT", body: { cols: 120, rows: 40 } }],
      ["/api/workspaces/a%2Fb/terminal/sessions/session/stop", { method: "POST", body: {} }],
      ["/api/workspaces/a%2Fb/terminal/sessions/session/restart", { method: "POST", body: { cols: 100, rows: 30 } }],
    ]);
  });

  it("maps saved-command list, create, edit, and delete", async () => {
    const command = { id: "one", name: "Status", command: "git status", order: 0 };
    mocks.get.mockResolvedValueOnce({ commands: [command] });
    mocks.api
      .mockResolvedValueOnce({ command })
      .mockResolvedValueOnce({ command: { ...command, name: "Short" } })
      .mockResolvedValueOnce({ deleted: true });

    await expect(listSavedCommands("workspace")).resolves.toEqual([command]);
    await expect(createSavedCommand("workspace", "Status", "git status")).resolves.toEqual(command);
    await expect(updateSavedCommand("workspace", "one", "Short", "git status --short")).resolves.toMatchObject({ name: "Short" });
    await deleteSavedCommand("workspace", "one");

    expect(mocks.get).toHaveBeenCalledWith("/api/workspaces/workspace/terminal/saved-commands");
    expect(mocks.api.mock.calls).toEqual([
      ["/api/workspaces/workspace/terminal/saved-commands", { method: "POST", body: { name: "Status", command: "git status" } }],
      ["/api/workspaces/workspace/terminal/saved-commands/one", { method: "PUT", body: { name: "Short", command: "git status --short" } }],
      ["/api/workspaces/workspace/terminal/saved-commands/one", { method: "DELETE" }],
    ]);
  });
});
