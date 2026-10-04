import { expect, test } from "@playwright/test";
import { sessions } from "../support/contract.mjs";

const dialog = (page) => page.getByRole("dialog", { name: "Go to or run" });
const input = (page) => dialog(page).getByRole("combobox");
const options = (page) => dialog(page).getByRole("option");
const status = (page) => dialog(page).getByRole("status");
const command = (page, name) => dialog(page).getByRole("option", { name, exact: true });
const heading = (page, name) => page.getByRole("heading", { level: 1, name, exact: true });
const composer = (page) => page.getByLabel("Message to Pi");

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
  await expect(dialog(page).getByRole("group", { name: "Gripi" }).getByRole("option")).toHaveText([/New session…/, /Hide sidebar/]);
  await expect(dialog(page).getByRole("group", { name: "This session" })).toHaveCount(0);
});
