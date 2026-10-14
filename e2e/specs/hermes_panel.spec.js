import { expect, test } from "@playwright/test";
import { hermes, prompts, replies, sessions } from "../support/contract.mjs";
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

function right(box) {
  return Math.round(box.x + box.width);
}

function scrollbarStart(page) {
  return page.locator("#conversation-scroll").evaluate((scroll) => Math.round(scroll.getBoundingClientRect().right - (scroll.offsetWidth - scroll.clientWidth)));
}

function panel(page) {
  return page.frameLocator("#hermes-panel iframe");
}

// Pi replies in the session open on the page.
async function replyFromPi(page) {
  const reply = message(page, "assistant", replies.standard);
  const count = await reply.count();
  const session = await page.locator('.prompt-form input[name="session"]').inputValue();
  expect((await page.request.post("/prompt", { form: { session, message: prompts.standard } })).ok()).toBe(true);
  await expect(reply).toHaveCount(count + 1);
  await expect(page.locator(".composer-state")).toHaveAttribute("data-state", "done");
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
  // The Hermes tab sits on the window's edge, and then on the panel's, beside the conversation's scrollbar.
  expect(right(await hermesButton(page).boundingBox())).toBe(await scrollbarStart(page));

  await hermesButton(page).click();
  await expect(hermesButton(page)).toHaveAttribute("aria-expanded", "true");
  await askInPanel(page, "Will it rain tomorrow?");
  expect(right(await hermesButton(page).boundingBox())).toBe(await scrollbarStart(page));
  expect(await scrollbarStart(page)).toBeGreaterThan((await page.locator("#hermes-panel").boundingBox()).x - 20);
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
  // The focus goes back to the session, or the hidden panel would keep it, and with it count as looked at.
  await expect(page.getByLabel("Message to Pi")).toBeFocused();
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

test("a reply while the panel is closed marks the Hermes tab", async ({ page }) => {
  await hermesButton(page).click();
  await expect(panel(page).getByLabel("Message to Hermes")).toBeVisible();
  await hermesButton(page).click();
  await expect(page.locator("#hermes-panel")).toBeHidden();
  await expect(hermesButton(page)).toHaveAttribute("title", "Hermes");

  // Another device asks the same conversation.
  const { session } = await (await page.request.get("/hermes/current")).json();
  expect((await page.request.post("/prompt", { form: { session, message: "Ping from the phone" } })).ok()).toBe(true);
  await expect(hermesButton(page)).toHaveAttribute("title", "Hermes replied");

  await hermesButton(page).click();
  await expect(message(panel(page), "assistant", `${hermes.run.replyPrefix}Ping from the phone`)).toBeVisible();
  await expect(hermesButton(page)).toHaveAttribute("title", "Hermes");
});

test("Hermes starting leaves the cursor where you've put it meanwhile", async ({ page }) => {
  let release;
  const held = new Promise((resolve) => { release = resolve; });
  await page.route("**/hermes", async (route) => {
    await held;
    await route.continue();
  });
  await hermesButton(page).click();
  await page.keyboard.press("Control+k");
  const search = page.getByRole("dialog", { name: "Go to or run" }).getByRole("combobox");
  await expect(search).toBeFocused();

  release();
  await expect(page.locator("#hermes-panel")).not.toHaveClass(/is-loading/);
  await page.waitForTimeout(500);
  await expect(search).toBeFocused();
});

test("Hermes starting takes the cursor though Pi replies meanwhile", async ({ page }) => {
  let release;
  const held = new Promise((resolve) => { release = resolve; });
  await page.route("**/hermes", async (route) => {
    await held;
    await route.continue();
  });
  await hermesButton(page).click();
  await replyFromPi(page);

  release();
  await expect(panel(page).getByLabel("Message to Hermes")).toBeFocused();
});

test("shown again, Hermes gives the cursor to the dialog it asks in", async ({ page }) => {
  await hermesButton(page).click();
  const composer = panel(page).getByLabel("Message to Hermes");
  await composer.fill(hermes.permission.prompt);
  await composer.press("Enter");
  const allow = panel(page).getByRole("dialog", { name: hermes.permission.title }).getByRole("option", { name: hermes.permission.allow });
  await expect(allow).toBeFocused();

  await hermesButton(page).click();
  await expect(page.getByLabel("Message to Pi")).toBeFocused();
  await hermesButton(page).click();
  await expect(allow).toBeFocused();
});

test("typing hermes in Ctrl+K shows the panel, and hides it again from inside it", async ({ page }) => {
  // A session named after Hermes doesn't come first.
  await page.route("**/sessions/palette", async (route) => {
    const list = (await (await route.fetch()).json()).sessions;
    await route.fulfill({ json: { sessions: [{ ...list[0], path: "/elsewhere/notes.jsonl", name: "Notes on Hermes" }, ...list] } });
  });
  const palette = page.getByRole("dialog", { name: "Go to or run" });
  const run = async (name) => {
    await page.keyboard.press("Control+k");
    await palette.getByRole("combobox").fill("hermes");
    await expect(palette.getByRole("option").first()).toHaveAccessibleName(name);
    await page.keyboard.press("Enter");
  };
  // Hermes takes the cursor, also while it starts, and Tab goes on to its conversation, as it does in Gripi.
  await run("Show Hermes");
  await expect(panel(page).getByLabel("Message to Hermes")).toBeFocused();
  await page.keyboard.press("Tab");
  await expect(panel(page).locator("#conversation-scroll")).toBeFocused();

  await run("Hide Hermes");
  await expect(page.locator("#hermes-panel")).toBeHidden();
  await expect(page.getByLabel("Message to Pi")).toBeFocused();
  await page.keyboard.press("Tab");
  await expect(page.locator("#conversation-scroll")).toBeFocused();

  await run("Show Hermes");
  await expect(panel(page).getByLabel("Message to Hermes")).toBeFocused();
});

test("a reply in the panel leaves what you type to Pi in Pi's composer", async ({ page }) => {
  await hermesButton(page).click();
  await expect(panel(page).getByLabel("Message to Hermes")).toBeVisible();
  const composer = page.getByLabel("Message to Pi");
  await composer.click();

  const { session } = await (await page.request.get("/hermes/current")).json();
  expect((await page.request.post("/prompt", { form: { session, message: "Ping from the phone" } })).ok()).toBe(true);
  await expect(message(panel(page), "assistant", `${hermes.run.replyPrefix}Ping from the phone`)).toBeVisible();
  await expect(panel(page).getByLabel("Message to Hermes")).toBeEnabled();
  await page.keyboard.type("Still typing");
  await expect(composer).toHaveValue("Still typing");
});

test("a reply from Pi leaves what you type to Hermes in Hermes's composer", async ({ page }) => {
  await hermesButton(page).click();
  const composer = panel(page).getByLabel("Message to Hermes");
  await expect(composer).toBeFocused();

  await replyFromPi(page);
  await page.keyboard.type("Still typing");
  await expect(composer).toHaveValue("Still typing");
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
