import { defineConfig } from '@playwright/test';
export default defineConfig({
  testDir: './e2e', timeout: 45_000, workers: 1, fullyParallel: false,
  use: { baseURL: 'http://127.0.0.1:3742', headless: true, screenshot: 'only-on-failure', trace: 'retain-on-failure' },
  webServer: [
    { command: '..\\browser-fixture.exe -data ..\\.test-data\\browser', url: 'http://127.0.0.1:3742', reuseExistingServer: false, timeout: 30_000 },
    { command: '..\\mock-model.exe', url: 'http://127.0.0.1:3743/v1/models', reuseExistingServer: false, timeout: 30_000 }
  ]
});
