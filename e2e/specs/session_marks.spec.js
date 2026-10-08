import { expect, test } from "@playwright/test";
import { setSessionsPinned } from "../support/ui.mjs";

const markedPaths = (page) => page.locator(".session-sidebar .session-row.is-marked").evaluateAll((rows) => rows.map((row) => row.dataset.sessionPath));
const markRow = (row, modifier = "ControlOrMeta") => row.locator("a.session").click({ modifiers: [modifier] });

test("mark sessions with Ctrl+click and a range with Shift+click, without opening them", async ({ page, context }) => {
  await page.goto("/");
  const url = page.url();
  const otherRows = page.locator('.session-sidebar .session-row[data-current="false"]');
  const pinnedPath = await otherRows.first().getAttribute("data-session-path");
  await setSessionsPinned(page, [pinnedPath], true);
  const pinnedRow = page.locator(".pinned-sessions-list .session-row").first();
  const listRow = page.locator('.sessions-list .session-row[data-current="false"]').nth(2);

  await markRow(listRow);
  await expect(listRow).toHaveClass(/\bis-marked\b/);
  await markRow(listRow);
  await expect(listRow).not.toHaveClass(/\bis-marked\b/);

  // A range runs in sidebar order, from the pinned section into the session list.
  await markRow(pinnedRow);
  await markRow(listRow, "Shift");
  const rows = await page.locator(".session-sidebar .session-row").evaluateAll((rows) => rows.map((row) => row.dataset.sessionPath));
  const listPath = await listRow.getAttribute("data-session-path");
  const range = rows.slice(rows.indexOf(pinnedPath), rows.indexOf(listPath) + 1);
  expect(range.length).toBeGreaterThan(3);
  await expect.poll(() => markedPaths(page)).toEqual(range);
  expect(context.pages()).toHaveLength(1);
  await expect(page).toHaveURL(url);

  // Marks outlive the sidebar redrawing itself.
  const unmarkedRow = page.locator('.sessions-list .session-row:not(.is-marked)').last();
  const unmarkedPath = await unmarkedRow.getAttribute("data-session-path");
  await setSessionsPinned(page, [unmarkedPath], true);
  await expect.poll(() => markedPaths(page)).toEqual(range);

  // Escape while typing leaves the marks alone; Escape on a session clears them.
  await page.getByLabel("Message to Pi").focus();
  await page.keyboard.press("Escape");
  await expect.poll(() => markedPaths(page)).toEqual(range);
  await listRow.locator("a.session").focus();
  await page.keyboard.press("Escape");
  await expect.poll(() => markedPaths(page)).toEqual([]);

  for (const session of [pinnedPath, unmarkedPath]) {
    expect((await page.request.post("/sessions/pin", { form: { session, pinned: "false" } })).ok()).toBe(true);
  }
});

test("a plain click opens a session and clears the marks", async ({ page }) => {
  await page.goto("/");
  const rows = page.locator('.sessions-list .session-row[data-current="false"]');
  await markRow(rows.nth(0));
  await markRow(rows.nth(1));
  await expect.poll(() => markedPaths(page)).toHaveLength(2);

  const name = await rows.nth(2).getAttribute("data-session-name");
  await rows.nth(2).locator("a.session").click();
  await expect(page.getByRole("heading", { level: 1, name })).toBeVisible();
  await expect.poll(() => markedPaths(page)).toEqual([]);
});

test("pin and unpin all marked sessions from the menu of one of them", async ({ page }) => {
  await page.goto("/");
  const rows = page.locator('.sessions-list .session-row[data-current="false"]');
  const paths = [await rows.nth(0).getAttribute("data-session-path"), await rows.nth(1).getAttribute("data-session-path")];
  const row = (path) => page.locator(`.session-sidebar .session-row[data-session-path="${path}"]`);
  await markRow(row(paths[0]));
  await markRow(row(paths[1]));

  await row(paths[1]).locator("a.session").click({ button: "right" });
  const menu = page.getByRole("menu");
  await expect(menu).toContainText("2 sessions");
  await expect(menu.getByRole("menuitem")).toHaveText(["Tags…", "Pin", "Open in new windows"]);
  await menu.getByRole("menuitem", { name: "Pin" }).click();
  for (const path of paths) await expect(row(path)).toHaveAttribute("data-pinned", "true");
  await expect.poll(() => markedPaths(page)).toEqual([]);

  await markRow(row(paths[0]));
  await markRow(row(paths[1]));
  await row(paths[0]).locator("a.session").click({ button: "right" });
  await menu.getByRole("menuitem", { name: "Unpin" }).click();
  for (const path of paths) await expect(row(path)).toHaveAttribute("data-pinned", "false");

  // With one marked session, its menu is the usual one.
  await markRow(row(paths[0]));
  await row(paths[0]).locator("a.session").click({ button: "right" });
  await expect(menu.getByRole("menuitem")).toHaveText(["Rename…", "Tags…", "Pin", "Open in new window", "Delete session…"]);
  await expect(menu.getByText("1 sessions")).toBeHidden();
});

test("open marked sessions in new windows, moving this window off the current one", async ({ page, context }) => {
  await page.goto("/");
  const currentRow = page.locator('.session-sidebar .session-row[data-current="true"]');
  const otherRow = page.locator('.sessions-list .session-row[data-current="false"]').first();
  const paths = [await currentRow.getAttribute("data-session-path"), await otherRow.getAttribute("data-session-path")];
  await markRow(currentRow);
  await markRow(otherRow);

  await otherRow.locator("a.session").click({ button: "right" });
  await page.getByRole("menuitem", { name: "Open in new windows" }).click();
  await expect.poll(() => context.pages().length).toBe(3);
  const sessionWindows = context.pages().slice(1);
  for (const sessionWindow of sessionWindows) await expect(sessionWindow).toHaveURL(/session_only=1/);
  expect(sessionWindows.map((sessionWindow) => new URL(sessionWindow.url()).searchParams.get("session")).sort()).toEqual([...paths].sort());
  await expect(currentRow).toHaveCount(1);
  expect(paths).not.toContain(await currentRow.getAttribute("data-session-path"));
  await expect.poll(() => markedPaths(page)).toEqual([]);
});

test("right-clicking an unmarked session opens its own menu and keeps the marks", async ({ page }) => {
  await page.goto("/");
  const rows = page.locator('.sessions-list .session-row[data-current="false"]');
  await markRow(rows.nth(0));
  await markRow(rows.nth(1));
  const marked = await markedPaths(page);
  expect(marked).toHaveLength(2);

  await rows.nth(2).locator("a.session").click({ button: "right" });
  await expect(page.getByRole("menuitem", { name: "Rename…" })).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(page.getByRole("menu")).toBeHidden();
  await expect.poll(() => markedPaths(page)).toEqual(marked);
});
