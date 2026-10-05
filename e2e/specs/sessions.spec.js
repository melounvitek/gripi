import { expect, test } from "@playwright/test";
import { prompts, sessions } from "../support/contract.mjs";
import { expectRunFinished, message, selectSession, sendPrompt, setSessionsPinned, startSession } from "../support/ui.mjs";

test("show desktop session activity in the time slot, centred and on one line", async ({ page }) => {
  await page.goto("/");
  const row = page.locator('.session-row[data-current="true"]');
  const title = row.locator(".session-title");
  const meta = row.locator(".session-meta");
  await meta.evaluate((element) => {
    const dot = document.createElement("span");
    dot.className = "session-running-indicator";
    element.replaceChildren(dot);
  });

  for (const text of ["Short title", "Fix sidebar session deletion and native rename behavior"]) {
    await title.evaluate((element, text) => { element.textContent = text; }, text);
    const titleBounds = await title.boundingBox();
    const lineHeight = await title.evaluate((element) => Number.parseFloat(getComputedStyle(element).lineHeight));
    expect(Math.round(titleBounds.height / lineHeight)).toBe(1);
    const linkBounds = await row.locator("a.session").boundingBox();
    const dotBounds = await meta.locator(".session-running-indicator").boundingBox();
    expect(Math.abs(dotBounds.y + dotBounds.height / 2 - linkBounds.y - linkBounds.height / 2)).toBeLessThan(1);
    // The dot takes the time's place, right-aligned with the other rows' times.
    const timeRight = await page.locator('.session-row[data-current="false"] .session-meta').first().evaluate((element) => element.getBoundingClientRect().right);
    expect(Math.abs(dotBounds.x + dotBounds.width - timeRight)).toBeLessThan(1);
    expect(titleBounds.x + titleBounds.width).toBeLessThanOrEqual(dotBounds.x);
  }
});

for (const busy of [false, true]) {
  test(`Ctrl shortcut badges leave session layout unchanged (${busy ? "busy" : "idle"})`, async ({ page }, testInfo) => {
    await page.goto("/");
    // Keep sidebar polling from replacing the synthetic title and activity state.
    await page.clock.install();
    await page.clock.pauseAt(new Date(Date.now() + 1000));
    const row = page.locator('.session-row[data-current="true"]');
    await row.locator(".session-title").evaluate((element) => {
      element.textContent = "Fix sidebar session deletion and native rename behavior";
    });
    await page.locator('.session-row[data-current="false"] .session-title').first().evaluate((element) => {
      element.textContent = "Short";
    });
    if (busy) {
      await row.locator(".session-meta").evaluate((element) => {
        const dot = document.createElement("span");
        dot.className = "session-running-indicator";
        element.replaceChildren(dot);
      });
    }

    const layout = () => page.locator(".session-row, .session-title, .session-running-indicator").evaluateAll((elements) =>
      elements.map((element) => element.getBoundingClientRect().toJSON()));
    const before = await layout();
    const badge = row.locator(".session-shortcut");
    await expect(badge).toBeHidden();
    const timeRight = await row.locator(".session-meta").evaluate((element) => element.getBoundingClientRect().right);
    await page.keyboard.down("Control");
    await expect(badge).toBeVisible();
    expect(await layout()).toEqual(before);
    // Numbers replace the time, so short and long titles share one column.
    await expect(row.locator(".session-meta")).toBeHidden();
    const badgeRights = await page.locator(".session-shortcut").evaluateAll((elements) =>
      elements.filter((element) => element.getClientRects().length).map((element) => Math.round(element.getBoundingClientRect().right)));
    expect(badgeRights.length).toBeGreaterThan(1);
    expect(new Set(badgeRights)).toEqual(new Set([Math.round(timeRight)]));
    await page.screenshot({ path: testInfo.outputPath("shortcut-overlay.png") });
    await page.keyboard.up("Control");
    await expect(badge).toBeHidden();
    expect(await layout()).toEqual(before);
  });
}

test("Ctrl shortcut badges replace the actions button on narrow touch-style rows", async ({ page }) => {
  await page.setViewportSize({ width: 700, height: 900 });
  await page.goto("/");
  await page.locator('label[aria-label="Open sessions"]').click();
  await expect(page.locator(".session-sidebar")).toHaveCSS("transform", "matrix(1, 0, 0, 1, 0, 0)");
  const row = page.locator('.session-row[data-current="true"]');
  const actions = row.locator("[data-session-actions-toggle]");
  const actionsBounds = await actions.boundingBox();
  await page.keyboard.down("Control");
  const badge = row.locator(".session-shortcut");
  await expect(badge).toBeVisible();
  await expect(row.locator(".session-meta")).toBeVisible();
  const badgeBounds = await badge.boundingBox();
  expect(Math.abs(badgeBounds.x + badgeBounds.width / 2 - actionsBounds.x - actionsBounds.width / 2)).toBeLessThan(1);
  await page.keyboard.up("Control");
  await expect(badge).toBeHidden();
});

