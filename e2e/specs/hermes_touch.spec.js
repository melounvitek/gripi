import { expect, test } from "@playwright/test";
import { hermes } from "../support/contract.mjs";
import { message } from "../support/ui.mjs";

test("a voice conversation starts and ends, and what was said opens, on the first taps", async ({ page }) => {
  await page.goto("/hermes");
  const call = page.getByRole("group", { name: "Voice conversation with Hermes" });
  await page.getByRole("button", { name: "Start a voice conversation" }).tap();
  await expect(page.locator("[data-voice-call-view] [data-voice-call-state]")).toHaveText("listening");
  const spoken = message(page, "user", hermes.voice.transcript).last();
  await expect(spoken).toBeAttached();

  await call.getByRole("button", { name: "end", exact: true }).tap();
  await expect(call).toBeHidden();
  await expect(page.getByLabel("Message to Hermes")).toBeVisible();

  await spoken.locator(".message-spoken-bars").tap();
  await expect(spoken.locator(".message-body")).toBeVisible();
});

test("the Hermes tab opens the panel over the whole screen, which goes back to Gripi, on the first taps", async ({ page }) => {
  expect((await page.request.post("/hermes/new")).ok()).toBe(true);
  await page.goto("/");
  const tab = page.getByRole("button", { name: "Hermes", exact: true });
  await tab.tap();
  const panel = page.locator("#hermes-panel");
  await expect(page.frameLocator("#hermes-panel iframe").getByLabel("Message to Hermes")).toBeVisible();
  expect(await panel.boundingBox()).toEqual({ x: 0, y: 0, ...page.viewportSize() });

  const back = page.frameLocator("#hermes-panel iframe").getByRole("button", { name: "Back to Gripi" });
  const { width, height } = await back.boundingBox();
  expect(Math.min(width, height)).toBeGreaterThanOrEqual(44);
  await back.tap();
  await expect(panel).toBeHidden();
  // Back in the session, without the keyboard.
  await expect(page.locator("#conversation-scroll")).toBeFocused();

  const { session } = await (await page.request.get("/hermes/current")).json();
  expect((await page.request.post("/prompt", { form: { session, message: "Ping from the desk" } })).ok()).toBe(true);
  await expect(tab).toHaveAttribute("title", "Hermes replied");
});

test("the panel closes on the first tap while Hermes is starting, and stays closed after a reload", async ({ page }) => {
  await page.route("**/hermes", () => {});
  await page.goto("/");
  await page.getByRole("button", { name: "Hermes", exact: true }).tap();
  const panel = page.locator("#hermes-panel");
  await expect(panel.getByRole("status")).toHaveText("Starting Hermes…");

  const close = panel.getByRole("button", { name: "Close" });
  const { width, height } = await close.boundingBox();
  expect(Math.min(width, height)).toBeGreaterThanOrEqual(44);
  await close.tap();
  await expect(panel).toBeHidden();

  await page.reload();
  await expect(page.locator("#conversation-scroll")).toBeVisible();
  await expect(panel).toBeHidden();
});

test("a question about a long path fits the phone screen", async ({ page }) => {
  // What Hermes asks names the whole path, which has no space to break at.
  const path = "/home/user/projects/a-long-project-name/with/nested/folders/to/a-long-file-name.ts";
  await page.route(/\/events\?/, async (route) => {
    const batch = await (await route.fetch()).json();
    for (const event of batch.events ?? []) {
      if (event.type === "extension_ui_request") Object.assign(event, { title: `recursive delete: rm -rf ${path}`, message: `$ rm -rf ${path}` });
    }
    await route.fulfill({ json: batch });
  });
  expect((await page.request.post("/hermes/new")).ok()).toBe(true);
  const { session } = await (await page.request.get("/hermes/current")).json();
  await page.goto("/hermes");
  expect((await page.request.post("/prompt", { form: { session, message: hermes.permission.prompt } })).ok()).toBe(true);

  const dialog = page.getByRole("dialog", { name: `recursive delete: rm -rf ${path}` });
  await expect(dialog).toContainText(`$ rm -rf ${path}`);
  // Nothing to scroll sideways.
  expect(await dialog.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true);
  const close = dialog.getByRole("button", { name: "Cancel" });
  const { width, height } = await close.boundingBox();
  expect(Math.min(width, height)).toBeGreaterThanOrEqual(44);
  await close.tap();
  await expect(dialog).toBeHidden();
});
