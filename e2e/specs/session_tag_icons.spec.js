import { expect, test } from "@playwright/test";
import { sessions } from "../support/contract.mjs";

for (const width of [320, 393, 1440]) {
  test(`inline tag icons fit ${width}px and filter on first activation without navigating`, async ({ page, isMobile }, testInfo) => {
    if (isMobile && width === 1440) test.skip();
    await page.setViewportSize({ width, height: 900 });
    await page.goto("/?show_all_sessions=1");
    const current = await page.locator(".session-row").filter({ hasText: sessions.marker }).getAttribute("data-session-path");
    const other = await page.locator(".session-row").filter({ hasText: sessions.history }).getAttribute("data-session-path");
    const tags = ["a".repeat(64), "b".repeat(64), "c".repeat(64)];
    const assign = async (tag, assigned) => {
      expect((await page.request.post("/sessions/tags", { form: { session: other, tag, assigned: String(assigned) } })).ok()).toBe(true);
    };
    try {
      for (const tag of tags) await assign(tag, true);
      await page.goto(`/?${new URLSearchParams({ session: current, session_search: "History" })}`);
      const openSidebar = async () => {
        if (width > 760) return;
        const toggle = page.locator('label[aria-label="Open sessions"]');
        if (isMobile) await toggle.tap();
        else await toggle.click();
        await expect(page.locator(".session-sidebar")).toHaveCSS("transform", "matrix(1, 0, 0, 1, 0, 0)");
      };
      await openSidebar();
      const row = page.locator(".sessions-list .session-row");
      const icon = row.getByRole("button", { name: `Filter sessions by ${tags[0]}`, exact: true });
      const otherIcon = row.getByRole("button", { name: `Filter sessions by ${tags[1]}`, exact: true });
      const overflow = row.getByRole("button", { name: "Edit all 3 tags", exact: true });
      await expect(icon.locator("svg")).toBeVisible();
      await expect(otherIcon.locator("svg")).toBeVisible();
      await expect(overflow).toBeVisible();
      expect(await icon.evaluate((element) => element.closest("a"))).toBeNull();
      const checkLayout = async () => {
        const project = await row.locator(".session-project").boundingBox();
        const first = await icon.boundingBox();
        const last = await overflow.boundingBox();
        const bounds = await row.boundingBox();
        expect(first.x).toBeGreaterThanOrEqual(project.x + project.width);
        expect(Math.abs(first.y + first.height / 2 - project.y - project.height / 2)).toBeLessThan(2);
        expect(last.x + last.width).toBeLessThanOrEqual(bounds.x + bounds.width);
        const pin = await row.locator(".session-pin-toggle").boundingBox();
        const actions = await row.locator(".session-actions-toggle").boundingBox();
        expect(Math.min(pin.y, actions.y)).toBeGreaterThanOrEqual(Math.max(first.y + first.height, last.y + last.height));
        expect(await row.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true);
        if (isMobile) {
          expect(first.width).toBeCloseTo(44, 2);
          expect(first.height).toBeCloseTo(44, 2);
        }
      };
      await checkLayout();
      // Even an unexpectedly long project name must leave all controls accessible.
      await row.locator(".session-project-label").evaluate((element) => { element.textContent = "long-project-".repeat(8); });
      await checkLayout();
      const label = page.getByRole("tooltip");
      if (!isMobile) {
        await icon.hover();
        await expect(label).toHaveText(tags[0]);
        await expect(label).toBeVisible();
        const bounds = await label.boundingBox();
        expect(bounds.x).toBeGreaterThanOrEqual(0);
        expect(bounds.x + bounds.width).toBeLessThanOrEqual(width);
        // The label remains readable when moving the pointer onto it.
        await label.hover();
        await expect(label).toBeVisible();
        await page.mouse.move(width - 1, 0);
        await icon.focus();
        await expect(label).toBeVisible();
        await page.keyboard.press("Escape");
        await expect(label).toBeHidden();

        const scroller = page.locator(".session-sidebar-content");
        const oldStyle = await scroller.getAttribute("style");
        await scroller.evaluate((element) => { element.style.maxHeight = "180px"; });
        await row.scrollIntoViewIfNeeded();
        await icon.hover();
        await expect(label).toBeVisible();
        const tooltip = await label.boundingBox();
        expect(tooltip.y).toBeGreaterThanOrEqual(0);
        expect(tooltip.y + tooltip.height).toBeLessThanOrEqual(page.viewportSize().height);
        expect(await label.evaluate((element) => {
          const bounds = element.getBoundingClientRect();
          return document.elementFromPoint(bounds.x + bounds.width / 2, bounds.bottom - 2) === element;
        })).toBe(true);
        await scroller.evaluate((element, style) => {
          if (style === null) element.removeAttribute("style");
          else element.setAttribute("style", style);
        }, oldStyle);
        await icon.hover();
      }
      await page.locator(".session-sidebar").screenshot({ path: testInfo.outputPath("inline-tag-icons.png"), animations: "disabled" });
      if (isMobile) await icon.tap();
      else await icon.press("Enter");
      await expect.poll(() => new URL(page.url()).searchParams.get("tag")).toBe(tags[0]);
      await expect(page.locator(".session-header-name")).toHaveText(sessions.marker);
      expect(new URL(page.url()).searchParams.get("session")).toBe(current);
      await expect(row).toHaveAttribute("data-session-path", other);
      // A live sidebar replacement and a reload preserve the same icon layout.
      await checkLayout();
      await page.reload();
      await openSidebar();
      await expect(icon.locator("svg")).toBeVisible();
      await checkLayout();
      // The stretched link must retain native title, path, and status hover information.
      const hoveredTitle = (selector) => row.locator(selector).evaluate((element) => {
        const bounds = element.getBoundingClientRect();
        return document.elementFromPoint(bounds.x + 5, bounds.y + bounds.height / 2)?.closest("[title]")?.getAttribute("title");
      });
      expect(await hoveredTitle(".session-title")).toBe(sessions.history);
      expect(await hoveredTitle(".session-project")).toBe(await row.locator(".session-project").getAttribute("title"));
      await row.locator(".session-indicators").evaluate((element) => {
        const indicator = document.createElement("span");
        indicator.title = "Pi is working";
        indicator.textContent = "•";
        element.append(indicator);
      });
      expect(await hoveredTitle(".session-indicators [title]")).toBe("Pi is working");
      // The project line still navigates via the row link, not a nested button/link.
      const project = await row.locator(".session-project").boundingBox();
      if (isMobile) await page.touchscreen.tap(project.x + 5, project.y + project.height / 2);
      else await page.mouse.click(project.x + 5, project.y + project.height / 2);
      await expect(page.locator(".session-header-name")).toHaveText(sessions.history);
    } finally {
      for (const tag of tags) await assign(tag, false);
    }
  });
}
