export const maxGuideColumn = 10_000;
export const guideColumnsError = "Enter comma-separated whole numbers from 1 to 10,000, such as 80, 90.";

export type ColumnGuideSettings = {
  editorColumnGuidesEnabled?: unknown;
  editorColumnGuides?: unknown;
};

function validColumn(value: unknown): value is number {
  return typeof value === "number" && Number.isInteger(value) && value >= 1 && value <= maxGuideColumn;
}

function sortedColumns(columns: number[]): number[] {
  return [...new Set(columns)].sort((a, b) => a - b);
}

export function columnGuideSettings(settings: ColumnGuideSettings) {
  const columns = settings.editorColumnGuides;
  return {
    editorColumnGuidesEnabled: settings.editorColumnGuidesEnabled === true,
    editorColumnGuides: Array.isArray(columns) && columns.length > 0 && columns.every(validColumn)
      ? sortedColumns(columns) : [80, 90],
  };
}

export function parseGuideColumns(text: string): number[] | null {
  const parts = text.split(",").map((part) => part.trim());
  if (!parts.every((part) => /^\d+$/.test(part))) return null;
  const columns = parts.map(Number);
  return columns.every(validColumn) ? sortedColumns(columns) : null;
}

export function columnGuideRulers(settings: ColumnGuideSettings): number[] {
  const normalized = columnGuideSettings(settings);
  return normalized.editorColumnGuidesEnabled ? normalized.editorColumnGuides : [];
}
