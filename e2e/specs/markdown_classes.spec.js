import { expect, test } from "@playwright/test";
import { prompts, sessions } from "../support/contract.mjs";
import { expectRunFinished, message, selectSession, sendPrompt } from "../support/ui.mjs";

for (const scenario of [
  { name: "raw HTML", key: "markdownRaw", texts: ["Raw span stays in conversation", "Raw code stays in conversation"] },
  { name: "code fences", key: "markdownFence", texts: ["Modal fence stays in conversation", "Image fence stays in conversation"] }
]) {
  test(`keep Markdown ${scenario.name} inside the conversation without blocking input`, async ({ page, isMobile }) => {
    await page.goto("/?no_session=1");
    if (isMobile) await page.locator('label[aria-label="Open sessions"]').tap();
    await selectSession(page, sessions[`${scenario.key}${isMobile ? "Mobile" : "Desktop"}`]);
    await sendPrompt(page, prompts[scenario.key]);
    await expectRunFinished(page);

    for (const stage of ["live", "reloaded history"]) {
      await test.step(stage, async () => {
        if (stage === "reloaded history") await page.reload();
        const response = message(page, "assistant", scenario.texts[0]);
        await expect(response).toBeVisible();
        const conversation = await page.locator("#conversation-scroll").boundingBox();
        const article = await response.boundingBox();

        for (const text of scenario.texts) {
          const content = response.getByText(text, { exact: true });
          await expect(content).toBeVisible();
          const bounds = await content.boundingBox();
          expect.soft(bounds.x, `${stage}: ${text} left edge`).toBeGreaterThanOrEqual(Math.max(article.x, conversation.x) - 1);
          expect.soft(bounds.x + bounds.width, `${stage}: ${text} right edge`).toBeLessThanOrEqual(Math.min(article.x + article.width, conversation.x + conversation.width) + 1);
          expect.soft(bounds.y, `${stage}: ${text} top edge`).toBeGreaterThanOrEqual(article.y - 1);
          expect.soft(bounds.y + bounds.height, `${stage}: ${text} bottom edge`).toBeLessThanOrEqual(article.y + article.height + 1);
        }

        if (scenario.key === "markdownFence") {
          const code = response.locator("pre code").filter({ hasText: "const safeValue = 42;" });
          await expect(code).toBeVisible();
          const highlighted = await code.evaluate((element) => [...element.querySelectorAll("span")].some(
            (span) => getComputedStyle(span).color !== getComputedStyle(element).color
          ));
          expect(highlighted).toBe(true);
        }

        const composer = page.getByLabel("Message to Pi");
        await composer.evaluate((element) => element.blur());
        const bounds = await composer.boundingBox();
        const x = bounds.x + bounds.width / 2;
        const y = bounds.y + bounds.height / 2;
        // Send one physical input, even when an injected overlay intercepts it.
        if (isMobile) await page.touchscreen.tap(x, y);
        else await page.mouse.click(x, y);
        await expect.soft(composer, `${stage}: first ${isMobile ? "tap" : "click"} focuses composer`).toBeFocused({ timeout: 1000 });
      });
    }
  });
}
