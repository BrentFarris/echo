import { llm } from "../../wailsjs/go/models";

// Shared LLM endpoint normalization and sync helpers.
//
// Settings.Endpoints + EndpointSelection are the modern source of truth; the
// top-level endpoint/model/generation/header fields are legacy mirrors of the
// selected Chat endpoint. Every SaveSettings call must run
// settingsWithEndpointSync first so the mirrors always match the selection —
// otherwise a stale mirror can overwrite an endpoint profile on save (see the
// chat model selector, which only changes EndpointSelection.Chat).

export const llmPresetFields = [
  "temperature",
  "topK",
  "topP",
  "minP",
  "contextLength",
  "maxTokens",
  "frequencyPenalty",
  "presencePenalty",
  "repetitionPenalty",
  "timeoutSeconds",
  "thinkingTokenBudget",
  "thinkingCorrection",
  "systemPromptAppendage",
] as const;

export type LLMPresetField = (typeof llmPresetFields)[number];
export type LLMPresetValues = Pick<llm.LLMEndpoint, LLMPresetField>;

const endpointTopics = [
  { key: "chat", label: "Chat" },
  { key: "research", label: "Research" },
  { key: "kanbanDecompose", label: "Kanban Decompose" },
  { key: "kanban", label: "Kanban" },
  { key: "inlineCode", label: "Inline code" },
] as const;

export type EndpointTopic = (typeof endpointTopics)[number]["key"];

export function settingsEndpoints(settings: llm.Settings | null | undefined): llm.LLMEndpoint[] {
  const saved = settings?.endpoints ?? [];
  const defaults = endpointDefaultsFromSettings(settings);
  if (saved.length) {
    return saved.map((endpoint, index) =>
      llm.LLMEndpoint.createFrom({
        ...defaults,
        id: endpoint.id || `endpoint-${index + 1}`,
        name: endpoint.name ?? `Endpoint ${index + 1}`,
        endpoint: endpoint.endpoint ?? "",
        model: endpoint.model ?? "",
        headers: endpoint.headers,
        ...endpointGenerationValues(endpoint, defaults),
      }),
    );
  }
  if (!settings) {
    return [];
  }
  return [
    llm.LLMEndpoint.createFrom({
      ...defaults,
      id: "default",
      name: "Default",
      endpoint: settings.endpoint ?? "",
      model: settings.model ?? "",
    }),
  ];
}

export function endpointDefaultsFromSettings(settings: llm.Settings | null | undefined): LLMPresetValues {
  return {
    temperature: numberOrDefault(settings?.temperature, 0.6),
    topK: numberOrDefault(settings?.topK, 20),
    topP: numberOrDefault(settings?.topP, 0.95),
    minP: numberOrDefault(settings?.minP, 0),
    contextLength: numberOrDefault(settings?.contextLength, 262144),
    maxTokens: numberOrDefault(settings?.maxTokens, 32168),
    frequencyPenalty: numberOrDefault(settings?.frequencyPenalty, 0),
    presencePenalty: numberOrDefault(settings?.presencePenalty, 1.5),
    repetitionPenalty: numberOrDefault(settings?.repetitionPenalty, 1.05),
    timeoutSeconds: numberOrDefault(settings?.timeoutSeconds, 600),
    thinkingTokenBudget: numberOrDefault(settings?.thinkingTokenBudget, -1),
    thinkingCorrection: settings?.thinkingCorrection === true,
    systemPromptAppendage: settings?.systemPromptAppendage ?? "",
  };
}

