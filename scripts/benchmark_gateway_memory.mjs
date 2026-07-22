#!/usr/bin/env node

import { mkdir, mkdtemp, readFile, rm } from "node:fs/promises";
import { createServer } from "node:net";
import { spawn, spawnSync } from "node:child_process";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { performance } from "node:perf_hooks";
import { seedFixtures } from "../e2e/fixtures/seed.mjs";

if (process.argv.length > 2) {
  console.error("Usage: scripts/benchmark_gateway_memory.mjs");
  process.exit(1);
}
if (process.platform !== "linux") {
  console.error("The gateway RSS benchmark currently requires Linux /proc.");
  process.exit(1);
}

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const runtimeRoot = await mkdtemp(path.join(os.tmpdir(), "gripi-memory-"));
const fixture = await seedFixtures(runtimeRoot);
const port = await availablePort();
const baseURL = `http://127.0.0.1:${port}`;
const fakePiPath = path.join(repoRoot, "e2e", "support", "fake_pi.mjs");
const env = {
  ...process.env,
  HOME: fixture.home,
  APP_ENV: "production",
  GRIPI_ADMIN_PASSWORD: "memory-benchmark",
  GRIPI_BROWSER_AUTH_DISABLED: "1",
  GRIPI_MULTI_USER_MODE: "",
  GRIPI_AUTO_APPROVE_PROJECTS: "0",
  GRIPI_ALLOW_INSECURE_REMOTE_HTTP: "",
  GRIPI_PERMITTED_HOSTS: "",
  GRIPI_TRUST_PROXY_HEADERS: "",
  GRIPI_RESOURCE_MONITORING: "",
  GRIPI_RPC_DIAGNOSTICS: "",
  GRIPI_RPC_IDLE_TIMEOUT_SECONDS: "300",
  GRIPI_RPC_IDLE_SWEEP_SECONDS: "30",
  GRIPI_ENV_PATH: path.join(runtimeRoot, "missing-env"),
  GRIPI_BIND_HOST: "127.0.0.1",
  GRIPI_PORT: String(port),
  GRIPI_SESSIONS_ROOT: fixture.sessionsRoot,
  GRIPI_ATTACHMENTS_ROOT: fixture.attachmentsRoot,
  GRIPI_SESSION_CWDS_PATH: fixture.configuredCwdsPath,
  GRIPI_READ_STATE_PATH: path.join(fixture.state, "read-state.json"),
  GRIPI_BROWSER_ACCESS_PATH: path.join(fixture.state, "browser-access.json"),
  GRIPI_WORKSPACE_SECRET_PATH: path.join(fixture.state, "workspace-secret"),
  GRIPI_WORKSPACE_ACCESS_PATH: path.join(fixture.state, "workspace-access.json"),
  GRIPI_WORKSPACE_OWNERSHIP_PATH: path.join(fixture.state, "session-owners.json"),
  GRIPI_NODE: process.execPath,
  GRIPI_PI: fakePiPath,
  GRIPI_E2E_SESSIONS_ROOT: fixture.sessionsRoot
};

const executableRoot = path.join(runtimeRoot, "checkout");
const command = path.join(executableRoot, "tmp", "gripi");
await mkdir(path.dirname(command), { recursive: true });
const initializeCheckout = spawnSync("git", ["init", "--quiet", executableRoot], { encoding: "utf8" });
if (initializeCheckout.status !== 0) {
  console.error(initializeCheckout.stderr || initializeCheckout.stdout);
  await rm(runtimeRoot, { recursive: true, force: true });
  process.exit(initializeCheckout.status ?? 1);
}
const build = spawnSync("go", ["build", "-o", command, "./cmd/gripi"], {
  cwd: repoRoot,
  env: process.env,
  encoding: "utf8"
});
if (build.status !== 0) {
  console.error(build.stderr || build.stdout);
  await rm(runtimeRoot, { recursive: true, force: true });
  process.exit(build.status ?? 1);
}

