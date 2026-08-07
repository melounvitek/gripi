import { expect, test } from "@playwright/test";
import { sessions } from "../support/contract.mjs";
import { message } from "../support/ui.mjs";

test("hide the desktop sidebar and remember the preference", async ({ page }) => {
  await page.goto("/");

  const sidebar = page.getByRole("complementary", { name: "Sessions" });
  const toggle = page.locator(".session-header [data-sidebar-visibility-toggle]");
  await expect(sidebar).toBeVisible();
  await expect(toggle).toBeVisible();
  await expect(toggle).toHaveAttribute("aria-label", "Hide sessions");
  await expect(toggle).toHaveAttribute("aria-expanded", "true");

  await toggle.click();

  await expect(sidebar).toBeHidden();
  await expect(toggle).toHaveAttribute("aria-label", "Show sessions");
  await expect(toggle).toHaveAttribute("aria-expanded", "false");
  await expect.poll(() => page.evaluate(() => localStorage.getItem("gripi:desktop-sidebar-hidden"))).toBe("true");

  await page.setViewportSize({ width: 760, height: 900 });
  const mobileToggle = page.locator('.session-header label[aria-label="Open sessions"]');
  await expect(toggle).toBeHidden();
  await expect(mobileToggle).toBeVisible();
  await expect(page.locator("#mobile-session-toggle")).not.toBeChecked();
  await mobileToggle.click();
  await expect(page.locator("#mobile-session-toggle")).toBeChecked();
  await expect(sidebar).toBeVisible();
  await expect.poll(() => page.evaluate(() => localStorage.getItem("gripi:desktop-sidebar-hidden"))).toBe("true");

  await page.setViewportSize({ width: 761, height: 900 });
  await expect(sidebar).toBeHidden();
  await expect(toggle).toBeVisible();

  await page.reload();

  await expect(sidebar).toBeHidden();
  await expect(toggle).toHaveAttribute("aria-label", "Show sessions");

  await page.keyboard.press("Control+Shift+f");

  await expect(sidebar).toBeVisible();
  await expect(page.getByRole("searchbox", { name: "Search sessions" })).toBeVisible();
  await expect.poll(() => page.evaluate(() => localStorage.getItem("gripi:desktop-sidebar-hidden"))).toBe("true");

  await page.reload();
  await expect(sidebar).toBeHidden();
  await toggle.click();

  await expect(sidebar).toBeVisible();
  await expect(toggle).toHaveAttribute("aria-label", "Hide sessions");
  await expect(toggle).toHaveAttribute("aria-expanded", "true");
  await expect.poll(() => page.evaluate(() => localStorage.getItem("gripi:desktop-sidebar-hidden"))).toBe(null);
});

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

