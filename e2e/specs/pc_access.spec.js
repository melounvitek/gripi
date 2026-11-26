import { expect, test } from "@playwright/test";
import { execFile, execFileSync, spawn } from "node:child_process";
import fs from "node:fs";
import net from "node:net";
import os from "node:os";
import path from "node:path";
import { createPcAccess, linkRequest } from "../../electron/pc_access.js";
import { sessions } from "../support/contract.mjs";

// The test plays the desktop app with its real link core, and an SSH server and agent of its own on this machine,
// which is the PC and the gateway at once.
const root = process.env.GRIPI_E2E_RUNTIME_ROOT;
const missingTool = ["/usr/sbin/sshd", "ssh", "ssh-agent", "ssh-add", "ssh-keygen", "git"].find((tool) => !onPath(tool));
test.skip(!root, "Needs the managed gateway, whose socket gripi uses");
test.skip(Boolean(missingTool), `${missingTool} is missing`);

const userAgentSocket = process.env.SSH_AUTH_SOCK;
let dir;
let sshd;
let agent;
let knownHosts;
let gitEnv;
let pcAccess;
const file = (name) => path.join(dir, name);
const fingerprintOf = (name) => execFileSync("ssh-keygen", ["-lf", file(`${name}.pub`)], { encoding: "utf8" }).split(" ")[1];

test.beforeAll(async () => {
  dir = fs.mkdtempSync(path.join(os.tmpdir(), "gripi-e2e-pc-"));
  const port = await freePort();
  for (const name of ["host", "client"]) {
    execFileSync("ssh-keygen", ["-q", "-t", "ed25519", "-N", "", "-C", `pc-e2e-${name}`, "-f", file(name)]);
  }
  const publicKey = (name) => fs.readFileSync(file(`${name}.pub`), "utf8").split(" ").slice(0, 2).join(" ");
  knownHosts = `[127.0.0.1]:${port} ${publicKey("host")}\n`;
  fs.writeFileSync(file("known_hosts"), knownHosts);
  fs.writeFileSync(file("authorized_keys"), `${publicKey("client")}\n`);

  const sshdConfig = [
    "ListenAddress 127.0.0.1",
    `Port ${port}`,
    `HostKey ${file("host")}`,
    `AuthorizedKeysFile ${file("authorized_keys")}`,
    `PidFile ${file("sshd.pid")}`,
    "UsePAM no",
    "StrictModes no",
    "PasswordAuthentication no",
    "KbdInteractiveAuthentication no"
  ];
  // The denied login must not lock out the next one. sshd before 9.8 has no penalties, nor the option.
  fs.writeFileSync(file("sshd_config"), [...sshdConfig, "PerSourcePenalties no"].join("\n") + "\n");
  try {
    execFileSync("/usr/sbin/sshd", ["-t", "-f", file("sshd_config")], { stdio: "ignore" });
  } catch {
    fs.writeFileSync(file("sshd_config"), sshdConfig.join("\n") + "\n");
  }
  sshd = await startDaemon("/usr/sbin/sshd", ["-D", "-e", "-f", file("sshd_config")], /Server listening/);

  // The PC's agent, which the link core's default connectAgent finds by SSH_AUTH_SOCK.
  agent = await startDaemon("ssh-agent", ["-D", "-a", file("agent.sock")], /Agent pid/);
  process.env.SSH_AUTH_SOCK = file("agent.sock");
  execFileSync("ssh-add", [file("client")], { stdio: "ignore" });
  fs.rmSync(file("client"));

  // Git and ssh on the gateway, with no configuration, keys or agent but what gripi pc ssh gives them.
  const ssh = [
    "ssh", "-F", "/dev/null", "-p", port,
    "-o", `UserKnownHostsFile=${file("known_hosts")}`, "-o", "GlobalKnownHostsFile=/dev/null", "-o", "StrictHostKeyChecking=yes",
    "-o", "BatchMode=yes", "-o", "IdentityFile=none", "-o", "IdentitiesOnly=no"
  ];
  gitEnv = {
    GIT_CONFIG_GLOBAL: "/dev/null",
    GIT_CONFIG_NOSYSTEM: "1",
    GIT_AUTHOR_NAME: "E2E",
    GIT_AUTHOR_EMAIL: "e2e@example.com",
    GIT_COMMITTER_NAME: "E2E",
    GIT_COMMITTER_EMAIL: "e2e@example.com",
    GIT_SSH_COMMAND: ssh.join(" ")
  };
  git("init", "-q", "--bare", file("repo.git"));
  git("init", "-q", "-b", "main", file("work"));
  git("-C", file("work"), "commit", "-q", "--allow-empty", "-m", "Pushed with the PC's key");
  git("-C", file("work"), "remote", "add", "origin", `127.0.0.1:${file("repo.git")}`);
});

test.afterEach(async () => {
  await pcAccess?.stopAll();
  pcAccess = undefined;
});

