import { expect, test } from "@playwright/test";
import { spawn } from "node:child_process";
import { createHash, randomUUID } from "node:crypto";
import { mkdir, readdir, readFile } from "node:fs/promises";
import path from "node:path";
import { prompts, sessions } from "../support/contract.mjs";
import { sendPrompt } from "../support/ui.mjs";

const root = process.env.GRIPI_E2E_RUNTIME_ROOT;
test.skip(!root, "Needs the managed gateway, which runs a fake sudo");

const commands = new Set();
test.afterEach(() => {
  commands.forEach((command) => command.kill());
  commands.clear();
});

async function openSession(page, slug, title) {
  const file = path.join(root, "sessions", "e2e", `${slug}.jsonl`);
  await page.goto(`/?session=${encodeURIComponent(file)}`);
  await expect(page.getByRole("heading", { level: 1, name: title })).toBeVisible();
  return file;
}

// Runs gripi sudo as the bash tool of the session's Pi would.
function gripiSudo(session, argv, cwd = root) {
  const env = { ...process.env, GRIPI_SOCKET_PATH: process.env.GRIPI_E2E_SOCKET_PATH, PI_SESSION_FILE: session };
  delete env.HERMES_SESSION_ID;
  const command = spawn(process.env.GRIPI_E2E_GRIPI, ["sudo", "--", ...argv], { cwd, env });
  commands.add(command);
  let stdout = "";
  let stderr = "";
  command.stdout.on("data", (chunk) => { stdout += chunk; });
  command.stderr.on("data", (chunk) => { stderr += chunk; });
  const ended = new Promise((resolve) => command.once("close", (code) => resolve({ code, stdout, stderr })));
  return { command, ended };
}

function sudoPrompt(page) {
  return page.getByRole("region", { name: "Gripi sudo" });
}

// Also once the prompt is hidden.
function passwordField(page) {
  return page.locator(".sudo-prompt input[type=password]");
}

// The secret as text, and as base64 at each alignment, such as in a JSON byte field, without the ends that it shares
// with its neighbours.
function leakedForms(secret) {
  return [secret, ...[0, 1, 2].map((offset) => Buffer.concat([Buffer.alloc(offset), Buffer.from(secret)]).toString("base64").slice(offset ? 4 : 0, -4))];
}

async function runtimeFiles() {
  const entries = await readdir(root, { recursive: true, withFileTypes: true });
  const files = await Promise.all(entries.filter((entry) => entry.isFile()).map((entry) => readFile(path.join(entry.parentPath, entry.name)).catch(() => null)));
  return files.filter(Boolean);
}

test("the prompt takes the editor's place, runs the approved command, and keeps the password to sudo", async ({ page, isMobile }) => {
  test.skip(isMobile, "Keyboard flow");
  const responses = [];
  page.on("requestfinished", (request) => responses.push(request.response().then((response) => response.body()).catch(() => "")));
  const session = await openSession(page, "sudo", sessions.sudo);
  const composer = page.getByLabel("Message to Pi");
  await expect(composer).toBeVisible();
  const folder = path.join(root, "projects", "sudo project");
  await mkdir(folder, { recursive: true });

  const { command, ended } = gripiSudo(session, ["sh", "-c", "echo sudo-ran; exit 7", "<b>bold</b>"], folder);
  const prompt = sudoPrompt(page);
  await expect(prompt.getByRole("definition")).toHaveText(['sh -c "echo sudo-ran; exit 7" "<b>bold</b>"', `"${folder}"`]);
  await expect(prompt.locator("b")).toHaveCount(0);
  await expect(composer).toBeHidden();
  const password = prompt.getByLabel("password");
  await expect(password).toBeFocused();

  await password.press("Enter");
  await expect(prompt.getByRole("alert")).toHaveText("Enter the password on one line.");
  await expect(prompt).toBeVisible();
  expect(command.exitCode).toBeNull();

  const canary = `gripi-canary-${randomUUID()}`;
  await password.fill(canary);
  await password.press("Enter");
  await expect(prompt).toBeHidden();
  await expect(passwordField(page)).toHaveValue("");
  await expect(composer).toBeVisible();
  await expect(composer).not.toBeFocused();
  const { code, stdout, stderr } = await ended;
  expect({ code, stdout }).toEqual({ code: 7, stdout: "sudo-ran\n" });
  expect(await readFile(process.env.GRIPI_E2E_SUDO_HASHES, "utf8")).toContain(createHash("sha256").update(canary).digest("hex"));

  const storage = await page.evaluate(() => JSON.stringify([{ ...localStorage }, { ...sessionStorage }]));
  const seen = [stdout, stderr, storage, await page.content(), ...await Promise.all(responses), ...await runtimeFiles()];
  for (const form of leakedForms(canary)) {
    expect(seen.filter((text) => text.includes(form))).toHaveLength(0);
  }
});

