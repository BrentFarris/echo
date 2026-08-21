export type PluginSource = {
  type: "local" | "github" | "builtin";
  repository?: string;
  ref?: string;
  commit?: string;
  subdirectory?: string;
  path?: string;
  builtin?: string;
};

export type PluginDimensions = { width?: number; height?: number };

export type PluginView = {
  pluginId: string;
  id: string;
  kind: "page" | "floating";
  title: string;
  icon?: string;
  defaultSize?: PluginDimensions;
  minimumSize?: PluginDimensions;
};

export type PluginPermission = { name: string; reason: string; hosts?: string[] };

export type PluginSetting = {
  key: string;
  type: "string" | "url" | "number" | "boolean" | "select" | "secret";
  scope: "global" | "workspace";
  label: string;
  help?: string;
  required?: boolean;
  value?: unknown;
  configured?: boolean;
  secretSource?: string;
  minimum?: number;
  maximum?: number;
  pattern?: string;
  options?: Array<{ value: string; label: string }>;
};

export type CatalogPlugin = {
  id: string;
  name: string;
  version: string;
  description?: string;
  digest: string;
	revision?: string;
  source: PluginSource;
  globalEnabled: boolean;
  workspaceEnabled: boolean;
  effective: boolean;
  compatible: boolean;
  health?: string;
  permissions?: PluginPermission[];
  approvedTools?: string[];
  views?: PluginView[];
  tools?: Array<{ name: string; description: string; readOnly?: boolean; mutating?: boolean }>;
  settings?: PluginSetting[];
};

export type PluginStage = {
  id: string;
  createdAt: string;
  source: PluginSource;
  previousDigest?: string;
  validation: {
    compatible: boolean;
    target: string;
    digest: string;
    manifest: {
      id: string;
      name: string;
      version: string;
      description?: string;
      permissions?: PluginPermission[];
      contributes?: {
		tools?: Array<{ name: string; description: string; method: string; inputSchema: Record<string, unknown>; outputSchema?: Record<string, unknown>; timeoutSeconds?: number; readOnly?: boolean; mutating?: boolean }>;
		settings?: Array<{ key: string; label: string; scope: "global" | "workspace"; type: PluginSetting["type"] }>;
	  };
      runtime?: unknown;
    };
  };
};

export type PluginCatalog = {
  safeMode: boolean;
  credentialStoreAvailable?: boolean;
  plugins: CatalogPlugin[];
  stages: PluginStage[];
  missing?: Array<{ id: string; source: PluginSource; enabled: boolean }>;
  conflicts?: Array<{ id: string; source: PluginSource; enabled: boolean }>;
  retained?: Array<{ id: string; name: string; version: string }>;
};

export type PluginUISession = {
  id: string;
  bridgeToken: string;
  nonce: string;
  pluginId: string;
  pluginName: string;
  viewId: string;
  viewTitle: string;
  viewKind: "page" | "floating";
  workspaceId?: string;
  digest: string;
	revision?: string;
  entryUrl: string;
  expiresAt: string;
  config?: Record<string, unknown>;
};