test("clears session filters without reloading the page", async ({ page }) => {
  await page.goto("/");

  await searchSessions(page, "History Desktop");
  const clearFilters = page.getByRole("link", { name: "Clear filters" });
  await expect(clearFilters).toBeVisible();
  await page.evaluate(() => { window.__clearFiltersPageSentinel = true; });

  await clearFilters.click();

  await expect.poll(() => new URL(page.url()).searchParams.get("session_search")).toBe(null);
  await expect(clearFilters).toBeHidden();
  await expect.poll(() => page.evaluate(() => window.__clearFiltersPageSentinel)).toBe(true);
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

test("a stalled stale-session refresh recovers without reloading the current view", async ({ page }) => {
  await page.goto("/");
  await page.evaluate(() => { window.__staleRefreshSentinel = true; });

  let markFragmentRequested;
  const fragmentRequested = new Promise((resolve) => { markFragmentRequested = resolve; });
  let releaseFragment;
  const fragmentRelease = new Promise((resolve) => { releaseFragment = resolve; });
  await page.route(/\/session_fragment(?:\?|$)/, async (route) => {
    markFragmentRequested();
    await fragmentRelease;
    await route.abort().catch(() => {});
  });
  await page.route(/\/events(?:\?|$)/, (route) => route.abort("connectionfailed"));

  const now = await page.evaluate(() => Date.now());
  await page.clock.install({ time: now });
  await page.clock.setSystemTime(now + 61_000);
  await page.evaluate(() => window.dispatchEvent(new Event("pageshow")));
  await fragmentRequested;

  await expect(page.locator("body")).toHaveClass(/session-switching/);
  await page.clock.runFor(12_001);

  await expect(page.locator("body")).not.toHaveClass(/session-switching/);
  await expect.poll(() => page.evaluate(() => window.__staleRefreshSentinel)).toBe(true);
  await expect(page.getByText("Session may be stale.")).toBeVisible();
  releaseFragment();
});

test("session switching blocks shortcuts from acting underneath the overlay", async ({ page }) => {
  await page.goto("/");
  await searchSessions(page, sessions.promptRetryCompact);
  const targetLink = page.getByRole("link", { name: new RegExp(sessions.promptRetryCompact) });
  await expect(targetLink).toBeVisible();

  let markFragmentRequested;
  const fragmentRequested = new Promise((resolve) => { markFragmentRequested = resolve; });
  let releaseFragment;
  const fragmentRelease = new Promise((resolve) => { releaseFragment = resolve; });
  await page.route(/\/session_fragment(?:\?|$)/, async (route) => {
    markFragmentRequested();
    await fragmentRelease;
    await route.continue();
  });

  await targetLink.click();
  await fragmentRequested;
  await expect(page.locator("body")).toHaveClass(/session-switching/);

  const escapeBlocked = await page.evaluate(() => {
    const event = new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true });
    return !document.dispatchEvent(event);
  });
  expect.soft(escapeBlocked).toBe(true);

  await page.evaluate(() => window.dispatchEvent(new Event("gripi:new-session-requested")));
  await expect.soft(page.locator('[data-modal="new-session-modal"]')).toBeHidden();

  releaseFragment();
  await expect(page.getByRole("heading", { level: 1, name: sessions.promptRetryCompact })).toBeVisible();
  await expect(page.locator("body")).not.toHaveClass(/session-switching/);

  await page.evaluate(() => window.dispatchEvent(new Event("gripi:new-session-requested")));
  await expect(page.locator('[data-modal="new-session-modal"]')).toBeVisible();
});

test("wake recovery does not supersede a pending user session switch", async ({ page }) => {
  await page.goto("/");
  await searchSessions(page, sessions.promptRetryCompact);
  const targetLink = page.getByRole("link", { name: new RegExp(sessions.promptRetryCompact) });
  await expect(targetLink).toBeVisible();

  let markFragmentRequested;
  const fragmentRequested = new Promise((resolve) => { markFragmentRequested = resolve; });
  let releaseFragments;
  const fragmentsRelease = new Promise((resolve) => { releaseFragments = resolve; });
  let fragmentRequests = 0;
  await page.route(/\/session_fragment(?:\?|$)/, async (route) => {
    fragmentRequests += 1;
    markFragmentRequested();
    await fragmentsRelease;
    await route.continue().catch(() => {});
  });
  let resuming = false;
  let resumeEventRequests = 0;
  await page.route(/\/events(?:\?|$)/, async (route) => {
    if (!resuming) return route.continue();

    resumeEventRequests += 1;
    await route.fulfill({ json: { events: [], last_seq: 0, missed: true } });
  });

  await targetLink.click();
  await fragmentRequested;
  const now = await page.evaluate(() => Date.now());
  await page.clock.install({ time: now });
  await page.clock.setSystemTime(now + 61_000);
  resuming = true;
  await page.evaluate(() => window.dispatchEvent(new Event("pageshow")));
  await page.clock.runFor(100);
  await new Promise((resolve) => setImmediate(resolve));

  expect(resumeEventRequests).toBe(0);
  expect(fragmentRequests).toBe(1);
  releaseFragments();
  await expect(page.getByRole("heading", { level: 1, name: sessions.promptRetryCompact })).toBeVisible();
  await expect(page.locator("body")).not.toHaveClass(/session-switching/);
});

