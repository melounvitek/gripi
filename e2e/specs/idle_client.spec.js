import { readFile } from "node:fs/promises";
import { expect, test } from "@playwright/test";
import { prompts, replies, sessions } from "../support/contract.mjs";
import { expectRunFinished, message, selectSession, sendPrompt } from "../support/ui.mjs";

const fakePiLog = process.env.GRIPI_E2E_FAKE_PI_LOG;

test("restore a missed completion after the Pi client retires", async ({ page }) => {
  test.skip(!fakePiLog, "requires the managed fake Pi runtime");

  await page.goto("/");
  await selectSession(page, sessions.idleClient);
  const started = page.waitForResponse(async (response) => {
    if (new URL(response.url()).pathname !== "/events" || !response.ok()) return false;
    return (await response.json()).events.some((event) => event.type === "agent_start");
  });
  await sendPrompt(page, prompts.steerStart);
  await started;
  await expect(page.getByRole("button", { name: "Abort running Pi" })).toBeVisible();

  let releaseEvents;
  const eventsPaused = new Promise((resolve) => { releaseEvents = resolve; });
  await page.route(/\/events(?:\?|$)/, async (route) => {
    await eventsPaused;
    await route.continue().catch(() => {});
  });
  try {
    await sendPrompt(page, prompts.steerMessage);
    const pid = startedPids(await fakePiRecords()).at(-1);
    expect(pid).toBeTruthy();
    await expect.poll(async () => stoppedPids(await fakePiRecords())).toContain(pid);
    await expect(page.getByRole("button", { name: "Abort running Pi" })).toBeVisible();
    releaseEvents();

    await expect(message(page, "assistant", replies.steer)).toBeVisible();
    await expect(page.getByRole("button", { name: "Abort running Pi" })).toBeHidden();
    await expect(page.getByLabel("Message to Pi")).toBeEnabled();
  } finally {
    releaseEvents();
    await page.unrouteAll({ behavior: "wait" });
  }
});

test("retire an idle Pi client despite browser polling and restart it on demand", async ({ page }) => {
  test.skip(!fakePiLog, "requires the managed fake Pi runtime");

  await page.goto("/");
  await selectSession(page, sessions.idleClient);
  const previousPids = startedPids(await fakePiRecords());

  await sendPrompt(page, prompts.standard);
  await expect(message(page, "assistant", replies.standard)).toHaveCount(1);
  await expectRunFinished(page);

  let firstPid;
  await expect.poll(async () => {
    firstPid = startedPids(await fakePiRecords()).find((pid) => !previousPids.includes(pid));
    return firstPid;
  }).toBeTruthy();
  await expect.poll(async () => stoppedPids(await fakePiRecords())).toContain(firstPid);

  await sendPrompt(page, prompts.standard);
  await expect(message(page, "assistant", replies.standard)).toHaveCount(2);
  await expectRunFinished(page);

  await expect.poll(async () => {
    const pids = startedPids(await fakePiRecords());
    return pids.some((pid) => pid !== firstPid && !previousPids.includes(pid));
  }).toBe(true);
});

async function fakePiRecords() {
  try {
    return (await readFile(fakePiLog, "utf8")).trim().split("\n").filter(Boolean).map((line) => JSON.parse(line));
  } catch (_error) {
    return [];
  }
}

function startedPids(records) {
  return records.filter((record) => record.event === "started" && record.sessionPath?.endsWith("/idle-client.jsonl")).map((record) => record.pid);
}

function stoppedPids(records) {
  return records.filter((record) => record.event === "stopped").map((record) => record.pid);
}
