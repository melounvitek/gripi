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

// The states of the fake Pis that searched for sessions, in the order they started: "started", "prompted" once they
// got the prompt, by when they stop on SIGTERM with a record of it, and "stopped".
async function piSearchRuns() {
  const runs = new Map();
  const log = await readFile(process.env.GRIPI_E2E_FAKE_PI_LOG, "utf8").catch(() => "");
  for (const record of log.trim().split("\n").filter(Boolean).map((line) => JSON.parse(line))) {
    if (record.event === "started" && record.args?.includes("search_sessions,show_session")) runs.set(record.pid, "started");
    else if (runs.has(record.pid) && record.event === "command" && record.command.type === "prompt") runs.set(record.pid, "prompted");
    else if (runs.has(record.pid) && record.event === "stopped") runs.set(record.pid, "stopped");
  }
  return [...runs.values()];
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

test("Pi's search shows while it runs, and stopping it, changing the search or a filter stops its Pi", async ({ page }) => {
  await page.goto("/");
  const field = await search(page, piSearch.slow);
  const status = page.getByRole("status").filter({ hasText: "Pi is searching conversations…" });
  const earlier = (await piSearchRuns()).length;
  const runs = async () => (await piSearchRuns()).slice(earlier);

  await page.getByRole("button", { name: "ask Pi" }).click();
  await expect(status).toBeVisible();
  await expect(page.getByText("No sessions match. Enter asks Pi.")).toBeHidden();
  await expect.poll(runs).toEqual(["prompted"]);
  await page.getByRole("button", { name: "Pi · stop" }).click();

  await expect(status).toBeHidden();
  await expect(page.getByRole("button", { name: "ask Pi" })).toBeVisible();
  await expect.poll(runs).toEqual(["stopped"]);

  await page.getByRole("button", { name: "ask Pi" }).click();
  await expect(status).toBeVisible();
  await expect.poll(runs).toEqual(["stopped", "prompted"]);
  await field.press("End");
  await field.pressSequentially(" more");

  await expect(status).toBeHidden();
  await expect(page.getByRole("button", { name: "Pi · stop" })).toBeHidden();
  await expect.poll(runs).toEqual(["stopped", "stopped"]);
  await expect.poll(() => new URL(page.url()).searchParams.get("session_search")).toBe(`${piSearch.slow} more`);

  await page.getByRole("button", { name: "ask Pi" }).click();
  await expect(status).toBeVisible();
  await expect.poll(runs).toEqual(["stopped", "stopped", "prompted"]);
  await page.getByRole("combobox", { name: "Filter sessions by project" }).click();
  await page.getByRole("option").nth(1).click();

  await expect(status).toBeHidden();
  await expect.poll(runs).toEqual(["stopped", "stopped", "stopped"]);
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
