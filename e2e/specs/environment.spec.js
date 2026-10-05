import { expect, test } from "@playwright/test";
import { sessions } from "../support/contract.mjs";
import { selectSession, sendPrompt } from "../support/ui.mjs";

const GITHUB_LINES = [
  "# gh: pull requests, issues, API",
  "GH_TOKEN=",
  "# your name and email on commits",
  "GIT_AUTHOR_NAME=",
  "GIT_AUTHOR_EMAIL=",
  "GIT_COMMITTER_NAME=",
  "GIT_COMMITTER_EMAIL=",
  "# git push over HTTPS with your token, also for SSH remotes; leave as is",
  "GIT_CONFIG_COUNT=2",
  "GIT_CONFIG_KEY_0=url.https://github.com/.insteadOf",
  "GIT_CONFIG_VALUE_0=git@github.com:",
  "GIT_CONFIG_KEY_1=credential.https://github.com.helper",
  "GIT_CONFIG_VALUE_1=!gh auth git-credential"
];
// Every row starts with the cursor glyph, which shows on the selected one only.
const ADD = "→+ add variable";
const variable = (name) => `→${name}••••••••`;
const DANGER = "rgb(204, 102, 102)";

const sidebar = (page) => page.getByRole("complementary", { name: "Sessions" });
const key = (page) => sidebar(page).getByRole("button", { name: "Environment variables" });
const dialog = (page) => page.getByRole("dialog", { name: "Environment" });
const rows = (page) => dialog(page).getByRole("option");
const row = (page, name) => dialog(page).getByRole("option", { name, exact: true });
const githubRow = (page) => row(page, "+ add GitHub variables gh, commits and push as you");
const status = (page) => dialog(page).getByRole("status");
const nameInput = (page) => dialog(page).getByLabel("Name:");
const valueInput = (page) => dialog(page).getByLabel("Value:");
const block = (page) => dialog(page).getByRole("textbox", { name: "NAME=value lines" });
const button = (page, name) => dialog(page).getByRole("button", { name, exact: true });
const activate = (control, mobile) => mobile ? control.tap() : control.click();
const savedNames = async (page) => (await (await page.request.get("/environment")).json()).variables.map(({ name }) => name);

async function open(page, mobile) {
  await page.goto("/?no_session=1");
  if (mobile) await activate(page.locator('label[aria-label="Open sessions"]'), mobile);
  await activate(key(page), mobile);
  // Rows appear once the saved names have arrived.
  await expect(rows(page).first()).toHaveAttribute("aria-selected", "true");
}

async function add(page, mobile, name, value) {
  await activate(row(page, "+ add variable"), mobile);
  await nameInput(page).fill(name);
  await valueInput(page).fill(value);
  await activate(button(page, "save"), mobile);
}

// The page reads only the pasted text, so the event stands in for the browser's own paste, which needs clipboard permissions.
async function paste(field, text) {
  await field.evaluate((element, text) => {
    const clipboardData = new DataTransfer();
    clipboardData.setData("text/plain", text);
    element.dispatchEvent(new ClipboardEvent("paste", { clipboardData, bubbles: true, cancelable: true }));
  }, text);
}

// Looks at the markup and at what the form fields hold, which the markup does not show.
function pageHolds(page, value) {
  return page.evaluate((value) => document.documentElement.outerHTML.includes(value) || [...document.querySelectorAll("input, textarea")].some((field) => field.value.includes(value)), value);
}

// One gateway serves every spec, and Pi would get what is left here in all of them.
test.afterEach(async ({ page }) => {
  for (const name of await savedNames(page)) await page.request.post("/environment/variable/delete", { form: { name } });
});

test("the key button and the Ctrl+K command open the dialog", async ({ page, isMobile }) => {
  test.skip(isMobile, "Keyboard flow");
  await open(page, isMobile);
  await expect(dialog(page)).toContainText("Pi and every command it runs get these variables in every Gripi session, on top of the gateway's own environment.");
  await expect(rows(page)).toHaveText([ADD, "→+ add GitHub variables gh, commits and push as you"]);
  await expect(status(page)).toHaveText("Nothing set yet.");
  await expect(dialog(page)).toContainText("↑↓ navigate · enter open · esc close");

  // Closing lets the sidebar refresh, which replaces the key button.
  await key(page).evaluate((element) => element.setAttribute("data-before-refresh", ""));
  await page.keyboard.press("Escape");
  await expect(dialog(page)).toBeHidden();
  await expect(key(page)).not.toHaveAttribute("data-before-refresh");
  await key(page).click();
  await expect(dialog(page)).toBeVisible();
  await page.keyboard.press("Escape");

  await page.keyboard.press("Control+k");
  await page.getByRole("group", { name: "Gripi" }).getByRole("option", { name: "Environment…", exact: true }).click();
  await expect(rows(page).first()).toBeFocused();
});