test("hide the desktop sidebar and remember the preference", async ({ page }) => {
  await page.goto("/");

  const sidebar = page.getByRole("complementary", { name: "Sessions" });
  const sidebarToggle = sidebar.locator("[data-sidebar-visibility-toggle]");
  const headerToggle = page.locator(".session-header [data-sidebar-visibility-toggle]");
  await expect(sidebar).toBeVisible();
  await expect(sidebarToggle).toBeVisible();
  await expect(sidebarToggle).toHaveAttribute("aria-label", "Hide sessions");
  await expect(sidebarToggle).toHaveAttribute("aria-expanded", "true");
  await expect(headerToggle).toBeHidden();

  await sidebarToggle.click();

  await expect(sidebar).toBeHidden();
  await expect(headerToggle).toBeVisible();
  await expect(headerToggle).toHaveAttribute("aria-label", "Show sessions");
  await expect(headerToggle).toHaveAttribute("aria-expanded", "false");
  await expect(headerToggle).toBeFocused();
  await expect.poll(() => page.evaluate(() => localStorage.getItem("gripi:desktop-sidebar-hidden"))).toBe("true");

  await page.setViewportSize({ width: 760, height: 900 });
  const mobileToggle = page.locator('.session-header label[aria-label="Open sessions"]');
  await expect(headerToggle).toBeHidden();
  await expect(mobileToggle).toBeVisible();
  await expect(page.locator("#mobile-session-toggle")).not.toBeChecked();
  await mobileToggle.click();
  await expect(page.locator("#mobile-session-toggle")).toBeChecked();
  await expect(sidebar).toBeVisible();
  await expect.poll(() => page.evaluate(() => localStorage.getItem("gripi:desktop-sidebar-hidden"))).toBe("true");

  await page.setViewportSize({ width: 761, height: 900 });
  await expect(sidebar).toBeHidden();
  await expect(headerToggle).toBeVisible();

  await page.reload();

  await expect(sidebar).toBeHidden();
  await expect(headerToggle).toHaveAttribute("aria-label", "Show sessions");

  await page.keyboard.press("Control+Shift+f");

  await expect(sidebar).toBeVisible();
  await expect(sidebarToggle).toBeVisible();
  await expect(headerToggle).toBeHidden();
  await expect(page.getByRole("searchbox", { name: "Search sessions" })).toBeVisible();
  await expect.poll(() => page.evaluate(() => localStorage.getItem("gripi:desktop-sidebar-hidden"))).toBe("true");

  await page.reload();
  await expect(sidebar).toBeHidden();
  await headerToggle.click();

  await expect(sidebar).toBeVisible();
  await expect(sidebarToggle).toBeVisible();
  await expect(sidebarToggle).toHaveAttribute("aria-label", "Hide sessions");
  await expect(sidebarToggle).toHaveAttribute("aria-expanded", "true");
  await expect(sidebarToggle).toBeFocused();
  await expect(headerToggle).toBeHidden();
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

test("session initialization preserves focus when the user starts composing", async ({ page }) => {
  await page.goto("/");
  await searchSessions(page, "Persisted browser");
  await page.clock.install();
  await page.clock.pauseAt(new Date(Date.now() + 1000));
  await page.getByRole("link", { name: new RegExp(sessions.history) }).click();
  await expect(page.getByRole("heading", { level: 1, name: sessions.history })).toBeVisible();

  const composer = page.getByLabel("Message to Pi");
  await composer.focus();
  await page.clock.runFor(100);
  await expect(composer).toBeFocused();
  await page.clock.resume();

  const prompt = "Keep this prompt in the composer";
  await page.keyboard.insertText(prompt);
  await expect(composer).toHaveValue(prompt);
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

test("counts matches and keeps the filters in place when the current session is filtered out", async ({ page }) => {
  await page.goto("/");
  await selectSession(page, sessions.marker);
  // Measured within the sidebar's scrolled content, because selecting a session can scroll it.
  const filters = page.locator(".sidebar-filter-row");
  const top = await filters.evaluate((row) => row.offsetTop);

  await searchSessions(page, "History Desktop");

  await expect(page.locator(".current-session-section")).toBeVisible();
  await expect(page.locator("[data-sidebar-filter-count]")).toHaveText(/^1 of \d+$/);
  expect(await filters.evaluate((row) => row.offsetTop)).toBe(top);
});

test("filters sessions while typing without adding browser history", async ({ page }) => {
  await page.goto("/");
  const historyLength = await page.evaluate(() => history.length);
  await page.getByRole("button", { name: "Search sessions" }).click();
  const search = page.getByRole("searchbox", { name: "Search sessions" });

  await search.pressSequentially("History Desktop");

  await expect(page.locator(".sessions-list .session-row")).toHaveCount(1);
  await expect(page.getByRole("link", { name: new RegExp(sessions.history) })).toBeVisible();
  await expect(search).toBeFocused();
  await expect.poll(() => new URL(page.url()).searchParams.get("session_search")).toBe("History Desktop");
  expect(await page.evaluate(() => history.length)).toBe(historyLength);
});

test("keeps refreshing the sidebar while the search field is focused", async ({ page }) => {
  await page.clock.install();
  await page.goto("/");
  await searchSessions(page, "History Desktop");
  const row = page.locator(".sessions-list .session-row");
  const form = { session: await row.getAttribute("data-session-path"), tag: "refresh-while-searching" };
  await page.request.post("/sessions/tags", { form: { ...form, assigned: "true" } });
  try {
    await page.clock.runFor(10_100);

    await expect(row.getByRole("button", { name: `Filter sessions by ${form.tag}`, exact: true })).toBeVisible();
    await expect(page.getByRole("searchbox", { name: "Search sessions" })).toBeFocused();
  } finally {
    await page.request.post("/sessions/tags", { form: { ...form, assigned: "false" } });
  }
});

test("choosing a project leaves the search field closed", async ({ page }) => {
  await page.goto("/");

  await page.getByRole("combobox", { name: "Filter sessions by project" }).click();
  await page.getByRole("option").nth(1).click();

  await expect(page.locator("[data-sidebar-filter-count]")).toBeVisible();
  await expect(page.getByRole("searchbox", { name: "Search sessions" })).toBeHidden();
});

test("projects are marked by letters that no two projects share", async ({ page }) => {
  await page.goto("/?show_all_sessions=1");
  const row = page.locator(".session-row").filter({ has: page.locator(".session-title", { hasText: sessions.marker }) });
  const trigger = page.getByRole("combobox", { name: "Filter sessions by project" });
  const option = (name) => page.getByRole("option", { name, exact: true });
  const letters = await row.locator(".project-monogram").textContent();

  await trigger.click();
  const marks = (await page.getByRole("option").locator(".project-monogram").allTextContents()).filter(Boolean);
  expect(new Set(marks).size).toBe(marks.length);
  await expect(option("contract-project").locator(".project-monogram")).toHaveText(letters);
  // Both have the initials "cp", so whichever came second uses its first two letters instead.
  expect([letters, await option("controls-project").locator(".project-monogram").textContent()].sort()).toEqual(["co", "cp"]);

  await option("contract-project").click();
  await expect(trigger.locator(".project-monogram")).toHaveText(letters);
  await expect(page.locator(".sessions-list .session-row .project-monogram").first()).toHaveText(letters);
});

test("pin and unpin with the mouse without leaving a focus outline", async ({ page }) => {
  await page.goto("/?show_all_sessions=1");
  const row = page.locator(".session-row").filter({ has: page.locator(".session-title", { hasText: sessions.marker }) });
  const url = page.url();
  // Start in keyboard mode to cover switching back to pointer activation.
  await page.keyboard.press("Tab");
  await row.getByRole("button", { name: `Pin session ${sessions.marker}`, exact: true }).click();
  const unpin = row.getByRole("button", { name: `Unpin session ${sessions.marker}`, exact: true });
  await expect(unpin).toBeEnabled();
  await expect(unpin).not.toBeFocused();
  await expect(unpin).toHaveCSS("outline-style", "none");

  await unpin.click();
  const pin = row.getByRole("button", { name: `Pin session ${sessions.marker}`, exact: true });
  await expect(pin).toBeEnabled();
  await expect(pin).not.toBeFocused();
  await expect(pin).toHaveCSS("outline-style", "none");
  await expect(page).toHaveURL(url);
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
  await row.getByRole("button", { name: `Pin session ${sessions.history}` }).focus();
  await page.keyboard.press("Enter");
  await expect(row).toHaveAttribute("data-pinned", "true");
  await expect(row.getByRole("button", { name: `Unpin session ${sessions.history}` })).toHaveAttribute("aria-pressed", "true");
  await expect(row.getByRole("button", { name: `Unpin session ${sessions.history}` })).toBeFocused();
  await expect(row.getByRole("button", { name: `Unpin session ${sessions.history}` })).toHaveCSS("outline-style", "solid");
  await expect(page.getByRole("heading", { level: 2, name: "Pinned" })).toBeVisible();
  await row.getByRole("button", { name: new RegExp(`Session actions for ${sessions.history}`) }).click();
  await expect(page.getByRole("menuitem", { name: "Unpin", exact: true })).toBeVisible();
  await page.keyboard.press("Escape");
  await row.getByRole("button", { name: `Unpin session ${sessions.history}` }).focus();
  await page.keyboard.press("Space");
  const pin = row.getByRole("button", { name: `Pin session ${sessions.history}`, exact: true });
  await expect(pin).toHaveAttribute("aria-pressed", "false");
  await expect(pin).toBeFocused();
  await expect(pin).toHaveCSS("outline-style", "solid");
});

test("open a background session in a new window from its contextual actions", async ({ page }) => {
  await page.goto("/");
  const row = page.locator('.session-row[data-current="false"]').first();
  const sessionPath = await row.getAttribute("data-session-path");
  const sessionName = await row.getAttribute("data-session-name");
  await row.getByRole("button", { name: /Session actions/ }).click();
  const popupPromise = page.waitForEvent("popup");
  await page.getByRole("menuitem", { name: "Open in new window" }).click();
  const sessionWindow = await popupPromise;
  await expect(sessionWindow).toHaveURL((url) => url.searchParams.get("session") === sessionPath && url.searchParams.get("session_only") === "1");
  await expect(sessionWindow.getByRole("heading", { level: 1, name: sessionName })).toBeVisible();
  await expect(page.getByRole("menu")).toBeHidden();
});

test("open a session in a new window from its actions in a narrow desktop window", async ({ page }) => {
  await page.setViewportSize({ width: 700, height: 900 });
  await page.goto("/");
  await page.locator('label[aria-label="Open sessions"]').click();
  const row = page.locator('.session-row[data-current="false"]').first();
  const sessionPath = await row.getAttribute("data-session-path");
  await row.getByRole("button", { name: /Session actions/ }).click();
  const popupPromise = page.waitForEvent("popup");
  await page.getByRole("menuitem", { name: "Open in new window" }).click();
  const sessionWindow = await popupPromise;
  await expect(sessionWindow).toHaveURL((url) => url.searchParams.get("session") === sessionPath && url.searchParams.get("session_only") === "1");
});

test("opening the current session in a new window moves the original window to another session", async ({ page }) => {
  await page.goto("/");
  const currentRow = page.locator('.session-row[data-current="true"]');
  const sessionPath = await currentRow.getAttribute("data-session-path");
  await currentRow.getByRole("button", { name: /Session actions/ }).click();
  const popupPromise = page.waitForEvent("popup");
  await page.getByRole("menuitem", { name: "Open in new window" }).click();
  const sessionWindow = await popupPromise;
  await expect(sessionWindow).toHaveURL((url) => url.searchParams.get("session") === sessionPath);
  await expect(currentRow).toHaveCount(1);
  await expect(currentRow).not.toHaveAttribute("data-session-path", sessionPath);
});

for (const desktopApp of [false, true]) {
  test(`open all pinned sessions in new windows${desktopApp ? " in the desktop app" : ""}`, async ({ page, context }) => {
    // The desktop app opens each window itself, so window.open returns nothing there even though the window opens.
    if (desktopApp) await page.addInitScript(() => {
      window.gripiElectron = {};
      const open = window.open.bind(window);
      window.open = (...args) => { open(...args); return null; };
    });
    await page.goto("/");
    const currentRow = page.locator('.session-row[data-current="true"]');
    const paths = [
      await currentRow.getAttribute("data-session-path"),
      ...await page.locator('.session-row[data-current="false"]').evaluateAll((rows) => rows.slice(0, 2).map((row) => row.dataset.sessionPath))
    ];
    const openAll = page.getByRole("button", { name: "Open all" });
    await setSessionsPinned(page, paths.slice(0, 1), true);
    // One pinned session opens from its own row, so the button waits for a second one.
    await expect(openAll).toHaveCount(0);
    await setSessionsPinned(page, paths.slice(1), true);

    await openAll.click();
    await expect.poll(() => context.pages().length).toBe(4);
    const sessionWindows = context.pages().slice(1);
    for (const sessionWindow of sessionWindows) await expect(sessionWindow).toHaveURL(/session_only=1/);
    expect(sessionWindows.map((sessionWindow) => new URL(sessionWindow.url()).searchParams.get("session")).sort()).toEqual([...paths].sort());
    // A window that could report a pop-up blocker starts with a copy of this window's sessionStorage, which holds its notification identity.
    const notificationClient = (sessionWindow) => sessionWindow.evaluate(() => sessionStorage.getItem("gripi:notification-presence-client"));
    const mainClient = await notificationClient(page);
    expect(mainClient).toBeTruthy();
    for (const sessionWindow of sessionWindows) expect(await notificationClient(sessionWindow)).not.toBe(mainClient);
    // Every pinned session now has its own window, so this one moves on to a session that is not pinned.
    await expect(currentRow).toHaveAttribute("data-pinned", "false");

    await setSessionsPinned(page, paths, false);
  });
}

test("rename and delete a background session from its contextual actions", async ({ page }) => {
  await page.goto("/");
  const previousURL = page.url();
  await startSession(page, "new-session-desktop");
  await expect(page).not.toHaveURL(previousURL);
  await expect(page.getByRole("heading", { level: 1, name: "New session (pending first assistant response)" })).toBeVisible();
  await sendPrompt(page, prompts.newSession);
  await expectRunFinished(page);

  const currentRow = page.locator('.session-row[data-current="true"]');
  const sessionPath = await currentRow.getAttribute("data-session-path");
  await currentRow.getByRole("button", { name: /Session actions/ }).click();
  const deleteAction = page.getByRole("menuitem", { name: "Delete session…" });
  await expect(deleteAction).toHaveAttribute("aria-disabled", "true");
  await expect(deleteAction).toHaveAttribute("title", "Cannot delete the current session");
  await page.keyboard.press("Escape");

  await searchSessions(page, "History Desktop");
  await page.getByRole("link", { name: new RegExp(sessions.history) }).click();
  await page.getByRole("link", { name: "Clear filters" }).click();
  const rowSelector = await page.evaluate((path) => `.session-row[data-session-path="${CSS.escape(path)}"]`, sessionPath);
  const row = page.locator(rowSelector);
  await expect(row).toBeVisible();

  await row.click({ button: "right" });
  await page.getByRole("menuitem", { name: "Rename…" }).click();
  const renameDialog = page.getByRole("dialog", { name: "Rename session" });
  const renamedName = "E2E Sidebar Session Actions";
  await renameDialog.getByRole("textbox", { name: "Name" }).fill(renamedName);
  let releaseSidebar;
  const sidebarRelease = new Promise((resolve) => { releaseSidebar = resolve; });
  await page.route(/\/sidebar(?:\?|$)/, async (route) => {
    await sidebarRelease;
    await route.continue();
  });
  try {
    await renameDialog.getByRole("button", { name: "Rename" }).click();
    await expect(renameDialog).toBeHidden();
    await expect(row.locator(".session-title")).toHaveText(renamedName);
    await expect(row.locator(".session-title")).toHaveAttribute("title", renamedName);
    await expect(row).toHaveAttribute("data-session-name", renamedName);
    await expect(row.getByRole("button", { name: `Pin session ${renamedName}`, exact: true })).toBeVisible();
    await expect(row.getByRole("button", { name: `Session actions for ${renamedName}` })).toBeFocused();
    await row.getByRole("button", { name: `Session actions for ${renamedName}` }).click();
    await page.getByRole("menuitem", { name: "Rename…" }).click();
    await expect(renameDialog.getByRole("textbox", { name: "Name" })).toHaveValue(renamedName);
    await renameDialog.getByRole("button", { name: "Cancel" }).click();
  } finally {
    releaseSidebar();
    await page.unrouteAll({ behavior: "wait" });
  }
  await page.reload();
  await expect(row.locator(".session-title")).toHaveText(renamedName);

  await row.getByRole("button", { name: `Session actions for ${renamedName}` }).click();
  await page.getByRole("menuitem", { name: "Delete session…" }).click();
  const deleteDialog = page.getByRole("dialog", { name: "Delete session" });
  await expect(deleteDialog).toContainText(renamedName);
  await deleteDialog.getByRole("button", { name: "Delete session" }).click();
  await expect(deleteDialog).toBeHidden();
  await expect(row).toHaveCount(0);
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
  await expect(page.getByText("Connection lost. Retrying…")).toBeVisible();
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
