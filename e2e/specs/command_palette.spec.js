import { expect, test } from "@playwright/test";
import { sessions } from "../support/contract.mjs";

const dialog = (page) => page.getByRole("dialog", { name: "Go to or run" });
const input = (page) => dialog(page).getByRole("combobox");
const options = (page) => dialog(page).getByRole("option");
const heading = (page, name) => page.getByRole("heading", { level: 1, name, exact: true });
const composer = (page) => page.getByLabel("Message to Pi");

async function open(page) {
  await page.goto("/");
  await page.keyboard.press("Control+k");
  await expect(input(page)).toBeFocused();
}

test("Ctrl+K finds a session by words in any order and Enter opens it", async ({ page, isMobile }) => {
  test.skip(isMobile, "Keyboard flow");
  await open(page);
  await expect(options(page).first()).toHaveAttribute("aria-selected", "true");
  await expect(dialog(page).getByRole("status")).toHaveText(/^\d+ more sessions · type to find them$/);
  // The session already open is not offered.
  const current = await page.locator('.prompt-form input[name="session"]').inputValue();
  await page.keyboard.type(await page.locator(".session-header-name").textContent());
  await expect(dialog(page).locator(`[data-session-path="${current}"]`)).toHaveCount(0);

  await input(page).fill("no such session anywhere");
  await expect(options(page)).toHaveCount(0);
  await expect(dialog(page).getByRole("status")).toHaveText("No matches.");

  // The project's path is searched as well as the session's name.
  await input(page).fill("desktop HISTORY-project e2e");
  await expect(options(page)).toHaveText([new RegExp(sessions.history)]);
  await page.keyboard.press("Enter");
  await expect(dialog(page)).toBeHidden();
  await expect(heading(page, sessions.history)).toBeVisible();
  await expect(composer(page)).toBeFocused();
});

test("arrows move the cursor while the input keeps the focus, and Ctrl+K, Escape or a click outside closes", async ({ page, isMobile }) => {
  test.skip(isMobile, "Keyboard flow");
  await open(page);
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

  // With no close button, a click beside the card closes it.
  await page.keyboard.press("Control+k");
  await expect(dialog(page)).toBeVisible();
  await page.mouse.click(5, 5);
  await expect(dialog(page)).toBeHidden();
});

test("the first activation of a row opens that session", async ({ page, isMobile }) => {
  await open(page);
  await page.keyboard.type("contract ready");
  const row = options(page).filter({ hasText: sessions.marker });
  await (isMobile ? row.tap() : row.click());
  await expect(dialog(page)).toBeHidden();
  await expect(heading(page, sessions.marker)).toBeVisible();
});

test("Enter pressed before the sessions arrive opens the first match once they do", async ({ page, isMobile }) => {
  test.skip(isMobile, "Keyboard flow");
  await page.goto("/");
  let release;
  const held = new Promise((resolve) => { release = resolve; });
  await page.route("**/sessions/palette", async (route) => {
    await held;
    await route.continue();
  });
  await page.keyboard.press("Control+k");
  await expect(dialog(page).getByRole("status")).toHaveText("Loading…");
  await page.keyboard.type(sessions.history);
  await page.keyboard.press("Enter");
  await expect(dialog(page)).toBeVisible();
  release();
  await expect(heading(page, sessions.history)).toBeVisible();
  await expect(dialog(page)).toBeHidden();
});

test("Ctrl+K leaves another open dialog alone", async ({ page, isMobile }) => {
  test.skip(isMobile, "Keyboard flow");
  await page.goto("/");
  await page.getByRole("button", { name: "New session", exact: true }).click();
  const newSession = page.getByRole("dialog", { name: "New session", exact: true });
  await expect(newSession).toBeVisible();
  await page.keyboard.press("Control+k");
  await expect(newSession).toBeVisible();
  await expect(dialog(page)).toBeHidden();
});
