interface InstallPrompt extends Event {
  prompt(): Promise<void>;
  userChoice: Promise<{ outcome: 'accepted' | 'dismissed' }>;
}

export function setupPWA(notice: (value: unknown, error?: boolean) => void) {
  let pending: InstallPrompt | null = null;
  let button: HTMLButtonElement | null = null;
  let installed = false;
  const standalone = window.matchMedia('(display-mode: standalone)');
  const update = () => { if (button) button.hidden = !pending || installed || standalone.matches; };
  window.addEventListener('beforeinstallprompt', event => {
    event.preventDefault();
    pending = event as InstallPrompt;
    update();
  });
  window.addEventListener('appinstalled', () => { installed = true; pending = null; update(); });
  standalone.addEventListener('change', update);

  // Vite development stays free of a worker; production includes the offline fallback.
  if (import.meta.env.PROD && window.isSecureContext && 'serviceWorker' in navigator) {
    void navigator.serviceWorker.register('/sw.js', { scope: '/', updateViaCache: 'none' })
      .catch(error => console.warn('Echo offline support could not start.', error));
  }

  return {
    mount(installButton: HTMLButtonElement) {
      button = installButton;
      button.onclick = async () => {
        const prompt = pending;
        if (!prompt) return;
        pending = null; update();
        try {
          await prompt.prompt();
          await prompt.userChoice;
        } catch { notice('Could not open installation. Try the install option in your browser menu.', true); }
      };
      update();
    }
  };
}
