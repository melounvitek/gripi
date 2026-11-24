import { expect, test } from "@playwright/test";
import { prompts, replies, sessions } from "../support/contract.mjs";
import { expectRunFinished, message, selectSession, sendPrompt } from "../support/ui.mjs";

const question = "Scratch question that no session holds";

// Each test starts without a scratch session.
test.beforeEach(async ({ page }) => {
  expect((await page.request.post("/scratch/discard", { headers: { Accept: "application/json" } })).ok()).toBe(true);
  await page.goto("/");
  await selectSession(page, sessions.marker);
});

function scratchButton(page) {
  return page.getByRole("button", { name: "Scratch session, not saved" });
}

function overlay(page) {
  return page.locator("[data-scratch-overlay]");
}

function scratch(page) {
  return page.frameLocator("[data-scratch-overlay] iframe");
}

async function openScratch(page) {
  await scratchButton(page).click();
  await expect(overlay(page)).toBeVisible();
  await expect(overlay(page)).not.toHaveClass(/is-loading/);
  await expect(scratch(page).getByRole("heading", { name: /Scratch/ })).toBeVisible();
}

async function expectLoadedImage(image) {
  await expect(image).toBeVisible();
  await expect.poll(() => image.evaluate((element) => element.naturalWidth)).toBeGreaterThan(0);
}

test("a scratch session opens over the page, keeps its conversation and images, and is never listed or stored", async ({ page }) => {
  const url = page.url();
  await expect(scratchButton(page)).not.toHaveClass(/is-active/);
  await openScratch(page);
  await expect(scratchButton(page)).toHaveClass(/is-active/);
  await expect(page.locator(".app-shell")).toHaveJSProperty("inert", true);
  const frame = scratch(page);
  await expect(frame.getByText("not saved")).toBeVisible();
  await expect(frame.getByText("no folder")).toBeVisible();

  await frame.locator("#image-input").setInputFiles({ name: "scratch.png", mimeType: "image/png", buffer: await page.screenshot({ clip: { x: 0, y: 0, width: 40, height: 40 } }) });
  await sendPrompt(frame, question);
  await expect(message(frame, "assistant", replies.standard)).toBeVisible();
  await expectRunFinished(frame);
  await expectLoadedImage(message(frame, "user", question).locator("img"));

  // Esc hides it, and the page is as it was.
  await page.keyboard.press("Escape");
  await expect(overlay(page)).toBeHidden();
  await expect(page.locator(".app-shell")).toHaveJSProperty("inert", false);
  expect(page.url()).toBe(url);

  // After a reload, Ctrl+Shift+N shows it again, with its conversation from Pi.
  await page.reload();
  await expect(scratchButton(page)).toHaveClass(/is-active/);
  await page.keyboard.press("Control+Shift+N");
  await expect(overlay(page)).toBeVisible();
  await expect(message(scratch(page), "assistant", replies.standard)).toBeVisible();
  await expectLoadedImage(message(scratch(page), "user", question).locator("img"));
  const key = new URL(await page.locator("[data-scratch-overlay] iframe").getAttribute("src"), page.url()).searchParams.get("session");
  await page.keyboard.press("Escape");

  // No list shows it, and the browser keeps nothing of it.
  await page.keyboard.press("Control+K");
  const palette = page.getByRole("dialog", { name: "Go to or run" });
  await palette.getByRole("combobox").fill("Scratch question");
  await expect(palette.locator(".command-palette-name", { hasText: question })).toHaveCount(0);
  await page.keyboard.press("Escape");
  await expect(page.locator(".session-sidebar")).not.toContainText(question);
  const stored = await page.evaluate(() => JSON.stringify({ ...window.localStorage }) + JSON.stringify({ ...window.sessionStorage }));
  expect(stored).not.toContain(key);

  // Discard ends it.
  await scratchButton(page).click();
  await scratch(page).getByRole("button", { name: "discard" }).click();
  await expect(overlay(page)).toBeHidden();
  await expect(scratchButton(page)).not.toHaveClass(/is-active/);
  await openScratch(page);
  await expect(message(scratch(page), "user", question)).toHaveCount(0);
});

test("Esc hides a scratch session that works, which goes on", async ({ page }) => {
  await openScratch(page);
  const frame = scratch(page);
  await sendPrompt(frame, prompts.abortStart);
  await expect(frame.locator(".composer-state")).toHaveAttribute("data-state", "running");

  await page.keyboard.press("Escape");
  await expect(overlay(page)).toBeHidden();

  await openScratch(page);
  await expect(frame.locator(".composer-state")).toHaveAttribute("data-state", "running");
  await frame.getByRole("button", { name: "Abort running Pi" }).click();
  await expect(frame.locator(".composer-state")).not.toHaveAttribute("data-state", "running");
});

test("Ctrl+K and Ctrl+N in a scratch session reach the page around it", async ({ page }) => {
  await openScratch(page);
  await expect(scratch(page).getByLabel(/^Message to Pi$/)).toBeFocused();

  await page.keyboard.press("Control+K");
  await expect(overlay(page)).toBeHidden();
  await expect(page.getByRole("dialog", { name: "Go to or run" })).toBeVisible();
  await page.keyboard.press("Escape");

  await openScratch(page);
  await expect(scratch(page).getByLabel(/^Message to Pi$/)).toBeFocused();
  await page.keyboard.press("Control+N");
  await expect(overlay(page)).toBeHidden();
  await expect(page.getByRole("dialog", { name: "New session", exact: true })).toBeVisible();
});

test("a scratch session that ends elsewhere says so in its overlay", async ({ page }) => {
  await openScratch(page);
  expect((await page.request.post("/scratch/discard", { headers: { Accept: "application/json" } })).ok()).toBe(true);
  await expect(scratch(page).getByText("This scratch session has ended.")).toBeVisible();
  await expect(scratch(page).getByLabel(/^Message to Pi$/)).toBeHidden();

  await scratch(page).getByRole("button", { name: "Start a new one" }).click();
  await expect(scratch(page).getByLabel(/^Message to Pi$/)).toBeVisible();
  await expect(scratch(page).getByText("This scratch session has ended.")).toBeHidden();
});

test("the desktop app's menu finds in a scratch session while its overlay shows, and leaves it for a new session", async ({ page }) => {
  await openScratch(page);
  await sendPrompt(scratch(page), question);
  await expectRunFinished(scratch(page));
  await page.evaluate(() => window.dispatchEvent(new CustomEvent("gripi:current-session-find-requested")));
  await expect(scratch(page).locator("[data-current-session-find]")).toBeVisible();
  await expect(page.locator("[data-current-session-find]")).toBeHidden();

  await page.evaluate(() => window.dispatchEvent(new CustomEvent("gripi:new-session-requested")));
  await expect(overlay(page)).toBeHidden();
  await expect(page.getByRole("dialog", { name: "New session", exact: true })).toBeVisible();
});
