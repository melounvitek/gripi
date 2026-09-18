import { expect, test } from "@playwright/test";
import { sessions } from "../support/contract.mjs";
import { expectRunFinished, message, selectSession } from "../support/ui.mjs";
import {
  activateClearQueue, attachClearQueueDraft, clearQueueDraft, expectClearedQueue, expectClearQueueDraft,
  expectClearQueueRunning, expectPendingQueue, prepareClearQueue, stopClearQueueRun
} from "../support/clear_queue.mjs";

test.afterEach(async ({ page }) => stopClearQueueRun(page));

test("cancel Clear queue without sending a request or changing the draft", async ({ page }) => {
  await prepareClearQueue(page, sessions.clearQueue);
  await attachClearQueueDraft(page);
  const clearRequests = [];
  page.on("request", (request) => {
    if (new URL(request.url()).pathname === "/clear_queue") clearRequests.push(request);
  });

  await activateClearQueue(page, { accept: false });

  await expectPendingQueue(page);
  await expectClearQueueDraft(page);
  await expectClearQueueRunning(page);
  expect(clearRequests).toHaveLength(0);
});

test("Clear queue waits for queue events, not a successful HTTP response", async ({ page }) => {
  await prepareClearQueue(page, sessions.clearQueue);
  await attachClearQueueDraft(page);
  const session = await page.locator('.prompt-form [name="session"]').inputValue();
  let release;
  const released = new Promise((resolve) => { release = resolve; });
  await page.route("**/clear_queue", async (route) => {
    await released;
    // Acknowledgement alone must not remove pending messages or restore them into the draft.
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ ok: true, session }) });
  });
  try {
    const requestPromise = page.waitForRequest("**/clear_queue");
    const responsePromise = page.waitForResponse("**/clear_queue");
    await activateClearQueue(page);
    const request = await requestPromise;
    expect(request.method()).toBe("POST");
    expect(request.headers().accept).toContain("application/json");
    const form = await new Request(request.url(), {
      method: request.method(), headers: request.headers(), body: request.postDataBuffer()
    }).formData();
    expect(form.get("session")).toBe(session);
    await expect(page.getByRole("button", { name: "Clear queue", exact: true })).toBeDisabled();
    await expectPendingQueue(page);
    await expectClearQueueDraft(page);
    await expectClearQueueRunning(page);

    release();
    await (await responsePromise).finished();
    // Let the fetch continuation and next paint run before checking for optimistic clearing.
    await page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))));
    await expectPendingQueue(page);
    await expectClearQueueDraft(page);
    await expectClearQueueRunning(page);

    // APIRequestContext bypasses page routes: native queue_update now drives the UI.
    const cleared = await page.request.post("/clear_queue", { form: { session }, headers: { Accept: "application/json" } });
    expect(cleared.ok()).toBe(true);
    await expectClearedQueue(page);
    await expectClearQueueDraft(page);
  } finally {
    release();
  }
});

test("Clear queue restores pending state on reload and clears it in a second tab", async ({ page, context }) => {
  await prepareClearQueue(page, sessions.clearQueue);
  await page.reload();
  await expectPendingQueue(page);
  await expectClearQueueRunning(page);
  await attachClearQueueDraft(page);

  const other = await context.newPage();
  try {
    await other.goto(page.url());
    await expectPendingQueue(other);
    await expectClearQueueRunning(other);
    const responsePromise = other.waitForResponse("**/clear_queue");
    await activateClearQueue(other);
    expect((await responsePromise).status()).toBe(200);

    await expectClearedQueue(other);
    await expectClearedQueue(page);
    await expectClearQueueDraft(page);
    await other.reload();
    await expectClearedQueue(other);
  } finally {
    await other.close();
  }
});

test("failed Clear queue keeps pending messages, attachments and Pi running", async ({ page }) => {
  await prepareClearQueue(page, sessions.clearQueue);
  const image = await attachClearQueueDraft(page);
  await page.route("**/clear_queue", (route) => route.fulfill({
    status: 502, contentType: "application/json", body: JSON.stringify({ error: "Native queue could not be cleared" })
  }));

  await activateClearQueue(page);

  const error = page.locator("[data-clear-queue-error]");
  await expect(error).toBeVisible();
  await expect(error).toContainText("Native queue could not be cleared");
  await expectPendingQueue(page);
  await expectClearQueueDraft(page);
  await expectClearQueueRunning(page);

  await page.unroute("**/clear_queue");
  await activateClearQueue(page);
  await expectClearedQueue(page);
  await expect(error).toBeHidden();
  await expectClearQueueDraft(page);

  // Submit the preserved draft and verify the saved image, not just its preview.
  await stopClearQueueRun(page);
  await expectRunFinished(page);
  const submission = page.waitForResponse("**/prompt");
  await page.getByLabel("Message to Pi").press("Enter");
  expect((await submission).ok()).toBe(true);
  await expectRunFinished(page);
  await page.reload();
  const savedImage = message(page, "user", clearQueueDraft).locator(".message-image");
  await expect(savedImage).toBeVisible();
  const bytes = await savedImage.evaluate(async (element) => Array.from(new Uint8Array(await (await fetch(element.src)).arrayBuffer())));
  expect(Buffer.from(bytes).equals(image)).toBe(true);
});

test("late Clear queue errors do not affect a different session", async ({ page }) => {
  await prepareClearQueue(page, sessions.clearQueue);
  let release;
  const released = new Promise((resolve) => { release = resolve; });
  await page.route("**/clear_queue", async (route) => {
    await released;
    await route.fulfill({ status: 502, contentType: "application/json", body: JSON.stringify({ error: "Late queue failure" }) });
  });
  try {
    const request = page.waitForRequest("**/clear_queue");
    const response = page.waitForResponse("**/clear_queue");
    await activateClearQueue(page);
    await request;
    await selectSession(page, sessions.controlsAbort);
    await page.getByLabel("Message to Pi").fill("Another session's draft");
    release();
    await (await response).finished();
    await page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))));
    await expect(page.locator("[data-clear-queue-error]")).toBeHidden();
    await expect(page.getByLabel("Message to Pi")).toHaveValue("Another session's draft");
    await selectSession(page, sessions.clearQueue);
    await expectPendingQueue(page);
    await expect(page.getByRole("button", { name: "Clear queue", exact: true })).toBeEnabled();
  } finally {
    release();
  }
});
