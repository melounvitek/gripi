import { expect, test } from "@playwright/test";
import { setSessionsPinned } from "../support/ui.mjs";

// Playwright turns the pop-up blocker off and its headless shell has none, so these run in full Chromium with it on.
test.use({ channel: "chromium", launchOptions: { ignoreDefaultArgs: ["--disable-popup-blocking"] } });

test("open pinned sessions one click at a time while the browser blocks pop-ups", async ({ page, context }) => {
  await page.goto("/");
  await setSessionsPinned(page, await page.locator('.session-row[data-current="false"]').evaluateAll((rows) => rows.slice(0, 3).map((row) => row.dataset.sessionPath)), true);
  const pinnedRows = page.locator(".pinned-sessions-list .session-row");
  const paths = await pinnedRows.evaluateAll((rows) => rows.map((row) => row.dataset.sessionPath));
  // The second pinned session is the current one, so its window is blocked at first.
  await pinnedRows.nth(1).locator("a.session").click();
  const currentRow = page.locator('.session-row[data-current="true"]');
  await expect(currentRow).toHaveAttribute("data-session-path", paths[1]);
  const note = page.getByRole("status").filter({ hasText: "Allow pop-ups for this site" });
  const openRest = page.getByRole("button", { name: "open the rest" });

  await page.getByRole("button", { name: "Open all" }).click();
  await expect(note).toContainText("Browser blocked 2 windows.");
  await expect.poll(() => context.pages().length).toBe(2);
  await expect(currentRow).toHaveAttribute("data-session-path", paths[1]);

  await openRest.click();
  await expect.poll(() => context.pages().length).toBe(3);
  // The current session's window is open now, so this one moves on, and the note outlives the sidebar it re-renders.
  await expect(currentRow).not.toHaveAttribute("data-session-path", paths[1]);
  await expect(note).toContainText("Browser blocked 1 window.");

  await openRest.click();
  await expect(note).toBeHidden();
  await expect.poll(() => context.pages().length).toBe(4);
  const sessionWindows = context.pages().slice(1);
  for (const sessionWindow of sessionWindows) await expect(sessionWindow).toHaveURL(/session_only=1/);
  expect(sessionWindows.map((sessionWindow) => new URL(sessionWindow.url()).searchParams.get("session")).sort()).toEqual([...paths].sort());

  await setSessionsPinned(page, paths, false);
});
