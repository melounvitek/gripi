import { expect, test } from "@playwright/test";
import { randomUUID } from "node:crypto";
import { appendFile, readFile, unlink, writeFile } from "node:fs/promises";
import path from "node:path";
import { prompts, replies, sessions } from "../support/contract.mjs";
import { expectRunFinished, message, sendPrompt } from "../support/ui.mjs";

const dialog = (page) => page.getByRole("dialog", { name: "Go to or run" });
const input = (page) => dialog(page).getByRole("combobox");
const options = (page) => dialog(page).getByRole("option");
const status = (page) => dialog(page).getByRole("status");
const command = (page, name) => dialog(page).getByRole("option", { name, exact: true });
const heading = (page, name) => page.getByRole("heading", { level: 1, name, exact: true });
const composer = (page) => page.getByLabel("Message to Pi");
const sessionNames = (page, group) => dialog(page).getByRole("group", { name: group, exact: true }).locator(".command-palette-name");

// Starting with no session open keeps every seeded session on offer, whichever one earlier specs left most recent.
async function open(page) {
  await page.goto("/?no_session=1");
  await page.keyboard.press("Control+k");
  await expect(input(page)).toBeFocused();
  // Rows appear once the sessions have arrived.
  await expect(options(page).first()).toHaveAttribute("aria-selected", "true");
}

async function openSession(page, query, name) {
  await input(page).fill(query);
  await page.keyboard.press("Enter");
  await expect(heading(page, name)).toBeVisible();
}

// Earlier specs can leave sessions unread, and those lead the list, so a test that relies on its order picks the one unread session, if any.
async function serveUnread(page, pick = () => null) {
  await page.route("**/sessions/palette", async (route) => {
    const list = (await (await route.fetch()).json()).sessions;
    const unread = pick(list);
    await route.fulfill({ json: { sessions: list.map((session) => ({ ...session, unread: session === unread })) } });
  });
}

// Holds the session list back until the returned function is called.
async function holdSessions(page, respond) {
  let release;
  const held = new Promise((resolve) => { release = resolve; });
  await page.route("**/sessions/palette", async (route) => {
    await held;
    await respond(route);
  });
  await page.goto("/?no_session=1");
  await page.keyboard.press("Control+k");
  await expect(status(page)).toHaveText("Loading…");
  return release;
}

test("Ctrl+K finds a session by words in any order and Enter opens it", async ({ page, isMobile }) => {
  test.skip(isMobile, "Keyboard flow");
  await open(page);
  await expect(status(page)).toHaveText(/^\d+ more sessions · type to find them$/);

  await input(page).fill("no such session anywhere");
  await expect(options(page)).toHaveCount(0);
  await expect(status(page)).toHaveText("No matches.");

  // The project's path is searched as well as the session's name.
  await input(page).fill("desktop HISTORY-project e2e");
  await expect(options(page)).toHaveText([new RegExp(sessions.history)]);
  await page.keyboard.press("Enter");
  await expect(dialog(page)).toBeHidden();
  await expect(heading(page, sessions.history)).toBeVisible();
  await expect(composer(page)).toBeFocused();

  // The session already open is not offered.
  await page.keyboard.press("Control+k");
  await input(page).fill("desktop HISTORY-project e2e");
  await expect(status(page)).toHaveText("No matches.");
});

test("arrows move the cursor while the input keeps the focus, and Ctrl+K or Escape closes", async ({ page, isMobile }) => {
  test.skip(isMobile, "Keyboard flow");
  await open(page);
  await openSession(page, "contract ready", sessions.marker);
  await page.keyboard.press("Control+k");
  await expect(options(page).first()).toHaveAttribute("aria-selected", "true");
  await page.keyboard.press("ArrowDown");
  await expect(options(page).nth(1)).toHaveAttribute("aria-selected", "true");
  await expect(input(page)).toHaveAttribute("aria-activedescendant", await options(page).nth(1).getAttribute("id"));
  await page.keyboard.press("ArrowUp");
  await page.keyboard.press("ArrowUp");
  await expect(options(page).last()).toHaveAttribute("aria-selected", "true");
  await page.keyboard.press("Tab");
  await expect(input(page)).toBeFocused();

  await page.keyboard.press("Control+k");
  await expect(dialog(page)).toBeHidden();
  await expect(composer(page)).toBeFocused();

  // A reopened palette starts over.
  await page.keyboard.press("Control+k");
  await expect(options(page).first()).toHaveAttribute("aria-selected", "true");
  await page.keyboard.press("Escape");
  await expect(dialog(page)).toBeHidden();
  await expect(composer(page)).toBeFocused();
});

