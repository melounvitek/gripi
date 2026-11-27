import { expect, test } from "@playwright/test";
import { sessions } from "../support/contract.mjs";
import { selectSession } from "../support/ui.mjs";

function sessionName(page) {
  return page.locator(".session-header-name");
}

function replyLink(page, name) {
  return page.locator('article[data-role="assistant"]').getByRole("link", { name });
}

function historyLink(page) {
  return replyLink(page, "the history session");
}

test("a reply's link to a session opens it in place, or in a new tab with Ctrl", async ({ page, context }) => {
  await page.goto("/");
  await selectSession(page, sessions.sessionLink);
  await expect(sessionName(page)).toHaveText(sessions.sessionLink);
  // A page that loaded again would lose this.
  await page.evaluate(() => { window.e2eSamePage = true; });

  await historyLink(page).click();
  await expect(sessionName(page)).toHaveText(sessions.history);
  expect(new URL(page.url()).searchParams.get("session")).toMatch(/\/history\.jsonl$/);
  expect(await page.evaluate(() => window.e2eSamePage)).toBe(true);
  await page.goBack();
  await expect(sessionName(page)).toHaveText(sessions.sessionLink);

  const [tab] = await Promise.all([context.waitForEvent("page"), historyLink(page).click({ modifiers: ["ControlOrMeta"] })]);
  await expect(sessionName(tab)).toHaveText(sessions.history);
  await tab.close();
  await expect(sessionName(page)).toHaveText(sessions.sessionLink);
});

test("of the sessions opened while a link's session is looked up, the last one stays open", async ({ page }) => {
  await page.goto("/");
  await selectSession(page, sessions.sessionLink);
  const held = new Map();
  await page.route("**/sessions/e2e-*", (route) => { held.set(new URL(route.request().url()).pathname, route); });
  const answer = async (path) => {
    await expect.poll(() => held.has(path)).toBe(true);
    const answered = page.waitForResponse(`**${path}`);
    await held.get(path).continue();
    held.delete(path);
    await answered;
  };

  // Another link.
  await historyLink(page).click();
  await replyLink(page, "the prompt session").click();
  await answer("/sessions/e2e-history");
  await answer("/sessions/e2e-prompt");
  await expect(sessionName(page)).toHaveText(sessions.prompt);
  await page.goBack();
  await expect(sessionName(page)).toHaveText(sessions.sessionLink);

  // A session from the sidebar.
  await historyLink(page).click();
  await selectSession(page, sessions.marker);
  await expect(sessionName(page)).toHaveText(sessions.marker);
  await answer("/sessions/e2e-history");
  await page.waitForTimeout(500);
  await expect(sessionName(page)).toHaveText(sessions.marker);
});