test("a variable is added, changed, renamed and removed, and its value stays out of the page", async ({ page, isMobile }) => {
  await open(page, isMobile);
  await activate(row(page, "+ add variable"), isMobile);
  await expect(dialog(page)).toContainText("enter save · esc cancel");
  // There is nothing to remove yet.
  await expect(button(page, "remove")).toBeHidden();
  await nameInput(page).fill("E2E_TOKEN");
  await valueInput(page).fill("fake-first-value");
  await activate(button(page, "save"), isMobile);
  await expect(status(page)).toHaveText("Saved · used from your next message.");
  await expect(rows(page)).toHaveText([variable("E2E_TOKEN"), ADD, /GitHub/]);
  expect(await pageHolds(page, "fake-first-value")).toBe(false);

  // Only the form of the opened row gets the value.
  await activate(row(page, "E2E_TOKEN"), isMobile);
  await expect(nameInput(page)).toHaveValue("E2E_TOKEN");
  await expect(valueInput(page)).toHaveValue("fake-first-value");
  await valueInput(page).fill("fake-second-value");
  await activate(button(page, "save"), isMobile);
  await expect(valueInput(page)).toBeHidden();

  await activate(row(page, "E2E_TOKEN"), isMobile);
  await expect(valueInput(page)).toHaveValue("fake-second-value");
  await nameInput(page).fill("E2E_RENAMED");
  await activate(button(page, "save"), isMobile);
  await expect(rows(page)).toHaveText([variable("E2E_RENAMED"), ADD, /GitHub/]);

  // Cancelling the form and closing the dialog both drop the value.
  await activate(row(page, "E2E_RENAMED"), isMobile);
  await expect(valueInput(page)).toHaveValue("fake-second-value");
  await activate(button(page, "cancel"), isMobile);
  await expect(valueInput(page)).toBeHidden();
  expect(await pageHolds(page, "fake-second-value")).toBe(false);
  await activate(row(page, "E2E_RENAMED"), isMobile);
  await expect(valueInput(page)).toHaveValue("fake-second-value");
  await activate(button(page, "Close"), isMobile);
  await expect(dialog(page)).toBeHidden();
  expect(await pageHolds(page, "fake-second-value")).toBe(false);

  await activate(key(page), isMobile);
  await activate(row(page, "E2E_RENAMED"), isMobile);
  await activate(button(page, "remove"), isMobile);
  await expect(status(page)).toHaveText("Removed · applies from your next message.");
  await expect(rows(page)).toHaveText([ADD, /GitHub/]);
  expect(await savedNames(page)).toEqual([]);
});

test("what the gateway rejects is shown in its own words and the form stays open", async ({ page, isMobile }) => {
  await open(page, isMobile);
  await add(page, isMobile, "GRIPI_PORT", "fake-value");
  await expect(status(page)).toHaveText("“GRIPI_PORT” is reserved for Gripi and Pi.");
  await expect(status(page)).toHaveCSS("color", DANGER);
  await expect(nameInput(page)).toHaveValue("GRIPI_PORT");
  await expect(valueInput(page)).toHaveValue("fake-value");
});

test("a state file the gateway cannot read is reported instead of an empty list", async ({ page, isMobile }) => {
  await page.route("**/environment", (route) => route.fulfill({ status: 500, json: { error: "Unable to read or save environment variables" } }));
  await page.goto("/?no_session=1");
  if (isMobile) await activate(page.locator('label[aria-label="Open sessions"]'), isMobile);
  await activate(key(page), isMobile);
  await expect(status(page)).toHaveText("Unable to read or save environment variables");
  await expect(status(page)).toHaveCSS("color", DANGER);
  await expect(rows(page)).toHaveCount(0);
});