test("the first activation beside the card closes it, and of a row opens that session", async ({ page, isMobile }) => {
  await open(page);
  // There is no close button.
  await (isMobile ? page.touchscreen.tap(20, 20) : page.mouse.click(5, 5));
  await expect(dialog(page)).toBeHidden();

  await page.keyboard.press("Control+k");
  await page.keyboard.type("contract ready");
  const row = options(page).filter({ hasText: sessions.marker });
  await (isMobile ? row.tap() : row.click());
  await expect(dialog(page)).toBeHidden();
  await expect(heading(page, sessions.marker)).toBeVisible();
});

test("Ctrl+K then Enter flips between the last two sessions this tab had open", async ({ page, isMobile }) => {
  test.skip(isMobile, "Keyboard flow");
  await serveUnread(page);
  await open(page);
  await openSession(page, "contract ready", sessions.marker);
  await page.keyboard.press("Control+k");
  await openSession(page, "history desktop", sessions.history);
  for (const name of [sessions.marker, sessions.history]) {
    await page.keyboard.press("Control+k");
    await expect(options(page).first()).toHaveText(new RegExp(name));
    await page.keyboard.press("Enter");
    await expect(heading(page, name)).toBeVisible();
  }
});

test("unread sessions lead, before the session this tab had open before and the newer ones", async ({ page, isMobile }) => {
  test.skip(isMobile, "Keyboard flow");
  let list, unread;
  // The oldest session, other than the two opened below.
  await serveUnread(page, (sessionsList) => {
    list = sessionsList;
    unread = list.findLast((session) => ![sessions.marker, sessions.history].includes(session.name));
    return unread;
  });
  const others = (...skipped) => list.filter((session) => session !== unread && !skipped.includes(session.name)).map((session) => session.name);
  await open(page);
  await expect(sessionNames(page, "Recent sessions")).toHaveText([`Unread: ${unread.name}`, ...others().slice(0, 4)]);

  await openSession(page, "contract ready", sessions.marker);
  await page.keyboard.press("Control+k");
  await openSession(page, "history desktop", sessions.history);
  await page.keyboard.press("Control+k");
  await expect(sessionNames(page, "Recent sessions")).toHaveText([`Unread: ${unread.name}`, sessions.marker, ...others(sessions.marker, sessions.history).slice(0, 3)]);

  // Typed words leave the previous session in its place.
  await input(page).fill("e2e");
  const matching = list.filter((session) => session !== unread && session.name !== sessions.history && `${session.name}\n${session.project}`.toLowerCase().includes("e2e"));
  await expect(sessionNames(page, "Sessions")).toHaveText([`Unread: ${unread.name}`, ...matching.slice(0, 7).map((session) => session.name)]);
});

test("Enter pressed before the sessions arrive opens the first match once they do", async ({ page, isMobile }) => {
  test.skip(isMobile, "Keyboard flow");
  const release = await holdSessions(page, (route) => route.continue());
  await page.keyboard.type(sessions.history);
  await page.keyboard.press("Enter");
  await expect(dialog(page)).toBeVisible();
  release();
  await expect(heading(page, sessions.history)).toBeVisible();
  await expect(dialog(page)).toBeHidden();
});

