import { expect, test } from "@playwright/test";
import { sessions } from "../support/contract.mjs";

const tags = ["color-7", "color-8", "color-9", "color-35", "color-34", "color-0", "color-1", "color-é", "color-3", "color-東京", "color-5", "color-🧪"];

async function colors(control) {
  return control.evaluate((element) => {
    const style = getComputedStyle(element);
    return { foreground: style.color, background: style.backgroundColor };
  });
}

async function expectColors(control, expected) {
  await expect(control).toHaveCSS("color", expected.foreground);
  await expect(control).toHaveCSS("background-color", expected.background);
}

test("tag text selects the same palette colors in server and live surfaces, including Unicode", async ({ page }, testInfo) => {
  test.setTimeout(60_000);
  await page.goto("/?show_all_sessions=1");
  const paths = [];
  for (const name of [sessions.marker, sessions.history]) {
    paths.push(await page.locator(".session-row").filter({ has: page.locator(".session-title", { hasText: name }) }).getAttribute("data-session-path"));
  }
  const assign = async (session, tag, assigned) => {
    expect((await page.request.post("/sessions/tags", { form: { session, tag, assigned: String(assigned) } })).ok()).toBe(true);
  };
  const headerChip = (tag) => page.locator(".header-tags").getByRole("button", { name: `Filter sessions by ${tag}`, exact: true });
  const sidebarChip = (tag) => page.locator('.session-row[data-current="true"]').getByRole("button", { name: `Filter sessions by ${tag}`, exact: true });
  try {
    for (const tag of tags) await assign(paths[0], tag, true);
    await page.goto(`/?${new URLSearchParams({ session: paths[0] })}`);
    const expected = {};
    for (const tag of tags) expected[tag] = await colors(headerChip(tag));
    expect(new Set(Object.values(expected).map((pair) => pair.foreground)).size).toBe(12);
    for (const tag of ["color-0", "color-1"]) await expectColors(sidebarChip(tag), expected[tag]);

    await page.goto(`/?${new URLSearchParams({ session: paths[1] })}`);
    await page.getByRole("button", { name: "Edit session tags", exact: true }).click();
    const editor = page.getByRole("dialog", { name: "Session tags", exact: true });
    for (const tag of tags) {
      const checkbox = editor.getByRole("checkbox", { name: tag, exact: true });
      await expectColors(editor.locator(".tag-picker-option").filter({ has: page.getByRole("checkbox", { name: tag, exact: true }) }), expected[tag]);
      await checkbox.check();
      await expect(checkbox).toBeChecked();
      await expectColors(headerChip(tag), expected[tag]);
    }
    await editor.getByRole("button", { name: "Close tag picker" }).click();
    await page.screenshot({ path: testInfo.outputPath("tag-colors.png"), animations: "disabled" });
    await page.reload();
    for (const tag of tags) await expectColors(headerChip(tag), expected[tag]);
    for (const tag of ["color-0", "color-1"]) await expectColors(sidebarChip(tag), expected[tag]);

    const selected = "color-🧪";
    await headerChip(selected).click();
    const filter = page.getByRole("button", { name: "Filter sessions by tag", exact: true });
    await expect(filter).toContainText(selected);
    await expect(filter).toHaveCSS("color", expected[selected].foreground);
    await expect(page.locator(".compact-tag-filter")).toHaveCSS("background-color", expected[selected].background);
    await filter.click();
    const chooser = page.getByRole("dialog", { name: "Filter by tag", exact: true });
    for (const tag of tags) await expectColors(chooser.locator(`[data-tag-option="${tag}"]`), expected[tag]);
    await chooser.getByRole("button", { name: "Close tag picker" }).click();

    await page.getByRole("button", { name: "New session", exact: true }).click();
    const draft = page.getByRole("dialog", { name: "New session", exact: true });
    await expectColors(draft.getByRole("button", { name: `Remove ${selected}`, exact: true }), expected[selected]);
    await draft.getByRole("button", { name: "Add tag", exact: true }).click();
    const picker = page.getByRole("dialog", { name: "New session tags", exact: true });
    for (const tag of tags.filter((tag) => tag !== selected)) {
      await picker.getByRole("checkbox", { name: tag, exact: true }).check();
      await expectColors(draft.getByRole("button", { name: `Remove ${tag}`, exact: true }), expected[tag]);
    }
    // A not-yet-saved tag must get its color without a server round trip.
    await picker.getByRole("searchbox").fill("color-éx");
    const create = picker.getByRole("button", { name: "Create “color-éx”", exact: true });
    await expectColors(create, expected["color-東京"]);
    await create.click();
    await expectColors(draft.getByRole("button", { name: "Remove color-éx", exact: true }), expected["color-東京"]);
  } finally {
    for (const session of paths) for (const tag of tags) await assign(session, tag, false);
  }
});
