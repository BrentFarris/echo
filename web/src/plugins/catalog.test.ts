import { beforeEach, describe, expect, it, vi } from "vitest";

const api = vi.hoisted(() => ({ get: vi.fn() }));

vi.mock("../../js/api.js", () => api);

import { clearPluginCatalog, getPluginCatalog, refreshPluginCatalog } from "./catalog";

const emptyCatalog = { safeMode: false, plugins: [], stages: [{ id: "calculator-stage" }] };
const installedCatalog = {
	safeMode: false,
	stages: [],
	plugins: [{
		id: "calculator", name: "Calculator", version: "1.0.0", digest: "calculator-digest",
		source: { type: "builtin", builtin: "calculator" }, globalEnabled: true,
		workspaceEnabled: false, effective: true, compatible: true, permissions: [],
		views: [{ pluginId: "calculator", id: "calculator", kind: "floating", title: "Calculator" }],
	}],
};

beforeEach(() => {
	clearPluginCatalog();
	api.get.mockReset();
});

describe("plugin catalog refresh", () => {
	it("fetches again when a plugin change arrives during an in-flight refresh", async () => {
		let resolveStaged!: (value: typeof emptyCatalog) => void;
		const stagedResponse = new Promise<typeof emptyCatalog>((resolve) => { resolveStaged = resolve; });
		let pluginRequests = 0;
		api.get.mockImplementation(async (path: string) => {
			if (path === "/api/workspaces") return { activeId: "workspace-one" };
			pluginRequests++;
			return pluginRequests === 1 ? stagedResponse : installedCatalog;
		});

		const stageRefresh = refreshPluginCatalog();
		await vi.waitFor(() => expect(pluginRequests).toBe(1));
		const approvalRefresh = refreshPluginCatalog();
		resolveStaged(emptyCatalog);

		await Promise.all([stageRefresh, approvalRefresh]);

		expect(pluginRequests).toBe(2);
		expect(getPluginCatalog().plugins.map((plugin) => plugin.id)).toEqual(["calculator"]);
	});
});
