import { expect, test } from "@playwright/test";
import { footerModel, footerStatus, prompts, sessions } from "../support/contract.mjs";
import { expectRunFinished, sendPrompt } from "../support/ui.mjs";

async function expectFixedFooter(page) {
  const footer = page.locator("#session-status-bar");
  await expect(footer.locator("[data-status-key]")).toHaveCount(2);
  await expect(footer.locator('[data-status-key="ctx"]')).toContainText("CTX");
  await expect(footer.getByRole("button", { name: "Open model and thinking settings" })).toBeVisible();
}

async function expectContainedLayout(page) {
  const layout = await page.evaluate(() => {
    const selectors = [".session-header", ".conversation-scroll", ".composer", "#session-status-bar", '[data-status-key="ctx"]', ".model-settings-chip", ".send-control"];
    return selectors.map((selector) => {
      const element = document.querySelector(selector);
      const bounds = element.getBoundingClientRect();
      return { selector, left: bounds.left, right: bounds.right, viewport: innerWidth, overflow: element.scrollWidth - element.clientWidth };
    });
  });
  for (const bounds of layout) {
    expect(bounds.left, bounds.selector).toBeGreaterThanOrEqual(0);
    expect(bounds.right, bounds.selector).toBeLessThanOrEqual(bounds.viewport + 1);
    expect(bounds.overflow, bounds.selector).toBeLessThanOrEqual(1);
  }
}

// The composer's rules span the conversation column; composer, footer and desktop header text share its inset edges.
async function expectColumnAlignment(page, isMobile) {
  const edges = await page.evaluate(() => {
    const box = (selector) => document.querySelector(selector).getBoundingClientRect();
    const textLeft = (selector) => {
      const range = document.createRange();
      range.selectNodeContents(document.querySelector(selector));
      return range.getBoundingClientRect().left;
    };
    const textarea = document.querySelector(".composer-textarea-wrap textarea");
    const inset = parseFloat(getComputedStyle(document.documentElement).getPropertyValue("--text-inset")) * parseFloat(getComputedStyle(document.documentElement).fontSize);
    return {
      column: box("#live-output"), rule: box(".composer-input-row"), inset,
      textarea: textarea.getBoundingClientRect().left + parseFloat(getComputedStyle(textarea).paddingLeft),
      footerLeft: textLeft('[data-status-key="ctx"] .session-status-label'), footerRight: box(".model-settings-chip").right,
      title: textLeft(".session-header-name"), actionsRight: box(".session-header-actions").right
    };
  });
  expect(Math.abs(edges.rule.left - edges.column.left)).toBeLessThanOrEqual(1);
  expect(Math.abs(edges.rule.right - edges.column.right)).toBeLessThanOrEqual(1);
  for (const left of [edges.textarea, edges.footerLeft, ...(isMobile ? [] : [edges.title])]) {
    expect(Math.abs(left - edges.column.left - edges.inset)).toBeLessThanOrEqual(1);
  }
  for (const right of [edges.footerRight, ...(isMobile ? [] : [edges.actionsRight])]) {
    expect(Math.abs(edges.column.right - edges.inset - right)).toBeLessThanOrEqual(1);
  }
}

async function activate(control, isMobile) {
  if (isMobile) await control.tap();
  else await control.click();
}

test.beforeEach(async ({ page, isMobile }) => {
  const title = isMobile ? sessions.footerMobile : sessions.footer;
  await page.goto(`/?${new URLSearchParams({ session_search: title })}`);
  if (isMobile) await page.locator('label[aria-label="Open sessions"]').tap();
  await activate(page.getByRole("link", { name: new RegExp(title) }), isMobile);
  await expect(page.getByRole("heading", { level: 1, name: title })).toBeVisible();
});

test("plugin statuses do not change the footer live or after reload", async ({ page }) => {
  await expectFixedFooter(page);
  const model = page.locator('[data-status-key="model"] .session-status-value');
  const before = await model.textContent();
  await sendPrompt(page, prompts.extension);
  const dialog = page.getByRole("dialog", { name: "Approve release?" });
  await expect(dialog).toBeVisible();
  try {
    await expectFixedFooter(page);
    await expect(model).toHaveText(before);

    await page.reload();
    await expect(dialog).toBeVisible();
    const state = await page.locator("#live-output").getAttribute("data-extension-ui-state");
    expect(JSON.parse(state).statuses).toContainEqual(expect.objectContaining(footerStatus));
    await expectFixedFooter(page);
    await expectContainedLayout(page);
  } finally {
    await dialog.getByRole("button", { name: "Confirm" }).click();
    await expectRunFinished(page);
  }
});

test("long provider model names stay contained and settings open on first activation", async ({ page, isMobile }) => {
  const chip = page.getByRole("button", { name: "Open model and thinking settings" });
  const dialog = page.getByRole("dialog", { name: "Model & thinking" });
  await activate(chip, isMobile);
  await expect(dialog).toBeVisible();
  await activate(dialog.getByRole("button", { name: "all", exact: true }), isMobile);
  await activate(dialog.getByRole("option", { name: new RegExp(footerModel.id) }), isMobile);
  await expect(dialog).toBeHidden();
  await activate(chip, isMobile);
  await activate(dialog.getByRole("button", { name: "high", exact: true }), isMobile);
  await expect(dialog).toBeHidden();
  await expect(chip).toContainText(`${footerModel.provider}/${footerModel.id} (high)`);

  for (const width of isMobile ? [393] : [1440, 900]) {
    await page.setViewportSize({ width, height: 900 });
    for (const reload of [false, true]) {
      if (reload) await page.reload();
      await expect(chip).toContainText(`${footerModel.provider}/${footerModel.id} (high)`);
      await expectFixedFooter(page);
      await expectContainedLayout(page);
      await expectColumnAlignment(page, isMobile);
      const value = chip.locator(".session-status-value");
      await expect(value).toHaveCSS("text-overflow", "ellipsis");
      expect(await value.evaluate((element) => element.scrollWidth > element.clientWidth)).toBe(true);
      await activate(chip, isMobile);
      await expect(dialog).toBeVisible();
      await activate(dialog.getByRole("button", { name: "Close model and thinking settings" }), isMobile);
    }
  }
});