export function endpointGenerationValues(
  endpoint: llm.LLMEndpoint,
  defaults: LLMPresetValues,
): LLMPresetValues {
  return {
    temperature: numberOrDefault(endpoint.temperature, defaults.temperature),
    topK: numberOrDefault(endpoint.topK, defaults.topK),
    topP: numberOrDefault(endpoint.topP, defaults.topP),
    minP: numberOrDefault(endpoint.minP, defaults.minP),
    contextLength: numberOrDefault(endpoint.contextLength, defaults.contextLength),
    maxTokens: numberOrDefault(endpoint.maxTokens, defaults.maxTokens),
    frequencyPenalty: numberOrDefault(endpoint.frequencyPenalty, defaults.frequencyPenalty),
    presencePenalty: numberOrDefault(endpoint.presencePenalty, defaults.presencePenalty),
    repetitionPenalty: numberOrDefault(endpoint.repetitionPenalty, defaults.repetitionPenalty),
    timeoutSeconds: numberOrDefault(endpoint.timeoutSeconds, defaults.timeoutSeconds),
    thinkingTokenBudget: numberOrDefault(endpoint.thinkingTokenBudget, defaults.thinkingTokenBudget),
    thinkingCorrection:
      endpoint.thinkingCorrection === undefined
        ? defaults.thinkingCorrection
        : endpoint.thinkingCorrection === true,
    systemPromptAppendage: endpoint.systemPromptAppendage ?? defaults.systemPromptAppendage,
  };
}

export function numberOrDefault(value: number | undefined, fallback: number): number {
  return typeof value === "number" && !Number.isNaN(value) ? value : fallback;
}

export function endpointSelection(
  settings: llm.Settings | null | undefined,
  endpoints = settingsEndpoints(settings),
): Record<EndpointTopic, string> {
  const fallback = endpoints[0]?.id ?? "";
  const raw = settings?.endpointSelection;
  const kanban = validEndpointID(raw?.kanban, endpoints) ? raw!.kanban : fallback;
  return {
    chat: validEndpointID(raw?.chat, endpoints) ? raw!.chat : fallback,
    research: validEndpointID(raw?.research, endpoints)
      ? raw!.research
      : validEndpointID(raw?.chat, endpoints) ? raw!.chat : fallback,
    kanbanDecompose: validEndpointID(raw?.kanbanDecompose, endpoints)
      ? raw!.kanbanDecompose
      : kanban,
    kanban,
    inlineCode: validEndpointID(raw?.inlineCode, endpoints) ? raw!.inlineCode : fallback,
  };
}

export function validEndpointID(id: string | undefined, endpoints: llm.LLMEndpoint[]): id is string {
  return Boolean(id && endpoints.some((endpoint) => endpoint.id === id));
}

// settingsWithEndpointSync rewrites the legacy top-level fields (endpoint,
// model, generation values, headers) so they mirror the selected Chat
// endpoint. Call this before every SaveSettings so a stale mirror can never
// overwrite an endpoint profile on the backend.
export function settingsWithEndpointSync(settings: llm.Settings): llm.Settings {
  const source = settingsSource(settings);
  const endpoints = settingsEndpoints(settings);
  const selection = endpointSelection(settings, endpoints);
  const chatEndpoint =
    endpoints.find((endpoint) => endpoint.id === selection.chat) ?? endpoints[0];
  source.endpoints = endpoints;
  source.endpointSelection = selection;
  source.endpoint = chatEndpoint?.endpoint ?? "";
  source.model = chatEndpoint?.model ?? "";
  for (const field of llmPresetFields) {
    source[field] = chatEndpoint?.[field] ?? endpointDefaultsFromSettings(settings)[field];
  }
  source.headers = chatEndpoint?.headers;
  return llm.Settings.createFrom(source);
}

export function settingsWithEndpointDraft(
  settings: llm.Settings,
  endpoints: llm.LLMEndpoint[],
  selection = endpointSelection(settings, endpoints),
): llm.Settings {
  const source = settingsSource(settings);
  source.endpoints = endpoints;
  source.endpointSelection = selection;
  return settingsWithEndpointSync(llm.Settings.createFrom(source));
}

function settingsSource(settings: llm.Settings): Record<string, unknown> {
  return JSON.parse(JSON.stringify(settings)) as Record<string, unknown>;
}
