#!/usr/bin/env node

import { closeSync, openSync, rmSync } from "node:fs";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import { createServer } from "node:net";
import { spawn } from "node:child_process";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { ADMIN_PASSWORD } from "./contract.mjs";
import { seedFixtures } from "../fixtures/seed.mjs";

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const runnerArgs = process.argv.slice(2);
const realPi = runnerArgs.includes("--real-pi");
const playwrightArgs = runnerArgs.filter((argument) => argument !== "--real-pi");
const realAgentDir = process.env.PI_CODING_AGENT_DIR || path.join(os.homedir(), ".pi", "agent");
const runtimeRoot = await mkdtemp(path.join(os.tmpdir(), "gripi-e2e-"));
process.once("exit", () => {
  if (process.env.GRIPI_E2E_KEEP_RUNTIME !== "1") rmSync(runtimeRoot, { recursive: true, force: true });
});
const fixture = await seedFixtures(runtimeRoot);
const port = await availablePort();
const baseURL = `http://127.0.0.1:${port}`;
const serverLogPath = path.join(runtimeRoot, "server.log");
const fakeLogPath = path.join(runtimeRoot, "fake-pi.log");
const authStatePath = path.join(runtimeRoot, "browser-state.json");
const serverLog = openSync(serverLogPath, "a");
const fakePiPath = path.join(repoRoot, "e2e", "support", "fake_pi.mjs");
let serverSpawnError = null;
let stopping = false;
let serverLogClosed = false;

const serverEnv = {
  ...process.env,
  HOME: fixture.home,
  RACK_ENV: "development",
  GRIPI_E2E_REAL_PI: realPi ? "1" : "",
  GRIPI_ADMIN_PASSWORD: ADMIN_PASSWORD,
  GRIPI_BROWSER_AUTH_DISABLED: realPi ? "1" : "",
  GRIPI_MULTI_USER_MODE: "",
  GRIPI_AUTO_APPROVE_PROJECTS: "0",
  GRIPI_RESOURCE_MONITORING: "1",
  GRIPI_ENV_PATH: path.join(runtimeRoot, "missing-env"),
  GRIPI_BIND_HOST: "127.0.0.1",
  GRIPI_SESSIONS_ROOT: fixture.sessionsRoot,
  GRIPI_ATTACHMENTS_ROOT: fixture.attachmentsRoot,
  GRIPI_SESSION_CWDS_PATH: fixture.configuredCwdsPath,
  GRIPI_READ_STATE_PATH: path.join(fixture.state, "read-state.json"),
  GRIPI_BROWSER_ACCESS_PATH: path.join(fixture.state, "browser-access.json"),
  GRIPI_WORKSPACE_SECRET_PATH: path.join(fixture.state, "workspace-secret"),
  GRIPI_WORKSPACE_ACCESS_PATH: path.join(fixture.state, "workspace-access.json"),
  GRIPI_WORKSPACE_OWNERSHIP_PATH: path.join(fixture.state, "session-owners.json"),
  GRIPI_RPC_IDLE_TIMEOUT_SECONDS: process.env.GRIPI_E2E_RPC_IDLE_TIMEOUT_SECONDS || (realPi ? "0" : "2"),
  GRIPI_RPC_IDLE_SWEEP_SECONDS: process.env.GRIPI_E2E_RPC_IDLE_SWEEP_SECONDS || (realPi ? "30" : "0.1"),
  GRIPI_NODE: realPi ? "" : process.execPath,
  GRIPI_PI: realPi ? "" : fakePiPath,
  GRIPI_E2E_SESSIONS_ROOT: fixture.sessionsRoot,
  GRIPI_E2E_FAKE_PI_LOG: fakeLogPath,
  ...(realPi ? {
    PI_CODING_AGENT_DIR: realAgentDir,
    PI_CODING_AGENT_SESSION_DIR: fixture.sessionsRoot
  } : {})
};

