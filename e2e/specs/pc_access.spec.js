import { expect, test } from "@playwright/test";
import { execFile, execFileSync, spawn } from "node:child_process";
import fs from "node:fs";
import net from "node:net";
import os from "node:os";
import path from "node:path";
import { createPcAccess, linkRequest } from "../../electron/pc_access.js";
import { ADMIN_PASSWORD, sessions } from "../support/contract.mjs";
import { selectSession } from "../support/ui.mjs";

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
let pcAccesses = [];
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
  await Promise.all(pcAccesses.map((pcAccess) => pcAccess.stopAll()));
  pcAccesses = [];
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
  return gripi(["pc", "ssh", "--", ...command]);
}

function gripi(args) {
  const env = {
    ...process.env,
    ...gitEnv,
    GRIPI_SOCKET_PATH: process.env.GRIPI_E2E_SOCKET_PATH,
    PI_SESSION_FILE: sessionFile()
  };
  delete env.HERMES_SESSION_ID;
  delete env.SSH_AUTH_SOCK;
  return new Promise((resolve) => {
    execFile(process.env.GRIPI_E2E_GRIPI, args, { cwd: file("work"), env, timeout: 15_000 }, (error, stdout, stderr) => {
      resolve({ code: error ? error.code : 0, stdout, stderr });
    });
  });
}

// Turns PC access on for the gateway in the desktop app of context, answering each approval dialog with answer, or what
// answer returns for the dialog's signal, and returns what the user saw there and the requests that reached it.
async function allowPcAccess(context, gateway, answer) {
  const cookie = (await context.cookies(gateway.url)).map(({ name, value }) => `${name}=${value}`).join("; ");
  const desktop = { seen: [], requests: [] };
  const pcAccess = createPcAccess({
    // Like main.js, with the approved browser's cookies.
    request: async (target, pathname, body, signal) => {
      const response = await linkRequest(new URL(pathname, target.url), body, { cookie, signal });
      if (pathname === "/pc/link") desktop.requests.push(...(response.body?.messages || []).filter((message) => message.type === "ssh"));
      return response;
    },
    confirm: async ({ signal, ...prompt }) => {
      desktop.seen.push(prompt);
      return typeof answer === "function" ? answer(signal) : answer;
    },
    authenticate: async () => {
      desktop.seen.push("password");
      return { ok: true };
    },
    readKnownHosts: async () => [knownHosts]
  });
  pcAccesses.push(pcAccess);
  pcAccess.start(gateway);
  return desktop;
}

// Clicks in the session in page, which sends the session's requests to the computer of its desktop app once that app's
// link is up. Listing the agent's keys, which needs no approval, then reaches that app.
async function useSession(page, desktop) {
  await page.goto(`/?session=${encodeURIComponent(sessionFile())}`);
  await expect(page.getByRole("heading", { level: 1, name: sessions.history })).toBeVisible();
  await expect.poll(async () => {
    await page.locator("#conversation-scroll").click();
    return reachedBy(desktop);
  }).toBe(true);
}

// Opens a page in a browser of its own, such as another computer's, which the user approves with the admin password.
async function approvedPage(browser) {
  const context = await browser.newContext({ storageState: { cookies: [], origins: [] } });
  const page = await context.newPage();
  await page.goto("/");
  await page.getByPlaceholder("Admin password").fill(process.env.GRIPI_E2E_ADMIN_PASSWORD || ADMIN_PASSWORD);
  await page.getByRole("button", { name: "Approve this browser" }).click();
  await expect(page.getByRole("heading", { name: "Browser access required" })).toBeHidden();
  return page;
}

// Lists the agent's keys with gripi pc ssh, and reports whether that reached desktop.
async function reachedBy(desktop) {
  const requests = desktop.requests.length;
  const listed = await gripiPc(["ssh-add", "-l"]);
  return listed.stdout.includes(fingerprintOf("client")) && desktop.requests.length > requests;
}

