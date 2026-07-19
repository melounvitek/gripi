#!/usr/bin/env node

import { mkdtemp, rm } from "node:fs/promises";
import { spawn } from "node:child_process";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

if (!process.env.GRIPI_E2E_BASE_URL) {
  console.error("GRIPI_E2E_BASE_URL is required for an external target.");
  process.exit(1);
}

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const runtimeRoot = await mkdtemp(path.join(os.tmpdir(), "gripi-e2e-external-"));
const playwright = path.join(repoRoot, "node_modules", "@playwright", "test", "cli.js");
const requestedArgs = process.argv.slice(2);
if (requestedArgs.includes("--no-deps") || requestedArgs.some((argument, index) => argument === "--project=real-pi" || (argument === "--project" && requestedArgs[index + 1] === "real-pi"))) {
  console.error("External contract runs cannot bypass the setup preflight or select the real-Pi project.");
  await rm(runtimeRoot, { recursive: true, force: true });
  process.exit(1);
}
const args = ["test", ...(requestedArgs.length ? requestedArgs : ["--project=desktop", "--project=mobile"])];
let exitCode = 1;
try {
  const tests = spawn(process.execPath, [playwright, ...args], {
    cwd: repoRoot,
    env: {
      ...process.env,
      GRIPI_E2E_AUTH_STATE: path.join(runtimeRoot, "browser-state.json"),
      GRIPI_E2E_REAL_PI: ""
    },
    stdio: "inherit"
  });
  exitCode = await new Promise((resolve, reject) => {
    tests.once("error", reject);
    tests.once("exit", (code) => resolve(code ?? 1));
  });
} catch (error) {
  console.error(error.stack || error.message);
} finally {
  await rm(runtimeRoot, { recursive: true, force: true });
}
process.exit(exitCode);
