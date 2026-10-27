type Options = { method?: string; body?: unknown; query?: Record<string, string>; signal?: AbortSignal; keepalive?: boolean };

// Preserve the editor's structured errors, including the disk snapshot used by
// the conflict dialog. The main chat API deliberately has a smaller contract.
export async function api(path: string, options: Options = {}): Promise<any> {
  if (path === '/api/settings') {
    const state = await api('/api/v1/state');
    const plugin = state.plugins.find((p: any) => p.id === 'monaco-editor');
    if (options.method === 'PUT') {
      const config = { ...plugin.config, ...(options.body as { settings?: object }).settings };
      if (Array.isArray(config.editorColumnGuides)) config.editorColumnGuides = config.editorColumnGuides.join(', ');
      return api('/api/v1/plugins/monaco-editor', { method: 'PATCH', body: { enabled: plugin.enabled, config } });
    }
    const settings = { ...plugin.config };
    if (typeof settings.editorColumnGuides === 'string') settings.editorColumnGuides = settings.editorColumnGuides.split(',').map(Number).filter((v: number) => v > 0);
    return { settings };
  }
  if (options.query) path += (path.includes('?') ? '&' : '?') + new URLSearchParams(options.query);
  const response = await fetch(path, { method: options.method || 'GET', signal: options.signal,
    keepalive: options.keepalive, headers: options.body === undefined ? {} : { 'Content-Type': 'application/json' },
    body: options.body === undefined ? undefined : JSON.stringify(options.body) });
  const payload = await response.json();
  if (!response.ok) throw Object.assign(new Error(payload.error || `HTTP ${response.status}`), { status: response.status, payload });
  return payload;
}
