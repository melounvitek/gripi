import { expect, test } from "@playwright/test";
import { randomUUID } from "node:crypto";
import { appendFile, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import path from "node:path";
import { sessions } from "../support/contract.mjs";
import { message } from "../support/ui.mjs";

test.use({ hasTouch: true });

test("a new CLI project stays out of project dropdowns until explicit takeover", async ({ page }) => {
  test.setTimeout(60_000);
  await page.goto("/?show_all_sessions=1");
  const seedLink = page.getByRole("link", { name: new RegExp(sessions.marker) });
  const seedURL = new URL(await seedLink.getAttribute("href"), page.url());
  const seedPath = seedURL.searchParams.get("session");
  const entries = (await readFile(seedPath, "utf8")).trim().split("\n").map(JSON.parse);
  const seedCwd = entries[0].cwd;
  await expectProjectOptions(page, seedCwd, 1);

  // Create only after startup: existing projects are intentionally grandfathered in.
  const cwd = await mkdtemp(path.join(path.dirname(seedCwd), "external-project-"));
  const id = randomUUID();
  const file = path.join(path.dirname(seedPath), `external-project-${id}.jsonl`);
  const title = `E2E External Project ${id.slice(0, 8)}`;
  entries[0].cwd = cwd;
  entries[0].id = id;
  entries.find((entry) => entry.type === "session_info").name = title;
  try {
    await writeFile(file, `${entries.map((entry) => JSON.stringify(entry)).join("\n")}\n`);
    await page.goto(seedURL.href);
    const cliSession = page.locator(`a.session[data-session-path="${file}"]`);
    await expect(page.locator(".sidebar-project-filter")).toHaveValue("");
    await expect(cliSession).toBeVisible();
    await expectProjectOptions(page, cwd, 0);

    await cliSession.tap();
    await expect(page.getByRole("heading", { level: 1, name: title })).toBeVisible();
    await expect(page.locator("#live-output")).toHaveAttribute("data-session-sync-mode", "available");
    await expectProjectOptions(page, cwd, 0);
    await page.reload();
    await expect(page.locator("#live-output")).toHaveAttribute("data-session-sync-mode", "available");
    await expectProjectOptions(page, cwd, 0);

    await appendCLIReply(file, "Reply from the new CLI project");
    await expect(message(page, "assistant", "Reply from the new CLI project")).toBeVisible();
    await expect(page.locator("#live-output")).toHaveAttribute("data-session-sync-mode", "external_follow");
    await expectProjectOptions(page, cwd, 0);

    const documentMarker = randomUUID();
    await page.evaluate((marker) => { window.externalProjectDocumentMarker = marker; }, documentMarker);
    await page.getByRole("button", { name: "Take over in gateway", exact: true }).tap();
    await expect(page.locator("#live-output")).toHaveAttribute("data-session-sync-mode", "managed");
    await expect(page.getByLabel("Message to Pi")).toBeEnabled();
    await expectProjectOptions(page, cwd, 1);
    await expectProjectOptions(page, seedCwd, 1);
    await expect(page.locator(".sidebar-project-filter")).toHaveValue("");
    await expect(cliSession).toBeVisible();
    await expect(seedLink).toBeVisible();
    expect(await page.evaluate(() => window.externalProjectDocumentMarker)).toBe(documentMarker);

    await page.reload();
    await expect(page.getByRole("heading", { level: 1, name: title })).toBeVisible();
    await expectProjectOptions(page, cwd, 1);
    await expectProjectOptions(page, seedCwd, 1);
    await expect(page.locator(".sidebar-project-filter")).toHaveValue("");
    await expect(cliSession).toBeVisible();
    await expect(seedLink).toBeVisible();
  } finally {
    await rm(file, { force: true });
    await rm(cwd, { recursive: true, force: true });
  }
});

async function expectProjectOptions(page, cwd, count) {
  // The custom dropdowns hide these native selects; hidden options must still be checked.
  await expect(page.locator(`.sidebar-project-filter option[value="${cwd}"]`)).toHaveCount(count);
  await expect(page.locator(`#new-session-known-cwd option[value="${cwd}"]`)).toHaveCount(count);
}

async function appendCLIReply(file, text) {
  const entries = (await readFile(file, "utf8")).trim().split("\n").map(JSON.parse);
  const previous = entries.at(-1);
  const timestamp = Math.max(Date.now(), Date.parse(previous.timestamp) + 1000);
  const assistant = entries.find((entry) => entry.message?.role === "assistant").message;
  const user = {
    type: "message", id: randomUUID().slice(0, 8), parentId: previous.id,
    timestamp: new Date(timestamp).toISOString(),
    message: { role: "user", content: [{ type: "text", text: `CLI request: ${text}` }], timestamp },
  };
  const reply = {
    type: "message", id: randomUUID().slice(0, 8), parentId: user.id,
    timestamp: new Date(timestamp + 1).toISOString(),
    message: { ...assistant, content: [{ type: "text", text }], timestamp: timestamp + 1 },
  };
  await appendFile(file, `${JSON.stringify(user)}\n${JSON.stringify(reply)}\n`);
}
