import { expect, test } from "@playwright/test";
import { sessions } from "../support/contract.mjs";

for (const width of [320, 341, 393, 768, 1440]) {
  test(`header icons stay inline at ${width}px through live edits, polling and reload`, async ({ page, isMobile }, testInfo) => {
    if (isMobile && width === 1440) test.skip();
    test.setTimeout(60_000);
    await page.goto("/?show_all_sessions=1");
    const session = await page.locator(".session-row").filter({ hasText: sessions.marker }).getAttribute("data-session-path");
    const tags = ["mixit_cross_sell", "new_mixit_club", "z".repeat(64), "🧪".repeat(32)];
    const assign = async (tag, assigned) => {
      expect((await page.request.post("/sessions/tags", { form: { session, tag, assigned: String(assigned) } })).ok()).toBe(true);
    };
    const activate = (control) => isMobile ? control.tap() : control.click();
    const header = page.locator(".session-header");
    const group = header.locator(".header-tags");
    const edit = header.getByRole("button", { name: "Edit session tags", exact: true });
    const dialog = page.getByRole("dialog", { name: "Session tags", exact: true });
    const close = dialog.getByRole("button", { name: "Close tag picker", exact: true });
    const icon = group.getByRole("button", { name: `Filter sessions by ${tags[0]}`, exact: true });
    const activity = header.getByRole("switch", { name: "Show agent activity", exact: true });
    const more = group.locator(".tag-overflow");
    try {
      await page.setViewportSize({ width, height: 900 });
      await page.clock.install();
      await page.goto(`/?${new URLSearchParams({ session })}`);
      const emptyHeight = (await header.boundingBox()).height;
      await activate(edit);
      await dialog.getByRole("searchbox").fill(tags[0]);
      await activate(dialog.getByRole("button", { name: `Create “${tags[0]}”`, exact: true }));
      await expect(dialog.getByRole("checkbox", { name: tags[0], exact: true })).toBeChecked();
      await activate(close);
      const checkLayout = async () => {
        await expect(icon.locator("svg")).toBeVisible();
        expect(await group.evaluate((element) => element.parentElement.classList.contains("session-header-project"))).toBe(true);
        const project = await header.locator(".session-header-project-icon").boundingBox();
        const icons = await group.boundingBox();
        const toggle = await activity.boundingBox();
        const bounds = await header.boundingBox();
        expect(Math.abs(icons.y + icons.height / 2 - project.y - project.height / 2)).toBeLessThan(1);
        expect(icons.x).toBeGreaterThanOrEqual(project.x + project.width);
        expect(toggle.x).toBeGreaterThanOrEqual(icons.x + icons.width);
        expect(toggle.x + toggle.width).toBeLessThanOrEqual(bounds.x + bounds.width);
        expect(await header.locator(".session-header-project").evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true);
        expect(Math.abs(bounds.height - emptyHeight)).toBeLessThanOrEqual(1);
        const track = await activity.locator(".session-header-view-toggle-track").boundingBox();
        expect(track.width).toBe(32);
        expect(await activity.locator(".session-header-view-toggle-track").evaluate((element) => {
          const bounds = element.getBoundingClientRect();
          return element.contains(document.elementFromPoint(bounds.x + bounds.width / 2, bounds.y + bounds.height / 2));
        })).toBe(true);
      };
      await checkLayout();
      await header.screenshot({ path: testInfo.outputPath("header-one-tag.png"), animations: "disabled" });
      if (!isMobile) {
        await icon.hover();
        await expect(page.getByRole("tooltip")).toHaveText(tags[0]);
        await icon.press("Tab");
        await page.keyboard.press("Shift+Tab");
        await expect(icon).toBeFocused();
        await expect(page.getByRole("tooltip")).toBeVisible();
        await page.keyboard.press("Escape");
        await expect(page.getByRole("tooltip")).toBeHidden();
      }
      await activate(icon);
      await expect.poll(() => new URL(page.url()).searchParams.get("tag")).toBe(tags[0]);
      await expect(header.locator(".session-header-name")).toHaveText(sessions.marker);
      expect(new URL(page.url()).searchParams.get("session")).toBe(session);
      for (const tag of tags.slice(1, 3)) await assign(tag, true);
      await page.clock.runFor(10_100);
      await expect(more).toHaveText("+1");
      await expect(more).toHaveAccessibleName("Edit all 3 tags");
      await checkLayout();
      await activate(activity);
      await expect(activity).toHaveAttribute("aria-checked", "false");
      await activate(activity);
      await expect(activity).toHaveAttribute("aria-checked", "true");
      await more.focus();
      const original = await more.elementHandle();
      await page.clock.runFor(10_100);
      expect(await original.evaluate((element) => element.isConnected)).toBe(true);
      await expect(more).toBeFocused();
      await assign(tags[3], true);
      await page.clock.runFor(10_100);
      await expect(more).toHaveText("+2");
      await expect(more).toHaveAccessibleName("Edit all 4 tags");
      await expect(more).toBeFocused();
      await activate(more);
      await dialog.getByRole("searchbox").fill("");
      for (const tag of tags) await expect(dialog.getByRole("checkbox", { name: tag, exact: true })).toBeChecked();
      await activate(dialog.getByRole("checkbox", { name: tags[3], exact: true }));
      await expect(more).toHaveText("+1");
      await activate(close);
      // A replaced overflow trigger falls back to the permanent title-row editor.
      await expect(edit).toBeFocused();
      await more.focus();
      await assign(tags[2], false);
      await page.clock.runFor(10_100);
      await expect(edit).toBeFocused();
      await expect(group.locator(".session-tag-icon")).toHaveCount(2);
      await checkLayout();
      await page.reload();
      await checkLayout();
      await expect(group.locator(".session-tag-icon")).toHaveCount(2);
      for (const tag of tags.slice(2)) await assign(tag, true);
      await page.clock.runFor(10_100);
      await expect(more).toHaveText("+2");
      await checkLayout();
      await header.screenshot({ path: testInfo.outputPath("header-overflow-tags.png"), animations: "disabled" });
      await header.locator(".session-header-project-label").evaluate((element) => {
        element.textContent = "customer_portal_frontend_feature_branch_integration_tests";
      });
      await checkLayout();
    } finally {
      for (const tag of tags) await assign(tag, false);
    }
  });
}
