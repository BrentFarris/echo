// protocolLaunch.ts — handling for launches through the echo: URL scheme.
//
// When the installed Echo PWA is opened via echo://open?port=..., the web app
// manifest maps the URL to /?echo=<encoded echo url>. The SPA strips the
// parameter so the hash router sees a clean URL; if the deep link points at a
// different Echo port than the one this PWA was installed from (a custom
// -port), the browser is redirected there instead.

export type ProtocolLaunch = {
  /** Absolute URL to redirect to, when the deep link targets another port. */
  redirect: string | null;
  /** The page path with the echo parameter removed. */
  cleanedSearch: string;
};

/**
 * Resolve a protocol-handler launch from the current query string.
 * Pure so it can be unit-tested; `applyProtocolLaunch` performs the side
 * effects.
 */
export function resolveProtocolLaunch(search: string, currentPort: string): ProtocolLaunch {
  const params = new URLSearchParams(search);
  const echoed = params.get("echo");
  if (!echoed) {
    return { redirect: null, cleanedSearch: search };
  }
  params.delete("echo");
  const cleanedSearch = params.toString();

  let port: string | null = null;
  try {
    port = new URL(echoed).searchParams.get("port");
  } catch {
    port = null;
  }
  if (port && /^\d+$/.test(port) && port !== currentPort) {
    // The PWA was installed from a different Echo origin; follow the link.
    return { redirect: `http://localhost:${port}/`, cleanedSearch };
  }
  return { redirect: null, cleanedSearch };
}

/** Apply a protocol-handler launch, if the current URL carries one. */
export function applyProtocolLaunch(): void {
  const { redirect, cleanedSearch } = resolveProtocolLaunch(window.location.search, window.location.port);
  if (redirect) {
    window.location.replace(redirect + window.location.hash);
    return;
  }
  if (new URLSearchParams(window.location.search).has("echo")) {
    window.history.replaceState(
      null,
      "",
      window.location.pathname + (cleanedSearch ? `?${cleanedSearch}` : "") + window.location.hash,
    );
  }
}
