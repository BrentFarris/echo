// api.js — thin fetch wrapper around the Echo JSON API.
//
// Every endpoint returns the envelope {ok, data} or {ok, error}. This module
// centralizes request construction, JSON parsing, and error handling so views
// can call API functions directly.

/**
 * Perform a JSON request to the Echo API.
 *
 * @param {string} path API path, e.g. "/api/health".
 * @param {Omit<RequestInit, "body"> & {query?: Record<string, unknown>, body?: unknown}} [options] fetch options.
 * @returns {Promise<any>} the `data` field of the response envelope.
 * @throws {Error} on network failure or a non-ok envelope.
 */
export async function api(path, { query, body, ...rest } = {}) {
  let url = path;
  if (query) {
    const params = new URLSearchParams();
    for (const [k, v] of Object.entries(query)) {
      if (v !== undefined && v !== null) params.set(k, String(v));
    }
    const qs = params.toString();
    if (qs) url += (url.includes("?") ? "&" : "?") + qs;
  }

  const init = { ...rest };
  if (body !== undefined) {
    init.method = init.method || "POST";
    init.headers = { "Content-Type": "application/json", ...(init.headers || {}) };
    init.body = JSON.stringify(body);
  }

  let res;
  try {
    res = await fetch(url, init);
  } catch (err) {
    throw new Error(`Network error calling ${path}: ${err.message}`);
  }

  let payload;
  try {
    payload = await res.json();
  } catch {
    throw new Error(`Invalid JSON from ${path} (HTTP ${res.status})`);
  }

  if (!res.ok || payload.ok === false) {
    const message = payload?.error || `Request to ${path} failed (HTTP ${res.status})`;
    const error = new Error(message);
    error.status = res.status;
    error.payload = payload;
	if (res.status === 401 && path !== "/api/auth/status" && path !== "/api/auth/login") {
	  window.dispatchEvent(new CustomEvent("echo:unauthorized"));
	}
    throw error;
  }

  return payload.data;
}

// Convenience helpers for the most common verbs.
export const get = (path, opts) => api(path, { ...opts, method: "GET" });
export const post = (path, body, opts) => api(path, { ...opts, method: "POST", body });
export const put = (path, body, opts) => api(path, { ...opts, method: "PUT", body });
export const del = (path, opts) => api(path, { ...opts, method: "DELETE" });
