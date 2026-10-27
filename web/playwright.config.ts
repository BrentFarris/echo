import { defineConfig } from '@playwright/test';
const binary = (name: string) => process.platform === 'win32' ? `..\\${name}.exe` : `../${name}`;
export default defineConfig({
  testDir: './e2e', timeout: 45_000, workers: 1, fullyParallel: false,
  use: { baseURL: 'http://127.0.0.1:3742', headless: true, screenshot: 'only-on-failure', trace: 'retain-on-failure' },
  webServer: [
    { command: `${binary('browser-fixture')} -data ../.test-data/browser -password echo-test-password`, url: 'http://127.0.0.1:3742', reuseExistingServer: false, timeout: 30_000 },
    { command: `${binary('browser-fixture')} -data ../.test-data/swarm-linux -port 3744 -name linux -password echo-test-password`, url: 'http://127.0.0.1:3744', reuseExistingServer: false, timeout: 30_000 },
    { command: `${binary('browser-fixture')} -data ../.test-data/swarm-mac -port 3745 -name mac -password echo-test-password`, url: 'http://127.0.0.1:3745', reuseExistingServer: false, timeout: 30_000 },
    { command: binary('mock-model'), url: 'http://127.0.0.1:3743/v1/models', reuseExistingServer: false, timeout: 30_000 }
  ]
});
