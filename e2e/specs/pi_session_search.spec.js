import { readFile } from "node:fs/promises";
import { expect, test } from "@playwright/test";
import { piSearch, sessions } from "../support/contract.mjs";

// The fake Pi answers with the search's words, so a search for a session's ID finds that session, whose name, folder
// and first message don't hold it.
const historyID = "e2e-history";

async function search(page, query) {
  await page.getByRole("button", { name: "Search sessions" }).click();
  const field = page.getByRole("searchbox", { name: "Search sessions" });
  await field.fill(query);
  await expect.poll(() => new URL(page.url()).searchParams.get("session_search")).toBe(query);
  return field;
}

// The fake Pis that searched for sessions, by PID, with whether each of them stopped.
async function piSearchRuns() {
  const runs = new Map();
  const log = await readFile(process.env.GRIPI_E2E_FAKE_PI_LOG, "utf8").catch(() => "");
  for (const record of log.trim().split("\n").filter(Boolean).map((line) => JSON.parse(line))) {
    if (record.event === "started" && record.args?.includes("search_sessions,show_session")) runs.set(record.pid, false);
    else if (record.event === "stopped" && runs.has(record.pid)) runs.set(record.pid, true);
  }
  return runs;
}

test("Enter asks Pi when the search matches no session, and its finds stay after a reload and Back", async ({ page }) => {
  await page.goto("/");
  const field = await search(page, historyID);
  await expect(page.getByText("No sessions match. Enter asks Pi.")).toBeVisible();

  await field.press("Enter");

  const link = page.getByRole("link", { name: new RegExp(sessions.history) });
  await expect(link).toBeVisible();
  await expect(page.locator(".sessions-list .session-row")).toHaveCount(1);
  await expect.poll(() => new URL(page.url()).searchParams.get("session_found")).toBe(historyID);
  const found = page.getByRole("button", { name: "found by Pi" });
  await expect(found).toBeVisible();

  await page.reload();
  await expect(link).toBeVisible();
  await link.click();
  await expect(page.getByRole("heading", { level: 1, name: sessions.history })).toBeVisible();
  expect(new URL(page.url()).searchParams.get("session_found")).toBe(historyID);
  await page.goBack();
  await expect(link).toBeVisible();
  await expect(found).toBeVisible();

  await found.click();
  await expect(page.getByText("No sessions match. Enter asks Pi.")).toBeVisible();
  await expect.poll(() => new URL(page.url()).searchParams.get("session_found")).toBe(null);
  await expect(page.getByRole("button", { name: "ask Pi" })).toBeVisible();
});

test("Pi's search shows while it runs, and stopping it or changing the search stops its Pi", async ({ page }) => {
  await page.goto("/");
  const field = await search(page, piSearch.slow);
  const status = page.getByRole("status").filter({ hasText: "Pi is searching conversations…" });
  const runs = (await piSearchRuns()).size;

  await page.getByRole("button", { name: "ask Pi" }).click();
  await expect(status).toBeVisible();
  await expect(page.getByText("No sessions match. Enter asks Pi.")).toBeHidden();
  await page.getByRole("button", { name: "Pi · stop" }).click();

  await expect(status).toBeHidden();
  await expect(page.getByRole("button", { name: "ask Pi" })).toBeVisible();
  await expect.poll(async () => [...(await piSearchRuns()).values()].slice(runs)).toEqual([true]);

  await page.getByRole("button", { name: "ask Pi" }).click();
  await expect(status).toBeVisible();
  await field.press("End");
  await field.pressSequentially(" more");

  await expect(status).toBeHidden();
  await expect(page.getByRole("button", { name: "Pi · stop" })).toBeHidden();
  await expect.poll(async () => [...(await piSearchRuns()).values()].slice(runs)).toEqual([true, true]);
});

test("the sidebar says when Pi found nothing more, and why it couldn't search", async ({ page }) => {
  await page.goto("/");
  const field = await search(page, "e2e-no-such-session");
  await page.getByRole("button", { name: "ask Pi" }).click();
  await expect(page.getByRole("status").filter({ hasText: "Pi found no other sessions." })).toBeVisible();

  await field.fill(piSearch.fail);
  await expect.poll(() => new URL(page.url()).searchParams.get("session_search")).toBe(piSearch.fail);
  await expect(page.getByText("Pi found no other sessions.")).toBeHidden();
  await field.press("Enter");
  await expect(page.getByRole("status").filter({ hasText: "Pi couldn’t search: No API key found for e2e." })).toBeVisible();
});
