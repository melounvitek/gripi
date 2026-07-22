import { expect, test } from "@playwright/test";
import { sessions } from "../support/contract.mjs";
import { message } from "../support/ui.mjs";

test("switch focus between the composer and conversation in a narrow desktop window", async ({ page }) => {
  await page.goto("/");

  await searchSessions(page, "History Desktop");
  await page.getByRole("link", { name: new RegExp(sessions.history) }).click();
  await expect(page.getByRole("heading", { level: 1, name: sessions.history })).toBeVisible();
  await expect(page.getByRole("searchbox", { name: "Find in conversation" })).toBeHidden();
  await page.setViewportSize({ width: 600, height: 900 });

  const composer = page.locator('textarea[name="message"]');
  const conversation = page.locator("#conversation-scroll");
  await expect(composer).toBeFocused();
  await page.keyboard.press("Tab");
  await expect(conversation).toBeFocused();

  await page.keyboard.press("Tab");
  await expect(composer).toBeFocused();
});

test("opens conversation find for a known session search match without trapping scroll", async ({ page }) => {
  await page.goto("/");

  await searchSessions(page, "Persisted browser");
  await page.getByRole("link", { name: new RegExp(sessions.history) }).click();

  const find = page.getByRole("searchbox", { name: "Find in conversation" });
  await expect(find).toBeVisible();
  await expect(find).toHaveValue("Persisted browser");
  const count = page.locator("[data-current-session-find-count]");
  await expect(count).toHaveText("1 / 2");
  await expect(page.locator("mark.current-session-find-match.is-active")).toHaveText("Persisted browser");

  await page.getByRole("button", { name: "Next match" }).click();
  await expect(count).toHaveText("2 / 2");
  await expect(message(page, "assistant", "Persisted browser answer").locator("mark.current-session-find-match.is-active")).toHaveText("Persisted browser");

  await page.getByRole("button", { name: "Close find" }).click();
  await page.getByRole("link", { name: new RegExp(sessions.history) }).click();
  await expect(find).toBeVisible();
  await expect(find).toHaveValue("Persisted browser");

  const scroll = page.locator("#conversation-scroll");
  const manualTop = await scroll.evaluate((element) => {
    const spacer = document.createElement("div");
    spacer.style.height = "2000px";
    element.querySelector("#live-output").before(spacer);
    element.scrollTop = element.scrollHeight;
    return element.scrollTop;
  });
  await expect.poll(() => scroll.evaluate((element) => element.scrollTop)).toBe(manualTop);
});

test("find, select, and pin a session with persisted history", async ({ page }) => {
  await page.goto("/");

  await searchSessions(page, "History Desktop");
  let session = page.getByRole("link", { name: new RegExp(sessions.history) });
  await expect(session).toBeVisible();
  await session.click();

  await expect(page.getByRole("heading", { level: 1, name: sessions.history })).toBeVisible();
  await expect(page.getByRole("link", { name: new RegExp(sessions.history) })).toHaveAttribute("aria-current", "page");
  await expect(message(page, "user", "Persisted browser question")).toBeVisible();
  await expect(message(page, "assistant", "Persisted browser answer")).toBeVisible();

  session = page.getByRole("link", { name: new RegExp(sessions.history) });
  const row = page.locator(".session-row").filter({ has: session });
  await row.getByRole("button", { name: "Pin session" }).click();
  await expect(row.getByRole("button", { name: "Unpin session" })).toBeVisible();
  await expect(page.getByRole("heading", { level: 2, name: "Pinned" })).toBeVisible();
});

test("a newer session switch wins when fragment responses arrive out of order", async ({ page }) => {
  await page.goto("/");
  const olderLink = page.getByRole("link", { name: new RegExp(sessions.marker) });
  const newerLink = page.getByRole("link", { name: new RegExp(sessions.promptRetryCompact) });
  await expect(olderLink).toBeVisible();
  await expect(newerLink).toBeVisible();
  const olderSession = new URL(await olderLink.getAttribute("href"), page.url()).searchParams.get("session");
  const newerSession = new URL(await newerLink.getAttribute("href"), page.url()).searchParams.get("session");

  let markOlderFragmentRequested;
  const olderFragmentRequested = new Promise((resolve) => { markOlderFragmentRequested = resolve; });
  let markNewerFragmentCompleted;
  const newerFragmentCompleted = new Promise((resolve) => { markNewerFragmentCompleted = resolve; });
  const fragmentCompletionOrder = [];
  const eventSessions = [];
  await page.route(/\/events(?:\?|$)/, async (route) => {
    eventSessions.push(new URL(route.request().url()).searchParams.get("session"));
    await route.continue();
  });
  await page.route(/\/session_fragment(?:\?|$)/, async (route) => {
    const session = new URL(route.request().url()).searchParams.get("session");
    if (session === olderSession) {
      markOlderFragmentRequested();
      await newerFragmentCompleted;
    }
    const response = await route.fetch();
    await route.fulfill({ response });
    fragmentCompletionOrder.push(session);
    if (session === newerSession) markNewerFragmentCompleted();
  });

  await olderLink.click();
  await olderFragmentRequested;
  await page.evaluate((href) => {
    history.pushState({}, "", href);
    window.dispatchEvent(new PopStateEvent("popstate"));
  }, await newerLink.getAttribute("href"));

  await expect(page.getByRole("heading", { level: 1, name: sessions.promptRetryCompact })).toBeVisible();
  await expect.poll(() => fragmentCompletionOrder).toEqual([newerSession, olderSession]);
  await expect(page.getByRole("heading", { level: 1, name: sessions.promptRetryCompact })).toBeVisible();
  await expect.poll(() => new URL(page.url()).searchParams.get("session")).toBe(newerSession);
  await expect.poll(() => eventSessions.at(-1)).toBe(newerSession);
  await expect(page.locator("#live-output")).toHaveAttribute("data-events-url", new RegExp(encodeURIComponent(newerSession)));
});

async function searchSessions(page, query) {
  await page.getByRole("button", { name: "Search sessions" }).click();
  const search = page.getByRole("searchbox", { name: "Search sessions" });
  await search.fill(query);
  await Promise.all([
    page.waitForURL((url) => url.searchParams.get("session_search") === query, { waitUntil: "domcontentloaded" }),
    search.press("Enter")
  ]);
}