test("a reload brings the oldest pending request back from the page, and the next one shows after it", async ({ page, isMobile }) => {
  test.skip(isMobile, "Keyboard flow");
  const session = await openSession(page, "sudo", sessions.sudo);
  const first = gripiSudo(session, ["echo", "first"]);
  const command = sudoPrompt(page).getByRole("definition").first();
  await expect(command).toHaveText("echo first");
  const second = gripiSudo(session, ["echo", "second"]);

  // Without live events, only the page can show them.
  await page.route((url) => url.pathname === "/events", (route) => route.abort());
  await page.reload();
  await expect(command).toHaveText("echo first");
  await sudoPrompt(page).getByRole("button", { name: "cancel" }).click();
  await expect(command).toHaveText("echo second");
  expect(await first.ended).toMatchObject({ code: 1, stderr: expect.stringContaining("cancelled") });

  await sudoPrompt(page).getByLabel("password").press("Escape");
  await expect(sudoPrompt(page)).toBeHidden();
  expect((await second.ended).code).toBe(1);
});

test("an answer in another tab takes the prompt away without moving the focus to the editor", async ({ page, isMobile }) => {
  test.skip(isMobile, "Keyboard flow");
  const session = await openSession(page, "sudo", sessions.sudo);
  const { ended } = gripiSudo(session, ["echo", "answered elsewhere"]);
  const password = sudoPrompt(page).getByLabel("password");
  await expect(password).toBeFocused();
  await password.pressSequentially("half typed");

  const other = await page.context().newPage();
  await openSession(other, "sudo", sessions.sudo);
  await sudoPrompt(other).getByLabel("password").fill("the other tab's password");
  await sudoPrompt(other).getByLabel("password").press("Enter");
  expect(await ended).toMatchObject({ code: 0, stdout: "answered elsewhere\n" });
  await other.close();
  await page.bringToFront();

  await expect(sudoPrompt(page)).toBeHidden();
  await expect(passwordField(page)).toHaveValue("");
  await expect(page.getByLabel("Message to Pi")).toBeVisible();
  await expect(page.getByLabel("Message to Pi")).not.toBeFocused();
});

test("a prompt that goes away as the run ends keeps the rest of the password out of the editor", async ({ page, isMobile }) => {
  test.skip(isMobile, "Keyboard flow");
  const session = await openSession(page, "sudo", sessions.sudo);
  await sendPrompt(page, prompts.abortStart);
  await expect(page.locator(".composer-state")).toHaveAttribute("data-state", "running");
  const { command, ended } = gripiSudo(session, ["echo", "never"]);
  const password = sudoPrompt(page).getByLabel("password");
  await expect(password).toBeFocused();
  await password.pressSequentially("pass");

  // The command gives up, as on its bash tool's timeout, and the run ends, which the page learns from one poll.
  const events = async (after) => (await page.request.get(`/events?session=${encodeURIComponent(session)}&after=${after}`)).json();
  const { last_seq: before } = await events(0);
  let polled;
  const polling = new Promise((resolve) => { polled = resolve; });
  let release;
  const released = new Promise((resolve) => { release = resolve; });
  await page.route((url) => url.pathname === "/events", async (route) => {
    polled();
    await released;
    await route.continue();
  });
  await polling;
  command.kill();
  await ended;
  expect((await page.request.post("/abort", { form: { session } })).ok()).toBe(true);
  await expect.poll(async () => {
    const payload = await events(before);
    return [payload.sudo_requests.length, payload.events.some((event) => event.type === "agent_settled")];
  }).toEqual([0, true]);
  release();
  await expect(sudoPrompt(page)).toBeHidden();
  await expect(page.getByRole("button", { name: "Abort running Pi" })).toBeHidden();

  await page.keyboard.type("word-canary");
  const composer = page.getByLabel("Message to Pi");
  await expect(composer).toBeVisible();
  await expect(composer).not.toBeFocused();
  await expect(composer).toHaveValue("");
  const storage = await page.evaluate(() => JSON.stringify([{ ...localStorage }, { ...sessionStorage }]));
  expect(storage).not.toContain("word-canary");
});

test("a phone cancels with one tap on actions big enough to tap", async ({ page, isMobile }) => {
  test.skip(!isMobile, "Touch flow");
  const session = await openSession(page, "sudo-mobile", sessions.sudoMobile);
  const { ended } = gripiSudo(session, ["echo", "never"]);
  const prompt = sudoPrompt(page);
  await expect(prompt).toBeVisible();
  const cancel = prompt.getByRole("button", { name: "cancel" });
  for (const control of [prompt.getByLabel("password"), prompt.getByRole("button", { name: "run as root" }), cancel]) {
    const box = await control.boundingBox();
    expect(Math.min(box.width, box.height)).toBeGreaterThanOrEqual(44);
  }

  await cancel.tap();
  await expect(prompt).toBeHidden();
  expect(await ended).toMatchObject({ code: 1, stdout: "" });
});