test("lines pasted into Name are saved as a block, or not at all", async ({ page, isMobile }) => {
  await open(page, isMobile);
  await activate(row(page, "+ add variable"), isMobile);
  // One line fills in both fields, without the quotes a .env file puts around a value.
  await paste(nameInput(page), 'E2E_ONE="fake one"\n');
  await expect(nameInput(page)).toHaveValue("E2E_ONE");
  await expect(valueInput(page)).toHaveValue("fake one");

  await paste(nameInput(page), "E2E_ONE=fake-one\nnot a pair\nE2E_TWO=fake-two");
  await expect(dialog(page)).toContainText("One NAME=value per line. Lines starting with # are ignored.");
  await expect(dialog(page)).toContainText("ctrl+enter save · esc cancel");
  await expect(block(page)).toHaveValue("E2E_ONE=fake-one\nnot a pair\nE2E_TWO=fake-two");
  await activate(button(page, "save"), isMobile);
  await expect(status(page)).toHaveText("Line 2: expected NAME=value.");
  await expect(status(page)).toHaveCSS("color", DANGER);
  expect(await savedNames(page)).toEqual([]);

  await block(page).fill("E2E_ONE=fake-one\n# a note\nE2E_TWO=fake-two");
  if (isMobile) await button(page, "save").tap();
  else await page.keyboard.press("Control+Enter");
  await expect(status(page)).toHaveText("Saved 2 variables · used from your next message.");
  await expect(rows(page)).toHaveText([variable("E2E_ONE"), variable("E2E_TWO"), ADD, /GitHub/]);
});

test("the GitHub row offers the lines that are still missing", async ({ page, isMobile }) => {
  await open(page, isMobile);
  await activate(githubRow(page), isMobile);
  await expect(dialog(page)).toContainText("GitHub: act as yourself");
  await expect(dialog(page)).toContainText("Fill in the empty values and save. Lines starting with # are ignored. Create the token at github.com/settings/tokens; a classic token needs the repo, read:org and gist scopes.");
  await expect(block(page)).toHaveValue(GITHUB_LINES.join("\n"));
  // The block is as tall as its lines, with no scrollbar of its own.
  expect(await block(page).evaluate((element) => element.scrollHeight - element.clientHeight)).toBe(0);
  await activate(button(page, "cancel"), isMobile);

  await add(page, isMobile, "GH_TOKEN", "fake-github-token");
  const warning = dialog(page).getByText("GH_TOKEN covers gh only. Commits and git push still use the gateway's identity.");
  await expect(warning).toHaveCSS("color", "rgb(240, 198, 116)");
  await expect(rows(page)).toHaveText([variable("GH_TOKEN"), ADD, "→+ add the rest for GitHub"]);
  await activate(row(page, "+ add the rest for GitHub"), isMobile);
  const missing = GITHUB_LINES.slice(2);
  await expect(block(page)).toHaveValue(missing.join("\n"));
  await block(page).fill(missing.map((line) => line.endsWith("=") ? `${line}Fake Person` : line).join("\n"));
  await activate(button(page, "save"), isMobile);

  await expect(status(page)).toHaveText("Saved 9 variables · used from your next message.");
  await expect(rows(page)).toHaveCount(11);
  await expect(rows(page).last()).toHaveText(ADD);
  await expect(warning).toBeHidden();
});

test("arrows move the cursor, Enter opens a row, and Escape closes the form before the dialog", async ({ page, isMobile }) => {
  test.skip(isMobile, "Keyboard flow");
  await open(page, isMobile);
  await expect(row(page, "+ add variable")).toBeFocused();
  await page.keyboard.press("Enter");
  await expect(nameInput(page)).toBeFocused();
  await page.keyboard.type("E2E_TOKEN");
  await page.keyboard.press("Tab");
  await page.keyboard.type("fake-value");
  await page.keyboard.press("Enter");
  // The cursor is on the variable that was just saved.
  await expect(row(page, "E2E_TOKEN")).toBeFocused();
  await expect(row(page, "E2E_TOKEN")).toHaveAttribute("aria-selected", "true");

  await page.keyboard.press("ArrowDown");
  await expect(row(page, "+ add variable")).toBeFocused();
  await page.keyboard.press("ArrowDown");
  await page.keyboard.press("ArrowDown");
  await expect(row(page, "E2E_TOKEN")).toBeFocused();
  await page.keyboard.press("ArrowUp");
  await expect(githubRow(page)).toBeFocused();
  await expect(rows(page).and(dialog(page).locator('[aria-selected="true"]'))).toHaveCount(1);

  await page.keyboard.press("Enter");
  await expect(block(page)).toBeFocused();
  // The caret waits at the first value to fill in, not after the lines to leave as they are.
  await page.keyboard.type("fake-token");
  await expect(block(page)).toHaveValue(GITHUB_LINES.join("\n").replace("GH_TOKEN=", "GH_TOKEN=fake-token"));
  await page.keyboard.press("Escape");
  await expect(block(page)).toBeHidden();
  await expect(githubRow(page)).toBeFocused();

  await page.keyboard.press("ArrowDown");
  await page.keyboard.press("Enter");
  await expect(valueInput(page)).toBeFocused();
  await expect(valueInput(page)).toHaveValue("fake-value");
  // The arrows belong to the field while the form is open.
  await page.keyboard.press("ArrowDown");
  await expect(valueInput(page)).toBeFocused();
  await page.keyboard.press("Escape");
  await expect(valueInput(page)).toBeHidden();
  await expect(row(page, "E2E_TOKEN")).toBeFocused();
  await page.keyboard.press("Escape");
  await expect(dialog(page)).toBeHidden();
});

