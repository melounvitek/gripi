import { expect, test } from "@playwright/test";
import { hermes, sessions } from "../support/contract.mjs";
import { message, selectSession } from "../support/ui.mjs";

// Each test talks in a new conversation, which every Hermes panel shows.
test.beforeEach(async ({ page }) => {
  expect((await page.request.post("/hermes/new")).ok()).toBe(true);
  await page.goto("/");
  await selectSession(page, sessions.marker);
});

function hermesButton(page) {
  return page.getByRole("button", { name: "Hermes", exact: true });
}

function panel(page) {
  return page.frameLocator("#hermes-panel iframe");
}

async function askInPanel(page, prompt) {
  const composer = panel(page).getByLabel("Message to Hermes");
  await composer.fill(prompt);
  await composer.press("Enter");
  await expect(message(panel(page), "assistant", `${hermes.run.replyPrefix}${prompt}`)).toBeVisible();
}

test("Hermes talks in a panel next to the session, which switching sessions and reloading leave alone", async ({ page }) => {
  // Hermes's conversations, saved or new, are not sessions in the sidebar.
  await expect(page.locator(".session-row").filter({ hasText: hermes.saved.title })).toHaveCount(0);
  await expect(page.locator("#hermes-panel")).toBeHidden();

  await hermesButton(page).click();
  await expect(hermesButton(page)).toHaveAttribute("aria-expanded", "true");
  await askInPanel(page, "Will it rain tomorrow?");
  await panel(page).getByLabel("Message to Hermes").fill("A half-typed question");
  // On a wide screen, the session stays in full view next to the panel.
  const conversation = await page.locator("#conversation-scroll").boundingBox();
  const shown = await page.locator("#hermes-panel").boundingBox();
  expect(conversation.x + conversation.width).toBeLessThanOrEqual(shown.x);

  await selectSession(page, sessions.history);
  await expect(panel(page).getByLabel("Message to Hermes")).toHaveValue("A half-typed question");
  await expect(hermesButton(page)).toHaveAttribute("aria-expanded", "true");

  await page.reload();
  await expect(message(panel(page), "assistant", `${hermes.run.replyPrefix}Will it rain tomorrow?`)).toBeVisible();

  await panel(page).getByRole("button", { name: "Close Hermes" }).click();
  await expect(page.locator("#hermes-panel")).toBeHidden();
  await expect(hermesButton(page)).toHaveAttribute("aria-expanded", "false");
  // The hidden panel would otherwise keep the focus, and with it count as looked at.
  await expect(hermesButton(page)).toBeFocused();
});

test("the panel says Hermes is starting until its conversation shows", async ({ page }) => {
  let release;
  const held = new Promise((resolve) => { release = resolve; });
  await page.route("**/hermes", async (route) => {
    await held;
    await route.continue();
  });
  await hermesButton(page).click();
  await expect(page.locator("#hermes-panel").getByRole("status")).toHaveText("Starting Hermes…");

  release();
  await expect(panel(page).getByLabel("Message to Hermes")).toBeVisible();
  await expect(page.locator("#hermes-panel").getByRole("status")).toBeHidden();
});

test("a reply while the panel is closed marks the Hermes button, and the sessions button while the sidebar is hidden", async ({ page }) => {
  await hermesButton(page).click();
  await expect(panel(page).getByLabel("Message to Hermes")).toBeVisible();
  await hermesButton(page).click();
  await expect(page.locator("#hermes-panel")).toBeHidden();
  const dot = hermesButton(page).locator(".hermes-reply-dot");
  await expect(dot).toBeHidden();

  // Another device asks the same conversation.
  const { session } = await (await page.request.get("/hermes/current")).json();
  expect((await page.request.post("/prompt", { form: { session, message: "Ping from the phone" } })).ok()).toBe(true);
  await expect(dot).toBeVisible();
  await page.getByRole("button", { name: "Hide sessions" }).click();
  await expect(page.getByRole("button", { name: "Show sessions" }).locator(".hermes-reply-dot")).toBeVisible();
  await page.getByRole("button", { name: "Show sessions" }).click();

  await hermesButton(page).click();
  await expect(message(panel(page), "assistant", `${hermes.run.replyPrefix}Ping from the phone`)).toBeVisible();
  await expect(dot).toBeHidden();
});

test("a new conversation in one window replaces it in every other", async ({ page, context }) => {
  await hermesButton(page).click();
  await askInPanel(page, "Plan my week");
  const other = await context.newPage();
  await other.goto("/hermes");
  await expect(message(other, "assistant", `${hermes.run.replyPrefix}Plan my week`)).toBeVisible();

  await other.getByRole("button", { name: "new", exact: true }).click();
  await expect(message(other, "user", "Plan my week")).toHaveCount(0);
  await expect(message(panel(page), "user", "Plan my week")).toHaveCount(0, { timeout: 10_000 });
  await expect(panel(page).getByLabel("Message to Hermes")).toBeVisible();
  await other.close();
});

test("closing the panel while the sidebar is hidden takes the focus out of it", async ({ page }) => {
  await hermesButton(page).click();
  await panel(page).getByLabel("Message to Hermes").click();
  await page.getByRole("button", { name: "Hide sessions" }).click();

  await panel(page).getByRole("button", { name: "Close Hermes" }).click();
  await expect(page.locator("#hermes-panel")).toBeHidden();
  expect(await page.evaluate(() => document.activeElement?.tagName)).not.toBe("IFRAME");
});

test("a window the browser blocks leaves the panel as it was", async ({ page }) => {
  await hermesButton(page).click();
  await askInPanel(page, "Draft a reply to the landlord");
  await page.evaluate(() => { window.open = () => null; });

  await panel(page).getByRole("button", { name: "Open Hermes in a new window" }).click();
  await expect(page.locator("#hermes-panel")).toBeVisible();
  await expect(message(panel(page), "assistant", `${hermes.run.replyPrefix}Draft a reply to the landlord`)).toBeVisible();
});

test("the panel opens in its own window, which the Hermes button then brings forward", async ({ page }) => {
  await hermesButton(page).click();
  await askInPanel(page, "Draft a reply to the landlord");

  const popup = page.waitForEvent("popup");
  await panel(page).getByRole("button", { name: "Open Hermes in a new window" }).click();
  const window = await popup;
  await expect(message(window, "assistant", `${hermes.run.replyPrefix}Draft a reply to the landlord`)).toBeVisible();
  await expect(page.locator("#hermes-panel")).toBeHidden();
  // Only the panel itself can be closed or popped out.
  await expect(window.getByRole("button", { name: "Close Hermes" })).toBeHidden();

  await hermesButton(page).click();
  await expect(page.locator("#hermes-panel")).toBeHidden();
  await window.close();
  await hermesButton(page).click();
  await expect(message(panel(page), "assistant", `${hermes.run.replyPrefix}Draft a reply to the landlord`)).toBeVisible();
});
