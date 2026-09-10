import { expect, test } from "@playwright/test";
import { sessions } from "../support/contract.mjs";

const tags = ["color-a", "color-b", "color-c", "color-d", "color-e", "color-f", "color-g", "color-é", "__proto__", "color-東京", "constructor", "color-🧪"];

function colors(hex) {
  expect(hex).toMatch(/^#[0-9a-f]{6}$/i);
  const rgb = [1, 3, 5].map((offset) => parseInt(hex.slice(offset, offset + 2), 16)).join(", ");
  return { foreground: `rgb(${rgb})`, background: `rgba(${rgb}, 0.12)` };
}

async function expectColors(control, expected) {
  await expect(control).toHaveCSS("color", expected.foreground);
  await expect(control).toHaveCSS("background-color", expected.background);
}

async function assign(page, session, tag, assigned = true) {
  const response = await page.request.post("/sessions/tags", { form: { session, tag, assigned: String(assigned) } });
  expect(response.ok()).toBe(true);
  return response.json();
}

const headerChip = (page, tag) => page.locator(".header-tags").getByRole("button", { name: `Filter sessions by ${tag}`, exact: true });
const pickerOption = (picker, tag) => picker.getByRole("checkbox", { name: tag, exact: true }).locator("..");

async function fixturePaths(page) {
  await page.goto("/?show_all_sessions=1");
  const paths = [];
  for (const name of [sessions.marker, sessions.history]) {
    paths.push(await page.locator(".session-row").filter({ has: page.locator(".session-title", { hasText: name }) }).getAttribute("data-session-path"));
  }
  return paths;
}

test("server-assigned tag colors agree across SSR, live, reload, picker, filter and drafts", async ({ page }, testInfo) => {
  test.setTimeout(60_000);
  const paths = await fixturePaths(page);
  const sidebarChip = (tag) => page.locator('.session-row[data-current="true"]').getByRole("button", { name: `Filter sessions by ${tag}`, exact: true });
  try {
    const expected = new Map();
    for (const tag of tags) {
      const payload = await assign(page, paths[0], tag);
      expect(payload.tag_colors).toBeDefined();
      expected.set(tag, colors(payload.tag_colors[tag]));
    }
    // Other specs may reserve palette entries first; assignments, not hashes or uniqueness, are authoritative.
    for (const url of ["/tags", `/sessions/tags?${new URLSearchParams({ session: paths[0] })}`]) {
      const payload = await (await page.request.get(url)).json();
      for (const tag of tags) expect(colors(payload.tag_colors[tag])).toEqual(expected.get(tag));
    }
    await page.goto(`/?${new URLSearchParams({ session: paths[0] })}`);
    for (const tag of tags) await expectColors(headerChip(page, tag), expected.get(tag));
    for (const tag of [...tags].sort().slice(0, 2)) await expect(sidebarChip(tag)).toHaveCSS("color", expected.get(tag).foreground);

    await page.goto(`/?${new URLSearchParams({ session: paths[1] })}`);
    await page.getByRole("button", { name: "Edit session tags", exact: true }).click();
    const editor = page.getByRole("dialog", { name: "Session tags", exact: true });
    for (const tag of tags) {
      const checkbox = editor.getByRole("checkbox", { name: tag, exact: true });
      await expectColors(pickerOption(editor, tag), expected.get(tag));
      await checkbox.check();
      await expect(checkbox).toBeChecked();
      await expectColors(headerChip(page, tag), expected.get(tag));
    }
    await editor.getByRole("button", { name: "Close tag picker" }).click();
    await page.screenshot({ path: testInfo.outputPath("tag-colors.png"), animations: "disabled" });
    await page.reload();
    for (const tag of tags) await expectColors(headerChip(page, tag), expected.get(tag));
    for (const tag of [...tags].sort().slice(0, 2)) await expect(sidebarChip(tag)).toHaveCSS("color", expected.get(tag).foreground);

    const selected = "color-🧪";
    await headerChip(page, selected).click();
    const filter = page.getByRole("button", { name: "Filter sessions by tag", exact: true });
    await expect(filter).toContainText(selected);
    await expect(filter).toHaveCSS("color", expected.get(selected).foreground);
    await expect(page.locator(".compact-tag-filter")).toHaveCSS("background-color", expected.get(selected).background);
    await filter.click();
    const chooser = page.getByRole("dialog", { name: "Filter by tag", exact: true });
    for (const tag of tags) await expectColors(chooser.locator(`[data-tag-option="${tag}"]`), expected.get(tag));
    await chooser.getByRole("button", { name: "Close tag picker" }).click();

    await page.getByRole("button", { name: "New session", exact: true }).click();
    const draft = page.getByRole("dialog", { name: "New session", exact: true });
    await draft.getByRole("button", { name: "Add tag", exact: true }).click();
    const picker = page.getByRole("dialog", { name: "New session tags", exact: true });
    for (const tag of tags) {
      await picker.getByRole("checkbox", { name: tag, exact: true }).check();
      await expectColors(draft.getByRole("button", { name: `Remove ${tag}`, exact: true }), expected.get(tag));
    }
    // Unsaved names do not reserve a palette entry.
    await picker.getByRole("searchbox").fill("color-éx");
    const create = picker.getByRole("button", { name: "Create “color-éx”", exact: true });
    await expectColors(create, colors("#a0a0a0"));
    await create.click();
    await expectColors(draft.getByRole("button", { name: "Remove color-éx", exact: true }), colors("#a0a0a0"));
    const unsaved = await (await page.request.get("/tags")).json();
    expect(Object.hasOwn(unsaved.tag_colors, "color-éx")).toBe(false);
  } finally {
    for (const session of paths) for (const tag of tags) await assign(page, session, tag, false);
  }
});

test("session-only HTML initializes colors, swapped HTML is reingested, and new saves recolor immediately", async ({ page }) => {
  const paths = await fixturePaths(page);
  const names = ["color-initial", "color-swapped", "color-live"];
  const editor = page.getByRole("dialog", { name: "Session tags", exact: true });
  const search = editor.getByRole("searchbox");
  const open = () => page.getByRole("button", { name: "Edit session tags", exact: true }).click();
  const close = () => editor.getByRole("button", { name: "Close tag picker" }).click();
  try {
    const initial = await assign(page, paths[0], names[0]);
    await page.goto(`/?${new URLSearchParams({ session: paths[0], session_only: "1" })}`);
    await expect(page.locator(".session-sidebar")).toHaveCount(0);
    await expectColors(headerChip(page, names[0]), colors(initial.tag_colors[names[0]]));
    // With the API unavailable, a known-name preview must use the HTML map.
    await page.route("**/sessions/tags?*", (route) => route.abort());
    await open();
    await expect(editor.getByRole("alert")).toBeVisible();
    await search.fill(names[0]);
    await expectColors(editor.locator(".tag-create"), colors(initial.tag_colors[names[0]]));
    await close();

    // Allocate after initialization so only the newly swapped header can supply this color.
    const swapped = await assign(page, paths[1], names[1]);
    await page.evaluate((session) => {
      window.colorSwapSentinel = true;
      history.pushState({}, "", `/?${new URLSearchParams({ session, session_only: "1" })}`);
      dispatchEvent(new PopStateEvent("popstate"));
    }, paths[1]);
    await expect(page.locator(".header-tags")).toHaveAttribute("data-tag-session", paths[1]);
    expect(await page.evaluate(() => window.colorSwapSentinel)).toBe(true);
    await open();
    await expect(editor.getByRole("alert")).toBeVisible();
    await search.fill(names[1]);
    await expectColors(editor.locator(".tag-create"), colors(swapped.tag_colors[names[1]]));
    await page.unroute("**/sessions/tags?*");
    await editor.getByRole("button", { name: "Retry", exact: true }).click();
    await expectColors(pickerOption(editor, names[1]), colors(swapped.tag_colors[names[1]]));

    await search.fill(names[2]);
    const create = editor.getByRole("button", { name: `Create “${names[2]}”`, exact: true });
    await expectColors(create, colors("#a0a0a0"));
    const savedResponse = page.waitForResponse((response) => response.url().endsWith("/sessions/tags") && response.request().method() === "POST");
    await create.click();
    const saved = await (await savedResponse).json();
    const expected = colors(saved.tag_colors[names[2]]);
    await expectColors(headerChip(page, names[2]), expected);
    await expectColors(pickerOption(editor, names[2]), expected);
    await close();
    await page.reload();
    await expectColors(headerChip(page, names[2]), expected);
  } finally {
    await page.unrouteAll({ behavior: "wait" });
    for (const session of paths) for (const tag of names) await assign(page, session, tag, false);
  }
});

test("sidebar refresh imports new colors and recolors unchanged header chips without losing focus", async ({ page }) => {
  const [session] = await fixturePaths(page);
  const tag = "color-polled";
  try {
    await page.clock.install();
    await page.goto(`/?${new URLSearchParams({ session })}`);
    const payload = await assign(page, session, tag);
    const expected = colors(payload.tag_colors[tag]);
    await page.clock.runFor(10_100);
    const chip = headerChip(page, tag);
    await expectColors(chip, expected);
    await chip.focus();
    const original = await chip.elementHandle();
    await chip.evaluate((element) => {
      element.style.setProperty("--tag-fg", "#a0a0a0");
      element.style.setProperty("--tag-bg", "#a0a0a01f");
      document.dispatchEvent(new CustomEvent("gripi:sidebar-tags"));
    });
    await expectColors(chip, expected);
    expect(await original.evaluate((element) => element.isConnected)).toBe(true);
    await expect(chip).toBeFocused();
  } finally {
    await assign(page, session, tag, false);
  }
});