const server = spawn("bundle", ["exec", "rackup", "-o", "127.0.0.1", "-p", String(port)], {
  cwd: repoRoot,
  env: serverEnv,
  detached: process.platform !== "win32",
  stdio: ["ignore", serverLog, serverLog]
});
server.once("error", (error) => { serverSpawnError = error; });

for (const signal of ["SIGINT", "SIGTERM"]) {
  process.on(signal, async () => {
    await stop();
    process.exit(130);
  });
}

let exitCode = 1;
try {
  await waitForServer(baseURL, server);
  const playwright = path.join(repoRoot, "node_modules", "@playwright", "test", "cli.js");
  const args = ["test", ...(playwrightArgs.length ? playwrightArgs : ["--project=desktop", "--project=mobile"])];
  const tests = spawn(process.execPath, [playwright, ...args], {
    cwd: repoRoot,
    env: {
      ...process.env,
      GRIPI_E2E_BASE_URL: baseURL,
      GRIPI_E2E_ADMIN_PASSWORD: ADMIN_PASSWORD,
      GRIPI_E2E_AUTH_STATE: authStatePath,
      GRIPI_E2E_EXPECT_ACCESS: realPi ? "" : "1",
      GRIPI_E2E_REAL_PI: realPi ? "1" : "",
      GRIPI_E2E_FAKE_PI_LOG: fakeLogPath
    },
    stdio: "inherit"
  });
  exitCode = await childExitCode(tests);
} catch (error) {
  console.error(error.stack || error.message);
} finally {
  if (exitCode !== 0) {
    console.error(`\nManaged Gripi log (${serverLogPath}):\n${await readableLog(serverLogPath)}`);
    console.error(`\nFake Pi log (${fakeLogPath}):\n${await readableLog(fakeLogPath)}`);
  }
  await stop();
}
process.exit(exitCode);

async function availablePort() {
  const listener = createServer();
  await new Promise((resolve, reject) => listener.once("error", reject).listen(0, "127.0.0.1", resolve));
  const { port } = listener.address();
  await new Promise((resolve) => listener.close(resolve));
  return port;
}

async function waitForServer(url, child) {
  const deadline = Date.now() + 20_000;
  while (Date.now() < deadline) {
    if (serverSpawnError) throw serverSpawnError;
    if (child.exitCode !== null) throw new Error(`Managed Gripi exited before becoming ready (${child.exitCode})`);
    try {
      const response = await fetch(url, { redirect: "manual" });
      if ([200, 302, 403].includes(response.status)) return;
    } catch (_error) {
    }
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  throw new Error(`Managed Gripi did not become ready at ${url}`);
}

function childExitCode(child) {
  return new Promise((resolve, reject) => {
    child.once("error", reject);
    child.once("exit", (code) => resolve(code ?? 1));
  });
}

async function readableLog(filePath) {
  try {
    return await readFile(filePath, "utf8");
  } catch (_error) {
    return "(no log)";
  }
}

async function stop() {
  if (stopping) return;
  stopping = true;
  if (server.exitCode === null && !serverSpawnError) {
    if (process.platform === "win32") server.kill("SIGTERM");
    else {
      try {
        process.kill(-server.pid, "SIGTERM");
      } catch (_error) {
      }
    }
    await Promise.race([
      new Promise((resolve) => server.once("exit", resolve)),
      new Promise((resolve) => setTimeout(resolve, 3000))
    ]);
    if (server.exitCode === null && process.platform !== "win32") {
      try {
        process.kill(-server.pid, "SIGKILL");
      } catch (_error) {
      }
    }
  }
  if (!serverLogClosed) {
    closeSync(serverLog);
    serverLogClosed = true;
  }
  if (process.env.GRIPI_E2E_KEEP_RUNTIME === "1") console.error(`Kept E2E runtime at ${runtimeRoot}`);
  else await rm(runtimeRoot, { recursive: true, force: true });
}