test("Ctrl and a digit open that numbered session, and not the sidebar's while Ctrl is held on from Ctrl+K", async ({ page, isMobile }) => {
  test.skip(isMobile, "Keyboard flow");
  await serveUnread(page);
  await page.goto("/?no_session=1");
  // With the sidebar's first session open, and so missing from the palette, the two lists number other sessions.
  await page.keyboard.press("Control+1");
  await expect(page.locator('.session-row[data-current="true"] [data-session-shortcut="1"]')).toBeVisible();
  const sidebarSecond = await page.locator(".session-row:has([data-session-shortcut='2'])").getAttribute("data-session-name");

  await page.keyboard.down("Control");
  await page.keyboard.press("k");
  // The commands after the five sessions are numbered too.
  await expect(dialog(page).locator(".command-palette-key:visible")).toHaveText(["1", "2", "3", "4", "5", "6", "7", "8", "9"]);
  const second = await options(page).nth(1).locator(".command-palette-name").textContent();
  expect(second).not.toBe(sidebarSecond);
  await page.keyboard.press("2");
  await page.keyboard.up("Control");
  await expect(dialog(page)).toBeHidden();
  // A second switch would cancel this one.
  await expect(page.locator('.session-row[data-current="true"]')).toHaveAttribute("data-session-name", second);
});

test("Ctrl and a digit pressed before the sessions arrive open that row once they do", async ({ page, isMobile }) => {
  test.skip(isMobile, "Keyboard flow");
  let second;
  const release = await holdSessions(page, async (route) => {
    // An unread session would lead the list.
    const list = (await (await route.fetch()).json()).sessions.map((session) => ({ ...session, unread: false }));
    second = list[1].name;
    await route.fulfill({ json: { sessions: list } });
  });
  await page.keyboard.press("Control+2");
  await expect(dialog(page)).toBeVisible();
  release();
  await expect(dialog(page)).toBeHidden();
  await expect(page.locator('.session-row[data-current="true"]')).toHaveAttribute("data-session-name", second);
});

test("without the sessions the commands still run, but not from an Enter pressed while waiting", async ({ page, isMobile }) => {
  test.skip(isMobile, "Keyboard flow");
  const release = await holdSessions(page, (route) => route.fulfill({ status: 500 }));
  await page.keyboard.press("Enter");
  release();
  await expect(status(page)).toHaveText("Could not load sessions.");
  // The first row is now a command the waiting Enter never saw.
  await expect(options(page).first()).toHaveAttribute("aria-selected", "true");
  await expect(dialog(page)).toBeVisible();

  await command(page, "New session… ctrl+n").click();
  await expect(page.getByRole("dialog", { name: "New session", exact: true })).toBeVisible();
});

test("Ctrl+K leaves another open dialog alone", async ({ page, isMobile }) => {
  test.skip(isMobile, "Keyboard flow");
  await page.goto("/?no_session=1");
  await page.getByRole("button", { name: "New session", exact: true }).click();
  const newSession = page.getByRole("dialog", { name: "New session", exact: true });
  await expect(newSession).toBeVisible();
  await page.keyboard.press("Control+k");
  await expect(newSession).toBeVisible();
  await expect(dialog(page)).toBeHidden();
});

test("commands open what their own controls open and leave a composer draft alone", async ({ page, isMobile }) => {
  test.skip(isMobile, "Keyboard flow");
  await open(page);
  await openSession(page, "contract ready", sessions.marker);
  await composer(page).fill("half-written thought");

  // The slash command a row stands for finds it too.
  await page.keyboard.press("Control+k");
  await page.keyboard.type("/tree");
  await expect(options(page)).toHaveText([/Session tree/]);
  await page.keyboard.press("Enter");
  await expect(dialog(page)).toBeHidden();
  await expect(page.getByRole("dialog", { name: "Session tree" })).toBeVisible();
  await page.keyboard.press("Escape");
  // Its digit opens it as well.
  await page.keyboard.press("Control+k");
  await page.keyboard.type("/tree");
  await page.keyboard.press("Control+1");
  await expect(dialog(page)).toBeHidden();
  await expect(page.getByRole("dialog", { name: "Session tree" })).toBeVisible();
  await page.keyboard.press("Escape");

  for (const [name, opened] of [
    ["Choose model and thinking /model", page.getByRole("dialog", { name: "Model & thinking" })],
    ["Fork from a message /fork", page.getByRole("dialog", { name: "Fork session" })],
    ["New session… ctrl+n", page.getByRole("dialog", { name: "New session", exact: true })]
  ]) {
    await page.keyboard.press("Control+k");
    await command(page, name).click();
    await expect(opened).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(opened).toBeHidden();
  }

  // Opened from the composer, these two give the focus back to it.
  await page.keyboard.press("Control+k");
  await command(page, "Rename…").click();
  await expect(page.getByRole("dialog", { name: "Rename session" }).getByLabel("Name")).toHaveValue(sessions.marker);
  await page.keyboard.press("Escape");
  await expect(composer(page)).toBeFocused();
  await page.keyboard.press("Control+k");
  await command(page, "Tags…").click();
  await expect(page.getByRole("dialog", { name: "Session tags" })).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(composer(page)).toBeFocused();

  await page.keyboard.press("Control+k");
  await command(page, "Find in session ctrl+f").click();
  await expect(page.getByRole("searchbox", { name: "Find in conversation" })).toBeFocused();
  await expect(composer(page)).toHaveValue("half-written thought");
});