test("in-flight event recovery does not supersede a pending user session switch", async ({ page }) => {
  await page.goto("/");
  await searchSessions(page, sessions.promptRetryCompact);
  const targetLink = page.getByRole("link", { name: new RegExp(sessions.promptRetryCompact) });
  await expect(targetLink).toBeVisible();

  let releaseEvent;
  const eventRelease = new Promise((resolve) => { releaseEvent = resolve; });
  let markEventRequested;
  const eventRequested = new Promise((resolve) => { markEventRequested = resolve; });
  await page.route(/\/events(?:\?|$)/, async (route) => {
    markEventRequested();
    await eventRelease;
    await route.fulfill({ json: { events: [], last_seq: 0, missed: true } }).catch(() => {});
  });
  await page.evaluate(() => window.dispatchEvent(new Event("pageshow")));
  await eventRequested;

  let releaseFragments;
  const fragmentsRelease = new Promise((resolve) => { releaseFragments = resolve; });
  await page.route(/\/session_fragment(?:\?|$)/, async (route) => {
    await fragmentsRelease;
    await route.continue().catch(() => {});
  });

  await targetLink.click();
  await expect(page.locator("body")).toHaveClass(/session-switching/);
  releaseEvent();
  await page.evaluate(() => Promise.resolve());
  releaseFragments();

  await expect(page.getByRole("heading", { level: 1, name: sessions.promptRetryCompact })).toBeVisible();
  await expect(page.locator("body")).not.toHaveClass(/session-switching/);
});

test("a newer session switch wins when fragment responses arrive out of order", async ({ page }) => {
  await page.goto("/");
  await searchSessions(page, sessions.marker);
  const olderLink = page.getByRole("link", { name: new RegExp(sessions.marker) });
  await expect(olderLink).toBeVisible();
  const olderHref = await olderLink.getAttribute("href");

  await changeSessionSearch(page, sessions.promptRetryCompact);
  const newerLink = page.getByRole("link", { name: new RegExp(sessions.promptRetryCompact) });
  await expect(newerLink).toBeVisible();
  const newerHref = await newerLink.getAttribute("href");
  const olderSession = new URL(olderHref, page.url()).searchParams.get("session");
  const newerSession = new URL(newerHref, page.url()).searchParams.get("session");

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

  await page.evaluate((href) => {
    history.pushState({}, "", href);
    window.dispatchEvent(new PopStateEvent("popstate"));
  }, olderHref);
  await olderFragmentRequested;
  await page.evaluate((href) => {
    history.pushState({}, "", href);
    window.dispatchEvent(new PopStateEvent("popstate"));
  }, newerHref);

  await expect(page.getByRole("heading", { level: 1, name: sessions.promptRetryCompact })).toBeVisible();
  await expect.poll(() => fragmentCompletionOrder).toEqual([newerSession, olderSession]);
  await expect(page.getByRole("heading", { level: 1, name: sessions.promptRetryCompact })).toBeVisible();
  await expect.poll(() => new URL(page.url()).searchParams.get("session")).toBe(newerSession);
  await expect.poll(() => eventSessions.at(-1)).toBe(newerSession);
  await expect(page.locator("#live-output")).toHaveAttribute("data-events-url", new RegExp(encodeURIComponent(newerSession)));
});

async function searchSessions(page, query) {
  await page.getByRole("button", { name: "Search sessions" }).click();
  await changeSessionSearch(page, query);
}

async function changeSessionSearch(page, query) {
  const search = page.getByRole("searchbox", { name: "Search sessions" });
  await search.fill(query);
  await Promise.all([
    page.waitForURL((url) => url.searchParams.get("session_search") === query, { waitUntil: "domcontentloaded" }),
    search.press("Enter")
  ]);
}
