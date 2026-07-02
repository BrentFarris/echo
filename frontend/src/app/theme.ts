import { llm } from "../../wailsjs/go/models";

export type ThemePaletteName = "light" | "dark";

export type ThemeToken = {
  key: string;
  label: string;
  cssVariable: string;
  group: string;
  defaults: Record<ThemePaletteName, string>;
};

type ThemeSettings = {
  light?: Record<string, string>;
  dark?: Record<string, string>;
};

type ThemeSettingsSource = {
  theme?: ThemeSettings | null;
};

const themeStyleID = "echo-theme-overrides";

export const themePaletteNames: ThemePaletteName[] = ["light", "dark"];

export const themeTokens: ThemeToken[] = [
  {
    key: "background",
    label: "Background",
    cssVariable: "--color-bg",
    group: "Base",
    defaults: { light: "#f7f3f1", dark: "#121214" },
  },
  {
    key: "surface",
    label: "Surface",
    cssVariable: "--color-surface",
    group: "Base",
    defaults: { light: "#fffdfa", dark: "#1b1b1e" },
  },
  {
    key: "surfaceMuted",
    label: "Muted Surface",
    cssVariable: "--color-surface-muted",
    group: "Base",
    defaults: { light: "#eee6e3", dark: "#252429" },
  },
  {
    key: "border",
    label: "Border",
    cssVariable: "--color-border",
    group: "Base",
    defaults: { light: "#d8ccc8", dark: "#343139" },
  },
  {
    key: "text",
    label: "Text",
    cssVariable: "--color-text",
    group: "Text",
    defaults: { light: "#241f1f", dark: "#f3eeee" },
  },
  {
    key: "textMuted",
    label: "Muted Text",
    cssVariable: "--color-text-muted",
    group: "Text",
    defaults: { light: "#6f6360", dark: "#b7aaab" },
  },
  {
    key: "accent",
    label: "Accent",
    cssVariable: "--color-accent",
    group: "Action",
    defaults: { light: "#2563eb", dark: "#60a5fa" },
  },
  {
    key: "accentStrong",
    label: "Strong Accent",
    cssVariable: "--color-accent-strong",
    group: "Action",
    defaults: { light: "#1d4ed8", dark: "#93bbfd" },
  },
  {
    key: "onAccent",
    label: "On Accent",
    cssVariable: "--color-on-accent",
    group: "Action",
    defaults: { light: "#ffffff", dark: "#ffffff" },
  },
  {
    key: "danger",
    label: "Danger",
    cssVariable: "--color-danger",
    group: "Status",
    defaults: { light: "#b42332", dark: "#ff6677" },
  },
  {
    key: "success",
    label: "Success",
    cssVariable: "--color-success",
    group: "Status",
    defaults: { light: "#1a7f37", dark: "#3fb950" },
  },
  {
    key: "info",
    label: "Info",
    cssVariable: "--color-info",
    group: "Status",
    defaults: { light: "#3c82e6", dark: "#58a6ff" },
  },
  {
    key: "warning",
    label: "Warning",
    cssVariable: "--color-warning",
    group: "Status",
    defaults: { light: "#9a6700", dark: "#d29922" },
  },
];

const themeTokenByKey = new Map(themeTokens.map((token) => [token.key, token]));

export const themeGroups = Array.from(new Set(themeTokens.map((token) => token.group)));

export function normalizeHexColor(value: string): string | null {
  const trimmed = value.trim();
  if (!/^#(?:[0-9a-fA-F]{3}|[0-9a-fA-F]{6})$/.test(trimmed)) {
    return null;
  }
  if (trimmed.length === 4) {
    return `#${trimmed[1]}${trimmed[1]}${trimmed[2]}${trimmed[2]}${trimmed[3]}${trimmed[3]}`.toLowerCase();
  }
  return trimmed.toLowerCase();
}

export function themeColorValue(
  settings: llm.Settings | null | undefined,
  palette: ThemePaletteName,
  token: ThemeToken,
): string {
  const override = themePalette(settings, palette)[token.key];
  const normalized = typeof override === "string" ? normalizeHexColor(override) : null;
  return normalized ?? token.defaults[palette];
}