let logs = "";
const child = spawn(command, [], {
  cwd: repoRoot,
  env,
  detached: true,
  stdio: ["ignore", "pipe", "pipe"]
});
child.stdout.on("data", (chunk) => { logs += chunk; });
child.stderr.on("data", (chunk) => { logs += chunk; });

try {
  await waitForServer(`${baseURL}/apple-touch-icon.png`, child);
  const timings = [];
  for (let iteration = 0; iteration < 52; iteration += 1) {
    for (const endpoint of ["/", "/sidebar"]) {
      const startedAt = performance.now();
      const response = await fetch(`${baseURL}${endpoint}`);
      await response.arrayBuffer();
      if (response.status !== 200) throw new Error(`${endpoint} returned ${response.status}`);
      if (iteration >= 2) timings.push(performance.now() - startedAt);
    }
  }

  const rssSamples = [];
  for (let sample = 0; sample < 20; sample += 1) {
    rssSamples.push(await processTreeRSS(child.pid));
    await sleep(50);
  }
  rssSamples.sort((left, right) => left - right);
  timings.sort((left, right) => left - right);
  const result = {
    implementation: "go",
    workload: "4 warmup and 100 measured GET requests across / and /sidebar with seeded E2E sessions",
    pid: child.pid,
    rssMiB: {
      median: mib(percentile(rssSamples, 0.5)),
      maximum: mib(rssSamples.at(-1))
    },
    requestMilliseconds: {
      median: rounded(percentile(timings, 0.5)),
      p95: rounded(percentile(timings, 0.95))
    }
  };
  console.log(JSON.stringify(result, null, 2));
} catch (error) {
  console.error(error.stack || error.message);
  console.error(`\nGateway output:\n${logs}`);
  process.exitCode = 1;
} finally {
  try { process.kill(-child.pid, "SIGTERM"); } catch (_error) {}
  await Promise.race([
    new Promise((resolve) => child.once("exit", resolve)),
    sleep(3000)
  ]);
  if (child.exitCode === null) {
    try { process.kill(-child.pid, "SIGKILL"); } catch (_error) {}
  }
  if (process.env.GRIPI_BENCHMARK_KEEP_RUNTIME === "1") console.error(`Kept benchmark runtime at ${runtimeRoot}`);
  else await rm(runtimeRoot, { recursive: true, force: true });
}

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
    if (child.exitCode !== null) throw new Error(`Gateway exited before becoming ready (${child.exitCode})`);
    try {
      const response = await fetch(url);
      if (response.status === 200) return;
    } catch (_error) {}
    await sleep(100);
  }
  throw new Error(`Gateway did not become ready at ${url}`);
}

async function processTreeRSS(rootPID) {
  const pids = [rootPID];
  for (let index = 0; index < pids.length; index += 1) {
    const childrenPath = `/proc/${pids[index]}/task/${pids[index]}/children`;
    try {
      const children = (await readFile(childrenPath, "utf8")).trim();
      if (children) pids.push(...children.split(/\s+/).map(Number));
    } catch (_error) {}
  }

  let totalKiB = 0;
  for (const pid of new Set(pids)) {
    try {
      const status = await readFile(`/proc/${pid}/status`, "utf8");
      totalKiB += Number(status.match(/^VmRSS:\s+(\d+)\s+kB$/m)?.[1] || 0);
    } catch (_error) {}
  }
  return totalKiB;
}

function percentile(sorted, ratio) {
  return sorted[Math.min(sorted.length - 1, Math.ceil(sorted.length * ratio) - 1)];
}

function mib(kibibytes) {
  return rounded(kibibytes / 1024);
}

function rounded(value) {
  return Math.round(value * 100) / 100;
}

function sleep(milliseconds) {
  return new Promise((resolve) => setTimeout(resolve, milliseconds));
}