test("commands are named after what they would change", async ({ page, isMobile }) => {
  test.skip(isMobile, "Keyboard flow");
  await open(page);
  await openSession(page, "contract ready", sessions.marker);
  // The test above leaves Pi running for this session, and the managed E2E gateway retires it after 2s idle. The page
  // then re-renders and ignores keys meanwhile. Waiting for that keeps it from swallowing a Ctrl+K below.
  if (process.env.GRIPI_E2E_FAKE_PI_LOG) await expect(page.locator("#live-output")).toHaveAttribute("data-session-sync-mode", "available");
  const run = async (name) => {
    await page.keyboard.press("Control+k");
    await command(page, name).click();
    await expect(dialog(page)).toBeHidden();
  };

  const row = page.locator('.session-row[data-current="true"]');
  const pinned = await row.getAttribute("data-pinned") === "true";
  for (const next of [!pinned, pinned]) {
    await run(next ? "Pin" : "Unpin");
    await expect(row).toHaveAttribute("data-pinned", String(next));
  }

  const brief = page.getByRole("group", { name: "Agent activity", exact: true }).getByRole("button", { name: "Brief", exact: true });
  await run("Brief activity");
  await expect(brief).toHaveAttribute("aria-pressed", "true");
  await run("Full activity");
  await expect(brief).toHaveAttribute("aria-pressed", "false");

  const sidebar = page.getByRole("complementary", { name: "Sessions" });
  await run("Hide sidebar");
  await expect(sidebar).toBeHidden();
  await expect(composer(page)).toBeFocused();
  await run("Show sidebar");
  await expect(sidebar).toBeVisible();
});

test("without an open session only the commands that need none are listed", async ({ page, isMobile }) => {
  test.skip(isMobile, "Keyboard flow");
  await open(page);
  await expect(dialog(page).getByRole("group", { name: "Recent sessions" }).getByRole("option")).toHaveCount(5);
  await expect(dialog(page).getByRole("group", { name: "Gripi" }).getByRole("option")).toHaveText([/New session…/, /Environment…/, /Hide sidebar/, /Show only a project…/, /Show only a tag…/, /Turn on notifications/]);
  await expect(dialog(page).getByRole("group", { name: "This session" })).toHaveCount(0);
});

// A draft and an attached image that a command run from the palette must leave as they are.
async function writeDraft(page) {
  await composer(page).fill("half-written thought");
  await page.locator("#image-input").setInputFiles({ name: "draft.png", mimeType: "image/png", buffer: await page.screenshot() });
  await expect(page.locator(".attachment-tray .attachment")).toHaveCount(1);
}

async function expectDraft(page) {
  await expect(composer(page)).toHaveValue("half-written thought");
  await expect(page.locator(".attachment-tray .attachment")).toHaveCount(1);
}

async function run(page, name) {
  await page.keyboard.press("Control+k");
  await command(page, name).click();
  await expect(dialog(page)).toBeHidden();
}

// The managed E2E gateway retires an idle Pi after 2s, and the page then re-renders and ignores keys meanwhile.
async function waitForIdlePiToRetire(page) {
  if (process.env.GRIPI_E2E_FAKE_PI_LOG) await expect(page.locator("#live-output")).toHaveAttribute("data-session-sync-mode", "available");
}