export function settingsWithThemeColor(
  settings: llm.Settings,
  palette: ThemePaletteName,
  tokenKey: string,
  rawValue: string,
): llm.Settings {
  const source = cloneSettingsSource(settings);
  const theme = sourceTheme(source);
  const paletteValues = { ...(theme[palette] ?? {}) };
  const normalized = normalizeHexColor(rawValue);
  const token = themeTokenByKey.get(tokenKey);
  const storedValue = normalized ?? rawValue.trim();
  if (normalized && token && normalized === token.defaults[palette]) {
    delete paletteValues[tokenKey];
  } else {
    paletteValues[tokenKey] = storedValue;
  }
  theme[palette] = paletteValues;
  source.theme = compactTheme(theme);
  return llm.Settings.createFrom(source);
}

export function settingsWithThemeDefaults(settings: llm.Settings): llm.Settings {
  const source = cloneSettingsSource(settings);
  source.theme = { light: {}, dark: {} };
  return llm.Settings.createFrom(source);
}

export function settingsWithCompactTheme(settings: llm.Settings): llm.Settings {
  const source = cloneSettingsSource(settings);
  source.theme = compactTheme(sourceTheme(source));
  return llm.Settings.createFrom(source);
}

export function applyTheme(settings: llm.Settings | null | undefined) {
  const css = themeOverrideCSS(settings);
  const existing = document.getElementById(themeStyleID);
  if (!css) {
    existing?.remove();
    return;
  }
  const style =
    existing instanceof HTMLStyleElement
      ? existing
      : document.createElement("style");
  style.id = themeStyleID;
  style.textContent = css;
  if (!style.parentElement) {
    document.head.appendChild(style);
  }
}

function themeOverrideCSS(settings: llm.Settings | null | undefined): string {
  const light = themeDeclarations(settings, "light");
  const dark = themeDeclarations(settings, "dark");
  const parts: string[] = [];
  if (light.length) {
    parts.push(`:root {\n${light.join("\n")}\n}`);
  }
  if (dark.length) {
    parts.push(`@media (prefers-color-scheme: dark) {\n  :root {\n${dark.map((line) => `  ${line}`).join("\n")}\n  }\n}`);
  }
  return parts.join("\n\n");
}

function themeDeclarations(settings: llm.Settings | null | undefined, palette: ThemePaletteName): string[] {
  const overrides = themePalette(settings, palette);
  return themeTokens.flatMap((token) => {
    const normalized = normalizeHexColor(overrides[token.key] ?? "");
    if (!normalized || normalized === token.defaults[palette]) {
      return [];
    }
    return [`  ${token.cssVariable}: ${normalized};`];
  });
}

function compactTheme(theme: ThemeSettings): ThemeSettings {
  return {
    light: compactPalette(theme.light ?? {}, "light"),
    dark: compactPalette(theme.dark ?? {}, "dark"),
  };
}

function compactPalette(
  palette: Record<string, string>,
  name: ThemePaletteName,
): Record<string, string> {
  const compacted: Record<string, string> = {};
  for (const [key, value] of Object.entries(palette)) {
    const normalized = normalizeHexColor(String(value));
    const token = themeTokenByKey.get(key);
    if (normalized && token && normalized === token.defaults[name]) {
      continue;
    }
    compacted[key] = normalized ?? String(value).trim();
  }
  return compacted;
}

function themePalette(
  settings: llm.Settings | null | undefined,
  palette: ThemePaletteName,
): Record<string, string> {
  const theme = (settings as ThemeSettingsSource | null | undefined)?.theme;
  const values = theme?.[palette];
  return values && typeof values === "object" ? values : {};
}

function sourceTheme(source: Record<string, unknown>): ThemeSettings {
  const raw = (source.theme ?? {}) as ThemeSettings;
  return {
    light: { ...(raw.light ?? {}) },
    dark: { ...(raw.dark ?? {}) },
  };
}

function cloneSettingsSource(settings: llm.Settings): Record<string, unknown> {
  return JSON.parse(JSON.stringify(settings)) as Record<string, unknown>;
}