test("on a phone the first tap opens the dialog and a row, and every target is tall enough", async ({ page, isMobile }) => {
  test.skip(!isMobile, "Touch flow");
  await page.setViewportSize({ width: 320, height: 568 });
  await page.goto("/?no_session=1");
  await page.locator('label[aria-label="Open sessions"]').tap();
  await expect(sidebar(page)).toHaveCSS("transform", "matrix(1, 0, 0, 1, 0, 0)");
  const [bell, bounds, close] = await Promise.all([sidebar(page).getByRole("button", { name: /notifications/i }), key(page), sidebar(page).getByLabel("Close sessions")].map((part) => part.boundingBox()));
  expect([bounds.width, bounds.height]).toEqual([44, 44]);
  expect(bounds.x).toBeGreaterThanOrEqual(bell.x + bell.width);
  expect(bounds.x + bounds.width).toBeLessThanOrEqual(close.x);

  await key(page).tap();
  await expect(rows(page).first()).toHaveAttribute("aria-selected", "true");
  // A bottom sheet, like the other dialogs.
  const sheet = await dialog(page).boundingBox();
  expect([sheet.x, sheet.width, sheet.y + sheet.height]).toEqual([0, 320, 568]);
  await row(page, "+ add variable").tap();
  await expect(nameInput(page)).toBeVisible();
  await nameInput(page).fill("E2E_TOKEN");
  await valueInput(page).fill("fake-value");
  await button(page, "save").tap();
  await row(page, "E2E_TOKEN").tap();
  await expect(valueInput(page)).toHaveValue("fake-value");

  for (const target of await dialog(page).locator("button:visible").all()) {
    expect((await target.boundingBox()).height, await target.textContent()).toBeGreaterThanOrEqual(44);
  }
});

test("on a phone the GitHub block stays in reach above the on-screen keyboard", async ({ page, isMobile }) => {
  test.skip(!isMobile, "Touch flow");
  await open(page, isMobile);
  await githubRow(page).tap();
  await expect(block(page)).toBeVisible();
  // The keyboard covers the bottom of the page without resizing it: only the visual viewport shrinks.
  const visible = 320;
  await page.evaluate((height) => {
    Object.defineProperty(window.visualViewport, "height", { get: () => height });
    window.visualViewport.dispatchEvent(new Event("resize"));
  }, visible);

  const sheet = await dialog(page).boundingBox();
  expect(sheet.y).toBeGreaterThanOrEqual(0);
  expect(sheet.y + sheet.height).toBeLessThanOrEqual(visible);
  for (const part of [dialog(page).getByText("GitHub: act as yourself"), button(page, "save")]) {
    await part.scrollIntoViewIfNeeded();
    const bounds = await part.boundingBox();
    expect(bounds.y).toBeGreaterThanOrEqual(sheet.y);
    expect(bounds.y + bounds.height).toBeLessThanOrEqual(visible);
  }
});

test("Pi gets a saved variable from the next message", async ({ page, isMobile }) => {
  test.skip(isMobile, "Not specific to touch");
  await page.goto("/");
  await selectSession(page, sessions.environment);
  await key(page).click();
  await add(page, isMobile, "E2E_ENVIRONMENT_TOKEN", "fake-token-for-pi");
  await expect(status(page)).toHaveText("Saved · used from your next message.");
  await page.keyboard.press("Escape");

  await sendPrompt(page, "!printenv E2E_ENVIRONMENT_TOKEN");
  await expect(page.locator('article[data-role="bashExecution"]').filter({ hasText: "$ printenv E2E_ENVIRONMENT_TOKEN" })).toContainText("fake-token-for-pi");
});