test("Pi's own commands load only once the text starts with a slash, and run steered in while Pi works", async ({ page, isMobile }) => {
  test.skip(isMobile, "Keyboard flow");
  await open(page);
  await openSession(page, "palette commands", sessions.paletteCommands);
  await sendPrompt(page, prompts.steerStart);
  await expect(page.getByRole("button", { name: "Abort running Pi" })).toBeVisible();
  await writeDraft(page);

  // Listing them asks Pi, which may have to start for it.
  const catalogs = [];
  page.on("request", (request) => { if (new URL(request.url()).pathname === "/commands") catalogs.push(request); });
  await page.keyboard.press("Control+k");
  await input(page).fill("steer template");
  await expect(status(page)).toHaveText("No matches.");
  expect(catalogs).toHaveLength(0);

  // Pi CLI refuses to reload while it works.
  await input(page).fill("/");
  await expect(command(page, "/steer-template Steer from a prompt template")).toBeVisible();
  await expect(dialog(page).getByRole("option", { name: /Reload Pi resources/ })).toHaveCount(0);
  await input(page).fill("/steer-t");
  await expect(options(page)).toHaveText([/\/steer-template/]);
  const sent = page.waitForRequest((request) => new URL(request.url()).pathname === "/prompt");
  await page.keyboard.press("Enter");
  const body = (await sent).postData();
  expect(body).toContain('name="message"\r\n\r\n/steer-template\r\n');
  expect(body).toContain('name="streaming_behavior"\r\n\r\nsteer\r\n');
  expect(body).not.toContain("draft.png");
  await expect(message(page, "assistant", replies.steer)).toBeVisible();
  await expectRunFinished(page);
  await expectDraft(page);
});

test("Pi's composer commands run from the palette leave a draft and its attachments alone", async ({ page, isMobile }) => {
  test.skip(isMobile, "Keyboard flow");
  await open(page);
  await openSession(page, "palette commands", sessions.paletteCommands);
  await waitForIdlePiToRetire(page);
  await writeDraft(page);

  await page.keyboard.press("Control+k");
  await input(page).fill("/immediate");
  const handled = page.waitForResponse((response) => response.request().postData()?.includes("/immediate-command"));
  await command(page, "/immediate-command Execute immediately").click();
  expect((await handled).status()).toBe(200);
  await expectDraft(page);
  // Nor does the command show them as its own.
  await expect(message(page, "user", "/immediate-command").locator("img")).toHaveCount(0);

  // Like Pi CLI's /export without a path, it is named after the session file.
  const download = page.waitForEvent("download");
  await run(page, "Download as HTML /export");
  expect((await download).suggestedFilename()).toBe("pi-session-palette-commands.html");
  await expect(page.locator(".composer-state")).toHaveAttribute("data-state", "done");
  await expectDraft(page);

  await run(page, "Reload Pi resources /reload");
  await expect(page.locator(".composer-state")).toHaveText("Reloaded");
  await expectDraft(page);
  // The palette asks again each time, so it lists what the reload loaded.
  await page.keyboard.press("Control+k");
  await input(page).fill("/fresh");
  await expect(command(page, "/fresh-resource Loaded after reload")).toBeVisible();
  await page.keyboard.press("Escape");

  await run(page, "Compact context /compact");
  await expect(page.locator('#live-output [data-pending-compaction="true"]')).toBeVisible();
  await expect(page.locator("#live-output article.message--compaction:not([data-pending-compaction])")).toBeVisible();
  await expectDraft(page);
  // The compaction outlasted the idle time.
  await waitForIdlePiToRetire(page);

  // The clone opens with an empty composer, and the draft stays with the session it was written in.
  const original = new URL(page.url()).searchParams.get("session");
  await run(page, "Clone session /clone");
  await expect.poll(() => new URL(page.url()).searchParams.get("session")).not.toBe(original);
  await expect(heading(page, sessions.paletteCommands)).toBeVisible();
  await expect(composer(page)).toHaveValue("");
  await page.goBack();
  await expect.poll(() => new URL(page.url()).searchParams.get("session")).toBe(original);
  await expect(composer(page)).toHaveValue("half-written thought");
});