test.afterAll(() => {
  if (userAgentSocket === undefined) delete process.env.SSH_AUTH_SOCK;
  else process.env.SSH_AUTH_SOCK = userAgentSocket;
  sshd?.kill();
  agent?.kill();
  if (dir) fs.rmSync(dir, { recursive: true, force: true });
});

function git(...args) {
  return execFileSync("git", args, { env: { ...process.env, ...gitEnv }, encoding: "utf8" }).trim();
}

const sessionFile = () => path.join(root, "sessions", "e2e", "history.jsonl");

// Runs gripi pc ssh as the bash tool of the session's Pi would. Only gripi's relay leads to an agent.
function gripiPc(command) {
  const env = {
    ...process.env,
    ...gitEnv,
    GRIPI_SOCKET_PATH: process.env.GRIPI_E2E_SOCKET_PATH,
    PI_SESSION_FILE: sessionFile()
  };
  delete env.HERMES_SESSION_ID;
  delete env.SSH_AUTH_SOCK;
  return new Promise((resolve) => {
    execFile(process.env.GRIPI_E2E_GRIPI, ["pc", "ssh", "--", ...command], { cwd: file("work"), env, timeout: 15_000 }, (error, stdout, stderr) => {
      resolve({ code: error ? error.code : 0, stdout, stderr });
    });
  });
}

// Turns PC access on for the gateway, answering each approval dialog with answer, and returns what the user saw.
async function allowPcAccess(context, gateway, answer) {
  const cookie = (await context.cookies(gateway.url)).map(({ name, value }) => `${name}=${value}`).join("; ");
  const seen = [];
  pcAccess = createPcAccess({
    // Like main.js, with the approved browser's cookies.
    request: (target, pathname, body, signal) => linkRequest(new URL(pathname, target.url), body, { cookie, signal }),
    confirm: async ({ signal, ...prompt }) => {
      seen.push(prompt);
      return answer;
    },
    authenticate: async () => {
      seen.push("password");
      return { ok: true };
    },
    readKnownHosts: async () => [knownHosts]
  });
  pcAccess.start(gateway);
  // The link is up, and the session's requests go to it once it was used there, when the agent's keys can be listed,
  // which needs no approval.
  const key = fingerprintOf("client");
  await expect.poll(async () => {
    await context.request.put("/web-push/presence", {
      data: { client_id: "pc-e2e", session: sessionFile(), focused: true, used: true, sequence: Date.now() }
    });
    return (await gripiPc(["ssh-add", "-l"])).stdout;
  }).toContain(key);
  return seen;
}

test("a login allowed for 5 minutes pushes, then fetches without asking again", async ({ context, baseURL }) => {
  const gateway = { id: "e2e", name: "E2E gateway", url: baseURL };
  const seen = await allowPcAccess(context, gateway, "grant");

  const push = await gripiPc(["git", "push", "origin", "main"]);
  expect(push.code, push.stderr).toBe(0);
  expect(git("--git-dir", file("repo.git"), "rev-parse", "main")).toBe(git("-C", file("work"), "rev-parse", "main"));
  const prompt = {
    gateway,
    sessionName: sessions.history,
    key: { type: "ssh-ed25519", fingerprint: fingerprintOf("client"), comment: "pc-e2e-client" },
    server: { names: [knownHosts.split(" ")[0]], hostKeyFingerprint: fingerprintOf("host") },
    login: os.userInfo().username
  };
  expect(seen).toEqual([prompt, "password"]);

  const fetch = await gripiPc(["git", "fetch", "origin"]);
  expect(fetch.code, fetch.stderr).toBe(0);
  expect(seen).toEqual([prompt, "password"]);
});

test("a denied login fails the push with the PC's refusal", async ({ context, baseURL }) => {
  const seen = await allowPcAccess(context, { id: "e2e", name: "E2E gateway", url: baseURL }, "deny");

  const push = await gripiPc(["git", "push", "origin", "main:refs/heads/denied"]);
  expect(push.code).not.toBe(0);
  expect(push.stderr).toContain("gripi pc: denied on the PC\n");
  expect(seen).toEqual([expect.objectContaining({ login: os.userInfo().username })]);
  expect(git("--git-dir", file("repo.git"), "for-each-ref", "refs/heads/denied")).toBe("");
});

function onPath(command) {
  if (path.isAbsolute(command)) return fs.existsSync(command);
  return process.env.PATH.split(path.delimiter).some((directory) => fs.existsSync(path.join(directory, command)));
}

async function freePort() {
  const server = net.createServer();
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  const { port } = server.address();
  await new Promise((resolve) => server.close(resolve));
  return port;
}

async function startDaemon(command, args, ready) {
  const child = spawn(command, args, { stdio: ["ignore", "pipe", "pipe"] });
  let output = "";
  await new Promise((resolve, reject) => {
    const read = (chunk) => {
      output += chunk;
      if (ready.test(output)) resolve();
    };
    child.stdout.on("data", read);
    child.stderr.on("data", read);
    child.on("exit", (code) => reject(new Error(`${command} exited with ${code}: ${output}`)));
  });
  return child;
}
