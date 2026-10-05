import { defineConfig } from '@playwright/test';
import { existsSync, readdirSync } from 'node:fs';
import { homedir } from 'node:os';
import { join } from 'node:path';

const cache = join(homedir(), '.cache/ms-playwright');
const cached = existsSync(cache)
  ? readdirSync(cache)
      .filter((p) => /^chromium-\d+$/.test(p))
      .sort((a, b) => Number(b.split('-')[1]) - Number(a.split('-')[1]))
      .map((p) => join(cache, p, 'chrome-linux64/chrome'))
      .find(existsSync)
  : undefined;
export default defineConfig({
  testDir: './tests',
  fullyParallel: false,
  workers: 1,
  timeout: 30_000,
  expect: { timeout: 8_000 },
  reporter: 'list',
  outputDir: '../artifacts/browser-results',
  use: {
    baseURL: 'http://127.0.0.1:8791',
    viewport: { width: 1536, height: 980 },
    launchOptions: { executablePath: process.env.FIBERLAB_BROWSER || cached },
    screenshot: 'only-on-failure',
    trace: 'retain-on-failure',
  },
  webServer: {
    command:
      '../bin/ftthlab serve --listen 127.0.0.1:8791 --data-dir ../artifacts/ui-workspace',
    url: 'http://127.0.0.1:8791/api/v1/health',
    reuseExistingServer: false,
    timeout: 20_000,
  },
});