test("Pi's commands arriving leave the rows shown in place, under the cursor and a pressed mouse button", async ({ page, isMobile }) => {
  test.skip(isMobile, "Keyboard flow");
  await open(page);
  await openSession(page, "palette commands", sessions.paletteCommands);
  await waitForIdlePiToRetire(page);
  let release;
  const held = new Promise((resolve) => { release = resolve; });
  await page.route("**/commands?**", async (route) => {
    await held;
    await route.continue();
  });
  await page.keyboard.press("Control+k");
  await input(page).fill("/");
  const model = command(page, "Choose model and thinking /model");
  await expect(model).toBeVisible();
  await page.keyboard.press("ArrowDown");
  const selected = await options(page).nth(1).getAttribute("id");
  await model.hover();
  await page.mouse.down();
  release();
  await expect(command(page, "/immediate-command Execute immediately")).toBeVisible();
  await expect(page.locator(`#${selected}`)).toHaveAttribute("aria-selected", "true");
  await page.mouse.up();
  await expect(page.getByRole("dialog", { name: "Model & thinking" })).toBeVisible();
});

test("a session switch the palette did not start closes it, since its commands act on the session they were listed for", async ({ page, isMobile }) => {
  test.skip(isMobile, "Keyboard flow");
  await open(page);
  await openSession(page, "contract ready", sessions.marker);
  await page.keyboard.press("Control+k");
  await expect(command(page, "Session tree /tree")).toBeVisible();
  await page.goBack();
  await expect(dialog(page)).toBeHidden();
  await expect(page.getByRole("heading", { level: 1, name: sessions.marker })).toBeHidden();
});

// A copy of the seeded marker session under a name of its own, to delete or to write to as Pi CLI would.
async function copySession(page, name) {
  const marker = (await (await page.request.get("/sessions/palette")).json()).sessions.find((session) => session.name === sessions.marker);
  const entries = (await readFile(marker.path, "utf8")).trim().split("\n").map(JSON.parse);
  const id = randomUUID();
  const file = path.join(path.dirname(marker.path), `palette-${id}.jsonl`);
  const title = `${name} ${id.slice(0, 8)}`;
  entries[0].id = id;
  entries.find((entry) => entry.type === "session_info").name = title;
  await writeFile(file, entries.map((entry) => `${JSON.stringify(entry)}\n`).join(""));
  return { file, title, entries };
}

test("the filter commands open the sidebar's project list and tag chooser", async ({ page, isMobile }) => {
  test.skip(isMobile, "Keyboard flow");
  await open(page);
  await openSession(page, "contract ready", sessions.marker);
  const sidebar = page.getByRole("complementary", { name: "Sessions" });
  const projectFilter = sidebar.getByRole("combobox", { name: "Filter sessions by project" });
  const allProjects = page.getByRole("option", { name: "All projects" });

  // A hidden sidebar shows for it, and the click that runs it leaves the list open, with the focus on its control.
  await run(page, "Hide sidebar");
  await expect(sidebar).toBeHidden();
  await run(page, "Show only a project…");
  await expect(sidebar).toBeVisible();
  await expect(projectFilter).toHaveAttribute("aria-expanded", "true");
  await expect(allProjects).toBeVisible();
  await expect(projectFilter).toBeFocused();
  await page.keyboard.press("ArrowDown");
  await page.keyboard.press("Enter");
  await expect(allProjects).toBeHidden();
  await expect.poll(() => new URL(page.url()).searchParams.get("project")).toBeTruthy();

  // Anchored to the composer, like Tags…, which then gets the focus back.
  await run(page, "Show only a tag…");
  const tags = page.getByRole("dialog", { name: "Filter by tag" });
  await expect(tags).toBeVisible();
  await expect(tags.getByRole("button", { name: "All tags" })).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(tags).toBeHidden();
  await expect(composer(page)).toBeFocused();
});

