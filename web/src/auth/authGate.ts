import { api } from "../../js/api.js";
import { clearSessions } from "../code/persistence";

type AuthStatus = {
  setupRequired: boolean;
  authenticated: boolean;
  transportSecure: boolean;
};

function escapeHTML(value: unknown): string {
  return String(value ?? "").replace(/[&<>"']/g, (character) => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
  }[character] ?? character));
}

function defaultDeviceName(): string {
  const platform = (navigator as Navigator & { userAgentData?: { platform?: string } }).userAgentData?.platform || navigator.platform || "Browser";
  return `${platform} browser`;
}

async function status(): Promise<AuthStatus> {
  return api("/api/auth/status", { method: "GET" }) as Promise<AuthStatus>;
}

export async function ensureAuthenticated(root: HTMLElement): Promise<void> {
  for (;;) {
    const current = await status();
    if (current.authenticated) return;
    await showAuthForm(root, current);
  }
}

function showAuthForm(root: HTMLElement, current: AuthStatus): Promise<void> {
  return new Promise((resolve) => {
    const setup = current.setupRequired;
    root.innerHTML = `
      <main class="auth-screen">
        <section class="auth-panel" aria-labelledby="auth-title">
          <div class="auth-mark" aria-hidden="true">E</div>
          <p class="auth-eyebrow">ECHO</p>
          <h1 id="auth-title">${setup ? "Secure this Echo server" : "Welcome back"}</h1>
          <p class="auth-description">${setup
            ? "Enter the one-time code printed in the Echo host console, then choose the owner password."
            : "Enter the owner password to access workspaces and conversations."}</p>
          ${current.transportSecure ? "" : `<div class="auth-warning"><strong>Trusted-LAN HTTP</strong><span>This connection is access-controlled but not encrypted. Use it only on a network you trust.</span></div>`}
          <form class="auth-form" data-auth-form>
            ${setup ? `<label>Setup code<input name="setupCode" required autocomplete="one-time-code" spellcheck="false"></label>` : ""}
            <label>Password<input name="password" type="password" required minlength="12" maxlength="128" autocomplete="${setup ? "new-password" : "current-password"}"></label>
            ${setup ? `<label>Confirm password<input name="confirmPassword" type="password" required minlength="12" maxlength="128" autocomplete="new-password"></label>` : ""}
            <label>Device name<input name="deviceName" maxlength="80" value="${escapeHTML(defaultDeviceName())}" autocomplete="off"></label>
            <p class="auth-error" role="alert" data-auth-error></p>
            <button type="submit" class="auth-submit">${setup ? "Finish setup" : "Sign in"}</button>
          </form>
        </section>
      </main>
    `;
    const form = root.querySelector<HTMLFormElement>("[data-auth-form]")!;
    const errorElement = root.querySelector<HTMLElement>("[data-auth-error]")!;
    const submit = form.querySelector<HTMLButtonElement>("button[type=submit]")!;
    form.addEventListener("submit", async (event) => {
      event.preventDefault();
      errorElement.textContent = "";
      const values = new FormData(form);
      const password = String(values.get("password") || "");
      if (setup && password !== String(values.get("confirmPassword") || "")) {
        errorElement.textContent = "Passwords do not match.";
        return;
      }
      submit.disabled = true;
      submit.textContent = setup ? "Securing Echo…" : "Signing in…";
      try {
        await api(setup ? "/api/auth/setup" : "/api/auth/login", {
          method: "POST",
          body: {
            ...(setup ? { setupCode: String(values.get("setupCode") || "") } : {}),
            password,
            deviceName: String(values.get("deviceName") || ""),
          },
        });
        resolve();
      } catch (error) {
        errorElement.textContent = error instanceof Error ? error.message : String(error);
        submit.disabled = false;
        submit.textContent = setup ? "Finish setup" : "Sign in";
      }
    });
    form.querySelector<HTMLInputElement>(setup ? "[name=setupCode]" : "[name=password]")?.focus();
  });
}

export async function logout(): Promise<void> {
  await api("/api/auth/logout", { method: "POST" });
  await clearSessions();
  window.dispatchEvent(new CustomEvent("echo:logged-out"));
}
