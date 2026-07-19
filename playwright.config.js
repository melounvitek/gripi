import { defineConfig, devices } from "@playwright/test";
import os from "node:os";
import path from "node:path";

const baseURL = process.env.GRIPI_E2E_BASE_URL;
if (!baseURL) throw new Error("GRIPI_E2E_BASE_URL is required. Use `npm run test:e2e` for a managed target.");

const storageState = process.env.GRIPI_E2E_AUTH_STATE || path.join(os.tmpdir(), `gripi-e2e-auth-${process.pid}.json`);

export default defineConfig({
  testDir: "./e2e/specs",
  fullyParallel: false,
  workers: 1,
  retries: 0,
  timeout: 20_000,
  expect: { timeout: 7_000 },
  reporter: [["list"]],
  use: {
    baseURL,
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
    video: "off"
  },
  projects: [
    {
      name: "setup",
      testMatch: /access\.setup\.js/
    },
    {
      name: "desktop",
      dependencies: ["setup"],
      testIgnore: [/access\.setup\.js/, /mobile\.spec\.js/, /real_pi\.spec\.js/],
      use: {
        storageState,
        viewport: { width: 1440, height: 900 }
      }
    },
    {
      name: "mobile",
      dependencies: ["setup"],
      testMatch: /mobile\.spec\.js/,
      use: {
        ...devices["Pixel 7"],
        storageState
      }
    },
    {
      name: "real-pi",
      testMatch: /real_pi\.spec\.js/,
      use: {
        viewport: { width: 1440, height: 900 }
      }
    }
  ]
});