test("the notification and gateway update commands do what their sidebar controls do", async ({ page, isMobile }) => {
  test.skip(isMobile, "Keyboard flow");
  await page.addInitScript(() => {
    localStorage.removeItem("gripi:notifications-disabled");
    window.gripiElectron = { showNotification: async () => {} };
  });
  await page.route(/\/gateway-update(\/check)?$/, (route) => route.fulfill({ json: { state: "available", targetSha: "abc12345", message: "1 update commit available" } }));
  await page.goto("/?no_session=1");
  const bell = page.getByRole("complementary", { name: "Sessions" }).getByRole("button", { name: /notifications/i });
  await expect(bell).toHaveAccessibleName("Notifications on — click to disable");
  await run(page, "Turn off notifications");
  await expect(bell).toHaveAccessibleName("Notifications off — click to enable");
  await run(page, "Turn on notifications");
  await expect(bell).toHaveAccessibleName("Notifications on — click to disable");

  await expect(page.getByRole("button", { name: "Update to abc12345" })).toBeVisible();
  const confirmation = new Promise((resolve) => page.once("dialog", (shown) => {
    resolve(shown.message());
    shown.dismiss();
  }));
  await run(page, "Update to abc12345…");
  expect(await confirmation).toContain("Update Gripi to abc12345?");
});

test("Pi CLI sessions are shown and hidden again as the sidebar's button does it", async ({ page, isMobile }) => {
  test.skip(isMobile, "Keyboard flow");
  test.setTimeout(45_000);
  const { file, entries } = await copySession(page, "E2E Palette CLI");
  try {
    // Earlier specs can push it past the sidebar's first page.
    await page.goto("/?no_session=1&show_all_sessions=1");
    const link = page.locator(`a.session[data-session-path="${file}"]`);
    await expect(link).toBeVisible();
    // Pi CLI writes to it.
    const assistant = entries.find((entry) => entry.message?.role === "assistant").message;
    const timestamp = Date.now();
    await appendFile(file, `${JSON.stringify({ type: "message", id: randomUUID().slice(0, 8), parentId: entries.at(-1).id, timestamp: new Date(timestamp).toISOString(), message: { ...assistant, content: [{ type: "text", text: "CLI reply" }], timestamp } })}\n`);
    await expect(link).toHaveCount(0, { timeout: 15_000 });

    const toggle = page.locator("[data-sidebar-cli-toggle]");
    await run(page, "Show Pi CLI sessions");
    await expect(toggle).toHaveAttribute("aria-pressed", "true");
    await expect(link).toBeVisible();
    await run(page, "Hide Pi CLI sessions");
    await expect(toggle).toHaveAttribute("aria-pressed", "false");
    await expect(link).toHaveCount(0);
  } finally {
    await unlink(file);
  }
});

test("Ctrl+D asks to delete the highlighted session, as in Pi CLI's session picker, but not a running one", async ({ page, isMobile }) => {
  test.skip(isMobile, "Keyboard flow");
  const { title } = await copySession(page, "E2E Palette Delete");
  await page.route("**/sessions/palette", async (route) => {
    const list = (await (await route.fetch()).json()).sessions;
    await route.fulfill({ json: { sessions: list.map((session) => ({ ...session, busy: session.name === title })) } });
  });
  await page.goto("/?no_session=1");
  await page.keyboard.press("Control+k");
  await input(page).fill(title);
  await expect(options(page)).toHaveText([new RegExp(title)]);
  await page.keyboard.press("Control+d");
  await expect(status(page)).toHaveText("Cannot delete a running session");
  await expect(dialog(page)).toBeVisible();
  await page.keyboard.press("Escape");
  await page.unroute("**/sessions/palette");

  await page.keyboard.press("Control+k");
  await input(page).fill(title);
  await expect(options(page)).toHaveText([new RegExp(title)]);
  await page.keyboard.press("Control+d");
  await expect(dialog(page)).toBeHidden();
  const confirm = page.getByRole("dialog", { name: "Delete session" });
  await expect(confirm).toContainText(title);
  await confirm.getByRole("button", { name: "Delete session", exact: true }).click();
  await expect(confirm).toBeHidden();
  await page.keyboard.press("Control+k");
  await input(page).fill(title);
  await expect(status(page)).toHaveText("No matches.");
});

test("the desktop app's menu opens and closes the palette as Ctrl+K does", async ({ page, isMobile }) => {
  test.skip(isMobile, "Desktop app");
  await page.goto("/?no_session=1");
  const fromMenu = () => page.evaluate(() => window.dispatchEvent(new Event("gripi:command-palette-requested")));
  await fromMenu();
  await expect(input(page)).toBeFocused();
  await expect(options(page).first()).toHaveAttribute("aria-selected", "true");
  await fromMenu();
  await expect(dialog(page)).toBeHidden();
});
