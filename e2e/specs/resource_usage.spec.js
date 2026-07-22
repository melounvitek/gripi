import { expect, test } from "@playwright/test";

test("shows systemctl cgroup memory with inactive file cache context", async ({ page }) => {
  await page.route("**/resource-usage", (route) => route.fulfill({
    contentType: "application/json",
    body: JSON.stringify({
      supported: true,
      memoryBytes: 2147483648,
      workingSetBytes: 1610612736,
      inactiveFileBytes: 536870912,
      cpuUsageUsec: 1000000,
      gatewayRssBytes: 1342177280,
      piRssBytes: 375390208,
      piProcessCount: 2
    })
  }));
  await page.goto("/");

  const usage = page.locator("[data-resource-usage]");
  await expect(usage).toBeVisible();
  await expect(usage.locator("[data-resource-usage-total]")).toContainText("RAM 2 GB");
  await expect(usage.locator("[data-resource-usage-breakdown]")).toHaveText("Gateway 1.25 GB · Pi 358 MB (2) · inactive file cache 512 MB");
  await expect(usage).toHaveAttribute("title", /Raw cgroup memory matching systemctl/);
});
