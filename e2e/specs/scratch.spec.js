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

// The button blinks while Pi works, and shows a dot when Pi finished while the overlay was hidden.
function scratchButtonState(page) {
  return scratchButton(page).evaluate((button) => {
    if (getComputedStyle(button).animationName === "progress-blink") return "working";
    return getComputedStyle(button, "::after").content === "none" ? "idle" : "finished";
  });
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

function scratchKey(page) {
  return page.locator("[data-scratch-overlay] iframe").evaluate((frame) => new URL(frame.src).searchParams.get("session"));
}

async function expectStartedOver(page, key) {
  await expect.poll(async () => { const next = await scratchKey(page); return !!next && next !== key; }).toBe(true);
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

  // Ctrl+Shift+N hides it, and the page is as it was.
  await page.keyboard.press("Control+Shift+N");
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
  const key = await scratchKey(page);
  await page.keyboard.press("Control+Shift+N");
  await expect(overlay(page)).toBeHidden();

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
  await scratch(page).getByRole("button", { name: "Discard the scratch session" }).click();
  await expect(overlay(page)).toBeHidden();
  await expect(scratchButton(page)).not.toHaveClass(/is-active/);
  await openScratch(page);
  await expect(message(scratch(page), "user", question)).toHaveCount(0);
});

test("a scratch session that works goes on while hidden, and Esc twice stops it, as in any session", async ({ page }) => {
  await openScratch(page);
  const frame = scratch(page);
  await sendPrompt(frame, prompts.abortStart);
  await expect(frame.locator(".composer-state")).toHaveAttribute("data-state", "running");

  await frame.getByRole("button", { name: "Hide the scratch session" }).click();
  await expect(overlay(page)).toBeHidden();

  await openScratch(page);
  await expect(frame.locator(".composer-state")).toHaveAttribute("data-state", "running");
  await expect(frame.getByLabel(/^Message to Pi$/)).toBeFocused();
  await page.keyboard.press("Escape");
  await expect(frame.locator(".composer-state")).toHaveText("Press ESC again to stop current task");
  await page.keyboard.press("Escape");
  await expectRunFinished(frame);
  await expect(overlay(page)).toBeVisible();
});

test("a hidden scratch session shows on its button that Pi works, and then that it finished until it shows again", async ({ page }) => {
  await openScratch(page);
  const frame = scratch(page);
  await sendPrompt(frame, prompts.longCommand);
  await expect(frame.locator(".composer-state")).toHaveAttribute("data-state", "running");
  await frame.getByRole("button", { name: "Hide the scratch session" }).click();
  await expect(overlay(page)).toBeHidden();
  await expect.poll(() => scratchButtonState(page)).toBe("working");

  await expect.poll(() => scratchButtonState(page)).toBe("finished");
  await openScratch(page);
  await expectRunFinished(frame);
  await frame.getByRole("button", { name: "Hide the scratch session" }).click();
  await expect(overlay(page)).toBeHidden();
  expect(await scratchButtonState(page)).toBe("idle");

  // A session that ends elsewhere while Pi works there didn't finish.
  await openScratch(page);
  await sendPrompt(frame, prompts.abortStart);
  await expect(frame.locator(".composer-state")).toHaveAttribute("data-state", "running");
  await frame.getByRole("button", { name: "Hide the scratch session" }).click();
  await expect.poll(() => scratchButtonState(page)).toBe("working");
  expect((await page.request.post("/scratch/discard", { headers: { Accept: "application/json" } })).ok()).toBe(true);
  await expect.poll(() => scratchButtonState(page)).toBe("idle");
});

test("in a scratch session, Ctrl+K does nothing and Ctrl+N starts it over", async ({ page }) => {
  await openScratch(page);
  await sendPrompt(scratch(page), question);
  await expectRunFinished(scratch(page));
  const key = await scratchKey(page);

  // Ctrl+K before Ctrl+N, so that a palette that opened would take Ctrl+N.
  await page.keyboard.press("Control+K");
  await page.keyboard.press("Control+N");
  await expectStartedOver(page, key);
  await expect(overlay(page)).not.toHaveClass(/is-loading/);
  await expect(scratch(page).getByLabel(/^Message to Pi$/)).toBeFocused();
  await expect(message(scratch(page), "user", question)).toHaveCount(0);
  await expect(page.getByRole("dialog", { name: "Go to or run" })).toBeHidden();
  await expect(page.getByRole("dialog", { name: "New session", exact: true })).toBeHidden();
  await expect(scratchButton(page)).toHaveClass(/is-active/);
});

test("while a scratch session starts, its overlay says so, and keys around it do nothing but hide it", async ({ page }) => {
  let start;
  const started = new Promise((resolve) => { start = resolve; });
  await page.route("**/scratch/new", async (route) => {
    await started;
    await route.continue();
  });
  await page.keyboard.press("Control+f");
  const find = page.locator("[data-current-session-find]");
  await expect(find).toBeVisible();

  await scratchButton(page).click();
  await expect(overlay(page).getByText("Starting a scratch session…")).toBeVisible();
  for (const key of ["Control+K", "Control+Shift+F", "Control+N", "Escape"]) await page.keyboard.press(key);
  await expect(overlay(page)).toBeVisible();
  await page.keyboard.press("Control+Shift+N");
  await expect(overlay(page)).toBeHidden();
  await expect(find).toBeVisible();
  await expect(page.getByRole("dialog", { name: "Go to or run" })).toBeHidden();
  await expect(page.getByRole("dialog", { name: "New session", exact: true })).toBeHidden();
  await expect(page.locator('input[name="session_search"]')).toBeHidden();

  start();
  await expect(scratchButton(page)).toHaveClass(/is-active/);
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

test("the desktop app's menu finds in a scratch session and starts it over while its overlay shows, and does nothing else", async ({ page }) => {
  await openScratch(page);
  await sendPrompt(scratch(page), question);
  await expectRunFinished(scratch(page));
  await page.evaluate(() => window.dispatchEvent(new CustomEvent("gripi:current-session-find-requested")));
  await expect(scratch(page).locator("[data-current-session-find]")).toBeVisible();
  await expect(page.locator("[data-current-session-find]")).toBeHidden();

  await page.evaluate(() => {
    window.dispatchEvent(new CustomEvent("gripi:command-palette-requested"));
    window.dispatchEvent(new CustomEvent("gripi:session-search-requested"));
  });
  await expect(overlay(page)).toBeVisible();
  await expect(page.getByRole("dialog", { name: "Go to or run" })).toBeHidden();
  await expect(page.locator('input[name="session_search"]')).toBeHidden();

  const key = await scratchKey(page);
  await page.evaluate(() => window.dispatchEvent(new CustomEvent("gripi:new-session-requested")));
  await expectStartedOver(page, key);
  await expect(overlay(page)).not.toHaveClass(/is-loading/);
  await expect(message(scratch(page), "user", question)).toHaveCount(0);
  await expect(page.getByRole("dialog", { name: "New session", exact: true })).toBeHidden();
});