test("a login allowed for 5 minutes pushes, then fetches without asking again", async ({ context, page, baseURL, isMobile }) => {
  test.skip(isMobile, "Desktop app flow");
  const gateway = { id: "e2e", name: "E2E gateway", url: baseURL };
  const desktop = await allowPcAccess(context, gateway, "grant");
  await useSession(page, desktop);
  const { seen } = desktop;

  const push = await gripiPc(["git", "push", "origin", "main"]);
  expect(push.code, push.stderr).toBe(0);
  expect(git("--git-dir", file("repo.git"), "rev-parse", "main")).toBe(git("-C", file("work"), "rev-parse", "main"));
  const prompt = {
    kind: "ssh",
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

test("a denied login fails the push with the PC's refusal", async ({ context, page, baseURL, isMobile }) => {
  test.skip(isMobile, "Desktop app flow");
  const desktop = await allowPcAccess(context, { id: "e2e", name: "E2E gateway", url: baseURL }, "deny");
  await useSession(page, desktop);
  const { seen } = desktop;

  const push = await gripiPc(["git", "push", "origin", "main:refs/heads/denied"]);
  expect(push.code).not.toBe(0);
  expect(push.stderr).toContain("gripi pc: denied on the PC\n");
  expect(seen).toEqual([expect.objectContaining({ login: os.userInfo().username })]);
  expect(git("--git-dir", file("repo.git"), "for-each-ref", "refs/heads/denied")).toBe("");
});

test("a session's requests go to the desktop app where it was last clicked in", async ({ browser, context, page, baseURL, isMobile }) => {
  test.skip(isMobile, "Desktop app flow");
  const gateway = { id: "e2e", name: "E2E gateway", url: baseURL };
  // Another computer's desktop app, which has an approved browser of its own.
  const desk = await approvedPage(browser);
  try {
    const laptopApp = await allowPcAccess(context, gateway, "deny");
    const deskApp = await allowPcAccess(desk.context(), gateway, "deny");

    await useSession(page, laptopApp);
    // A switch to the session in the sidebar is a use of the session it opens, once the desk's link is up.
    await desk.goto("/");
    await expect.poll(async () => {
      await selectSession(desk, sessions.prompt);
      const used = desk.waitForRequest((request) => new URL(request.url()).pathname === "/web-push/presence" && request.postDataJSON().used);
      await selectSession(desk, sessions.history);
      await (await used).response();
      return reachedBy(deskApp);
    }).toBe(true);
    const presence = page.waitForRequest((request) => new URL(request.url()).pathname === "/web-push/presence");
    await page.reload();
    await presence;
    expect(await reachedBy(deskApp)).toBe(true);
  } finally {
    await desk.context().close();
  }
});

// What the line above the editor says while the computer asks to approve the push.
const waitingLine = () => `Waiting for you on ${os.hostname().split(".")[0]}: SSH login to ${knownHosts.split(" ")[0]} as ${os.userInfo().username}`;

async function expectJustAboveTheEditor(page, line) {
  const lineBox = await line.boundingBox();
  const editorBox = await page.locator(".composer-input-row").boundingBox();
  expect(editorBox.y - (lineBox.y + lineBox.height)).toBeGreaterThanOrEqual(0);
  expect(editorBox.y - (lineBox.y + lineBox.height)).toBeLessThan(16);
}

test("the session shows the login that the computer asks to approve above the editor, which stays usable", async ({ context, page, baseURL, isMobile }) => {
  test.skip(isMobile, "Desktop app flow");
  test.setTimeout(40_000);
  let approve;
  const desktop = await allowPcAccess(context, { id: "e2e", name: "E2E gateway", url: baseURL }, () => new Promise((resolve) => { approve = resolve; }));
  await useSession(page, desktop);

  const push = gripiPc(["git", "push", "origin", "main:refs/heads/waiting"]);
  const line = page.locator(".pc-approval");
  await expect(line).toBeVisible({ timeout: 10_000 });
  await expect(line.locator(".pc-approval-text")).toHaveText(waitingLine());
  await expect(line.locator(".pc-approval-time")).toHaveText(/^expires in 1:[45]\d$/);
  await expectJustAboveTheEditor(page, line);
  const editor = page.getByLabel("Message to Pi");
  await editor.fill("Typed while the computer asks");
  await expect(editor).toHaveValue("Typed while the computer asks");

  // Also after a reload, from the page alone.
  const events = (url) => url.pathname === "/events";
  await page.route(events, (route) => route.abort());
  await page.reload();
  await expect(line).toBeVisible();
  await page.unroute(events);

  approve("once");
  expect((await push).code).toBe(0);
  await expect(line).toBeHidden({ timeout: 10_000 });
});

test("a phone shows the login that the computer asks to approve, and cancels it with one tap", async ({ browser, page, baseURL, isMobile }) => {
  test.skip(!isMobile, "Touch flow");
  test.setTimeout(40_000);
  const laptop = await approvedPage(browser);
  try {
    let dismissed = false;
    // Like Electron's message box, the dialog closes with Deny when its signal aborts.
    const desktop = await allowPcAccess(laptop.context(), { id: "e2e", name: "E2E gateway", url: baseURL }, (signal) => new Promise((resolve) => {
      signal.addEventListener("abort", () => {
        dismissed = true;
        resolve("deny");
      });
    }));
    await useSession(laptop, desktop);
    await page.goto(`/?session=${encodeURIComponent(sessionFile())}`);

    const push = gripiPc(["git", "push", "origin", "main:refs/heads/cancelled"]);
    const line = page.locator(".pc-approval");
    await expect(line).toBeVisible({ timeout: 10_000 });
    await expect(line.locator(".pc-approval-text")).toHaveText(waitingLine());
    await expectJustAboveTheEditor(page, line);
    const cancel = line.getByRole("button", { name: /^cancel / });
    expect((await cancel.boundingBox()).height).toBeGreaterThanOrEqual(44);

    await cancel.tap();
    await expect(line).toBeHidden();
    await expect.poll(() => dismissed).toBe(true);
    const pushed = await push;
    expect(pushed.code).not.toBe(0);
    expect(pushed.stderr).toContain("gripi pc: cancelled in Gripi\n");
    expect(await page.evaluate(() => document.activeElement?.tagName)).not.toBe("TEXTAREA");
    expect(git("--git-dir", file("repo.git"), "for-each-ref", "refs/heads/cancelled")).toBe("");
  } finally {
    await laptop.context().close();
  }
});

// The file that the computer runs for a command, which its dialog and Gripi's line show.
const which = (command) => execFileSync("sh", ["-c", `command -v ${command}`], { encoding: "utf8" }).trim();
const script = "pwd; echo err >&2; exit 3";

test("a command allowed on the computer runs there, and gives back its output and exit code", async ({ context, page, baseURL, isMobile }) => {
  test.skip(isMobile, "Desktop app flow");
  test.setTimeout(40_000);
  let approve;
  const gateway = { id: "e2e", name: "E2E gateway", url: baseURL };
  const desktop = await allowPcAccess(context, gateway, () => new Promise((resolve) => { approve = resolve; }));
  await useSession(page, desktop);
  const folder = fs.realpathSync(dir);

  const ran = gripi(["pc", "run", "--cwd", folder, "--", "sh", "-c", script]);
  const line = page.locator(".pc-approval");
  await expect(line.locator(".pc-approval-text")).toHaveText(`Waiting for you on ${os.hostname().split(".")[0]}: ${which("sh")} -c "${script}"`, { timeout: 10_000 });
  approve("run");

  expect(await ran).toEqual({ code: 3, stdout: `${folder}\n`, stderr: "err\n" });
  // Without polkit's password dialog, which only logins ask for.
  expect(desktop.seen).toEqual([{ kind: "run", gateway, sessionName: sessions.history, command: `${which("sh")} -c "${script}"`, folder, root: false }]);
  await expect(line).toBeHidden();
});

test("a phone cancels a command that the computer asks to run, which then doesn't", async ({ browser, page, baseURL, isMobile }) => {
  test.skip(!isMobile, "Touch flow");
  test.setTimeout(40_000);
  const laptop = await approvedPage(browser);
  try {
    const desktop = await allowPcAccess(laptop.context(), { id: "e2e", name: "E2E gateway", url: baseURL }, (signal) => new Promise((resolve) => {
      signal.addEventListener("abort", () => resolve("deny"));
    }));
    await useSession(laptop, desktop);
    await page.goto(`/?session=${encodeURIComponent(sessionFile())}`);

    const touched = file("cancelled");
    const ran = gripi(["pc", "run", "--", "touch", touched]);
    const line = page.locator(".pc-approval");
    await expect(line.locator(".pc-approval-text")).toHaveText(`Waiting for you on ${os.hostname().split(".")[0]}: ${which("touch")} ${touched}`, { timeout: 10_000 });
    await line.getByRole("button", { name: /^cancel / }).tap();

    const cancelled = await ran;
    expect(cancelled.code).toBe(1);
    expect(cancelled.stderr).toBe("gripi pc: the gateway answered 502: cancelled in Gripi\n");
    await expect(line).toBeHidden();
    expect(fs.existsSync(touched)).toBe(false);
  } finally {
    await laptop.context().close();
  }
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
