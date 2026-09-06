import { expect, test } from "@playwright/test";
import { sessions } from "../support/contract.mjs";

async function fixtureSessions(page) {
  await page.goto("/?show_all_sessions=1");
  const result = {};
  for (const [key, name] of Object.entries({ current: sessions.marker, other: sessions.history, pin: sessions.prompt })) {
    const row = page.locator(".session-row").filter({ has: page.locator(".session-title", { hasText: name }) });
    result[key] = { path: await row.getAttribute("data-session-path"), project: await row.locator(".session-project").getAttribute("title"), name };
  }
  for (const session of [result.current, result.other]) {
    expect((await page.request.post("/sessions/pin", { form: { session: session.path, pinned: "false" } })).ok()).toBe(true);
  }
  return result;
}

async function assign(page, session, tag, assigned = true) {
  const response = await page.request.post("/sessions/tags", { form: { session: session.path, tag, assigned: String(assigned) } });
  expect(response.ok()).toBe(true);
}

const dialogFor = (page) => page.getByRole("dialog", { name: "Session tags", exact: true });

test("tag pickers stay beside desktop controls and fit narrow phone screens", async ({ page, isMobile }, testInfo) => {
  const { current } = await fixtureSessions(page);
  await page.setViewportSize(isMobile ? { width: 360, height: 640 } : { width: 1280, height: 800 });
  await page.goto(`/?session=${encodeURIComponent(current.path)}`);
  const edit = page.getByRole("button", { name: "Edit session tags", exact: true });
  if (isMobile) await edit.tap();
  else await edit.click();
  const dialog = dialogFor(page);
  await expect(dialog.getByRole("status")).toBeHidden();
  const bounds = await dialog.boundingBox();
  const control = await edit.boundingBox();
  const viewport = page.viewportSize();
  expect(bounds.x).toBeGreaterThanOrEqual(0);
  expect(bounds.x + bounds.width).toBeLessThanOrEqual(viewport.width);
  expect(bounds.y + bounds.height).toBeLessThanOrEqual(viewport.height);
  if (!isMobile) {
    expect(control.x + control.width / 2).toBeGreaterThanOrEqual(bounds.x);
    expect(control.x + control.width / 2).toBeLessThanOrEqual(bounds.x + bounds.width);
    expect(Math.abs(bounds.y - (control.y + control.height))).toBeLessThan(20);
  }
  await page.screenshot({ path: testInfo.outputPath("tag-picker-layout.png"), animations: "disabled" });
});

test("compact tag header keeps its editor on the title row through tag changes", async ({ page, isMobile }, testInfo) => {
  const activate = (control) => isMobile ? control.tap() : control.click();
  const { current } = await fixtureSessions(page);
  const tag = `compact-${testInfo.project.name}`;
  await page.setViewportSize(isMobile ? { width: 360, height: 640 } : { width: 1280, height: 800 });
  try {
    await page.goto(`/?session=${encodeURIComponent(current.path)}`);
    const header = page.locator(".session-header");
    const edit = header.locator("[data-tag-edit]");
    const chip = header.getByRole("button", { name: `Filter sessions by ${tag}`, exact: true });
    const expectTitleRowEditor = async () => {
      const title = await header.locator(".session-header-name").boundingBox();
      const control = await edit.boundingBox();
      expect(Math.abs(control.y + control.height / 2 - (title.y + title.height / 2))).toBeLessThanOrEqual(4);
      expect(control.x).toBeGreaterThanOrEqual(title.x + title.width - 1);
      expect(control.width).toBeLessThanOrEqual(44);
      expect(control.height).toBeLessThanOrEqual(44);
    };
    const emptyHeight = (await header.boundingBox()).height;
    await page.screenshot({ path: testInfo.outputPath("compact-header-empty.png"), animations: "disabled" });
    expect.soft(emptyHeight).toBeLessThanOrEqual(110);
    await expectTitleRowEditor();
    await expect(edit).toHaveAccessibleName("Edit session tags");
    await activate(edit);
    const dialog = dialogFor(page);
    const search = dialog.getByRole("searchbox", { name: "Find or create a tag" });
    await expect(search).toBeFocused();
    await search.fill(tag);
    await activate(dialog.getByRole("button", { name: `Create “${tag}”`, exact: true }));
    const checkbox = dialog.getByRole("checkbox", { name: tag, exact: true });
    await expect(checkbox).toBeChecked();
    await activate(dialog.getByRole("button", { name: "Close tag picker", exact: true }));
    await expect(dialog).toBeHidden();
    await expect(edit).toBeFocused();
    await expect(chip).toBeVisible();
    await expect.poll(async () => (await header.boundingBox()).height).toBeGreaterThan(emptyHeight);
    await expectTitleRowEditor();
    await expect(edit).toHaveAccessibleName("Edit session tags");
    await page.screenshot({ path: testInfo.outputPath("compact-header-tagged.png"), animations: "disabled" });

    await page.reload();
    await expect(chip).toBeVisible();
    await expectTitleRowEditor();
    await activate(edit);
    await expect(checkbox).toBeChecked();
    await activate(checkbox);
    await expect(checkbox).toHaveCount(0);
    await activate(dialog.getByRole("button", { name: "Close tag picker", exact: true }));
    await expect(dialog).toBeHidden();
    await expect(edit).toBeFocused();
    await expect(chip).toBeHidden();
    await expect.poll(async () => Math.abs((await header.boundingBox()).height - emptyHeight)).toBeLessThanOrEqual(1);
    await expectTitleRowEditor();
    await expect(edit).toHaveAccessibleName("Edit session tags");
    await page.reload();
    await expect(chip).toBeHidden();
    await expect.poll(async () => Math.abs((await header.boundingBox()).height - emptyHeight)).toBeLessThanOrEqual(1);
  } finally {
    await assign(page, current, tag, false);
  }
});

test("closing the current session overflow editor restores focus after a sidebar refresh", async ({ page, isMobile }, testInfo) => {
  const activate = (control) => isMobile ? control.tap() : control.click();
  const { current } = await fixtureSessions(page);
  const tags = ["one", "two", "three"].map((tag) => `overflow-focus-${tag}-${testInfo.project.name}`);
  for (const tag of tags) await assign(page, current, tag);
  try {
    await page.goto(`/?${new URLSearchParams({ session: current.path, tag: tags[0] })}`);
    if (isMobile) await activate(page.locator('label[aria-label="Open sessions"]'));
    const overflow = page.locator('.session-row[data-current="true"]').getByRole("button", { name: "Edit all 3 tags" });
    await activate(overflow);
    const dialog = dialogFor(page);
    const checkbox = dialog.getByRole("checkbox", { name: tags[0], exact: true });
    await expect(checkbox).toBeChecked();
    const original = await overflow.elementHandle();
    await activate(checkbox);
    await expect(checkbox).toHaveCount(0);
    await expect.poll(() => original.evaluate((element) => element.isConnected)).toBe(false);
    await activate(dialog.getByRole("button", { name: "Close tag picker", exact: true }));
    await expect(dialog).toBeHidden();
    await expect(page.getByRole("button", { name: "Edit session tags", exact: true })).toBeFocused();
  } finally {
    for (const tag of tags) await assign(page, current, tag, false);
  }
});

test("sidebar tag controls retain keyboard focus across polling and ArrowUp selects the last choice", async ({ page, isMobile }, testInfo) => {
  const { current } = await fixtureSessions(page);
  const tags = ["alpha", "beta", "gamma"].map((name) => `keyboard-${name}-${testInfo.project.name}`);
  for (const tag of tags) await assign(page, current, tag);
  try {
    await page.clock.install();
    await page.goto(`/?${new URLSearchParams({ session: current.path, tag: tags[0] })}`);
    if (isMobile) await page.locator('label[aria-label="Open sessions"]').tap();
    const row = page.locator('.sessions-list .session-row');
    for (const control of [row.locator('.tag-chip').first(), row.locator('.tag-overflow'), page.locator('[data-tag-chooser]'), page.getByRole('button', { name: 'Clear tag filter', exact: true })]) {
      await control.focus();
      const original = await control.elementHandle();
      await page.clock.runFor(10_100);
      await expect.poll(() => original.evaluate((element) => element.isConnected)).toBe(false);
      await expect(control).toBeFocused();
    }
    if (isMobile) await row.locator('.tag-overflow').tap();
    else await row.locator('.tag-overflow').click();
    const dialog = dialogFor(page);
    const search = dialog.getByRole('searchbox');
    await expect(dialog.getByRole('checkbox').last()).toBeVisible();
    await search.fill('');
    await search.press('ArrowUp');
    await expect(dialog.getByRole('checkbox').last()).toBeFocused();
  } finally {
    for (const tag of tags) await assign(page, current, tag, false);
  }
});

test("create, reuse and remove tags immediately with reload and keyboard access", async ({ page, isMobile }, testInfo) => {
  const activate = (control) => isMobile ? control.tap() : control.click();
  const { current, other } = await fixtureSessions(page);
  const tag = `review-${testInfo.project.name}`;
  await page.goto(`/?session=${encodeURIComponent(current.path)}`);
  await activate(page.getByRole("button", { name: "Edit session tags", exact: true }));
  const dialog = dialogFor(page);
  const search = dialog.getByRole("searchbox", { name: "Find or create a tag" });
  await expect(search).toBeFocused();
  await search.fill(`  ${tag.toUpperCase()}  `);
  await activate(dialog.getByRole("button", { name: `Create “${tag}”` }));
  const checkbox = dialog.getByRole("checkbox", { name: tag, exact: true });
  await expect(checkbox).toBeChecked();
  await expect(page.locator('.session-row[data-current="true"]').getByRole("button", { name: `Filter sessions by ${tag}`, exact: true })).toHaveCount(1);
  await expect(search).toHaveValue(`  ${tag.toUpperCase()}  `);
  await page.screenshot({ path: testInfo.outputPath("tag-editor.png"), animations: "disabled" });
  await page.keyboard.press("Escape");
  await expect(dialog).toBeHidden();
  await expect(page.getByRole("button", { name: "Edit session tags", exact: true })).toBeFocused();
  await page.reload();
  await expect(page.locator(".header-tags").getByRole("button", { name: `Filter sessions by ${tag}`, exact: true })).toBeVisible();

  await page.goto(`/?session=${encodeURIComponent(other.path)}`);
  await activate(page.getByRole("button", { name: "Edit session tags", exact: true }));
  await search.fill(tag.toUpperCase());
  if (isMobile) await activate(checkbox);
  else {
    await search.press("ArrowDown");
    await expect(checkbox).toBeFocused();
    await page.keyboard.press("Space");
  }
  await expect(checkbox).toBeChecked();
  if (!isMobile) await expect(checkbox).toBeFocused();
  await activate(checkbox);
  await expect(checkbox).not.toBeChecked();
  await activate(dialog.getByRole("button", { name: "Close tag picker" }));
  await page.reload();
  await expect(page.getByRole("button", { name: "Edit session tags", exact: true })).toBeVisible();
  await assign(page, current, tag, false);
});

test("tag filter combines across projects without switching conversation; pins bypass and compact clear preserves filters", async ({ page, isMobile }, testInfo) => {
  const activate = (control) => isMobile ? control.tap() : control.click();
  const { current, other, pin } = await fixtureSessions(page);
  const tag = `shared-${testInfo.project.name}`;
  await assign(page, current, tag);
  await assign(page, other, tag);
  expect((await page.request.post("/sessions/pin", { form: { session: pin.path, pinned: "true" } })).ok()).toBe(true);
  try {
    await page.goto(`/?session=${encodeURIComponent(current.path)}`);
    await page.getByLabel("Message to Pi").fill("Keep this draft");
    await activate(page.locator(".header-tags").getByRole("button", { name: `Filter sessions by ${tag}`, exact: true }));
    if (isMobile) await activate(page.locator('label[aria-label="Open sessions"]'));
    await expect.poll(() => new URL(page.url()).searchParams.get("tag")).toBe(tag);
    await expect(page.locator(".sessions-list .session-row")).toHaveCount(2);
    await expect(page.locator("[data-tag-filter-count]")).toHaveText("2");
    await expect(page.locator(".session-header-name")).toHaveText(current.name);
    await expect(page.getByLabel("Message to Pi")).toHaveValue("Keep this draft");
    await expect(page.locator(".pinned-sessions-list .session-row").filter({ has: page.locator(".session-title", { hasText: pin.name }) })).toHaveAttribute("data-session-path", pin.path);
    const chip = page.locator(".sessions-list .session-row").filter({ has: page.locator(`.session-title`, { hasText: other.name }) }).getByRole("button", { name: `Filter sessions by ${tag}`, exact: true });
    await activate(chip);
    await expect.poll(() => new URL(page.url()).searchParams.get("session")).toBe(current.path);
    await activate(page.getByRole("button", { name: "Filter sessions by tag", exact: true }));
    const chooser = page.getByRole("dialog", { name: "Filter by tag", exact: true });
    await expect(chooser.getByRole("button", { name: `${tag} 2`, exact: true })).toBeVisible();
    await page.keyboard.press("Escape");

    await page.goto(`/?${new URLSearchParams({ session: current.path, tag, project: other.project, session_search: "History" })}`);
    if (isMobile) await activate(page.locator('label[aria-label="Open sessions"]'));
    await expect(page.locator(".sessions-list .session-row")).toHaveAttribute("data-session-path", other.path);
    await expect(page.locator(".current-session-section .session-row")).toHaveAttribute("data-session-path", current.path);
    await activate(page.getByRole("button", { name: "Filter sessions by tag", exact: true }));
    await expect(chooser.getByRole("button", { name: `${tag} 2`, exact: true })).toBeVisible();
    await page.keyboard.press("Escape");
    await page.screenshot({ path: testInfo.outputPath("tag-filter.png"), animations: "disabled" });
    await activate(page.getByRole("button", { name: "Clear tag filter", exact: true }));
    await expect.poll(() => new URL(page.url()).searchParams.get("tag")).toBe(null);
    const filterLabel = page.locator(".tag-filter-toggle span").first();
    expect(await filterLabel.evaluate((label) => label.scrollWidth <= label.clientWidth)).toBe(true);
    expect(new URL(page.url()).searchParams.get("project")).toBe(other.project);
    expect(new URL(page.url()).searchParams.get("session_search")).toBe("History");
    await expect(page.locator(".sessions-list .session-row")).toHaveAttribute("data-session-path", other.path);
  } finally {
    await assign(page, current, tag, false);
    await assign(page, other, tag, false);
    await page.request.post("/sessions/pin", { form: { session: pin.path, pinned: "false" } });
  }
});

test("failed and pending tag writes stay honest and retain the editor search", async ({ page, isMobile }, testInfo) => {
  const activate = (control) => isMobile ? control.tap() : control.click();
  const { current, other } = await fixtureSessions(page);
  const tag = `retry-${testInfo.project.name}`;
  await assign(page, other, tag);
  await page.goto(`/?session=${encodeURIComponent(current.path)}`);
  await activate(page.getByRole("button", { name: "Edit session tags", exact: true }));
  const dialog = dialogFor(page);
  const search = dialog.getByRole("searchbox");
  await search.fill(tag);
  let release;
  const pending = new Promise((resolve) => { release = resolve; });
  await page.route("**/sessions/tags", async (route) => {
    await pending;
    await route.fulfill({ status: 503, json: { error: "Please retry this tag change" } });
  });
  const checkbox = dialog.getByRole("checkbox", { name: tag, exact: true });
  try {
    await activate(checkbox);
    await expect(checkbox).toBeDisabled();
    await expect(checkbox).not.toBeChecked();
    await expect(search).toHaveValue(tag);
    await page.keyboard.press("Escape");
    await expect(dialog).toBeHidden();
    await activate(page.getByRole("button", { name: "Edit session tags", exact: true }));
    await expect(checkbox).toBeDisabled();
    await expect(search).toHaveValue(tag);
  } finally {
    release();
    await page.unrouteAll({ behavior: "wait" });
  }
  await expect(dialog.getByRole("alert")).toHaveText("Please retry this tag change");
  await activate(dialog.getByRole("button", { name: "Retry", exact: true }));
  await expect(checkbox).toBeChecked();
  await expect(search).toHaveValue(tag);
  await activate(dialog.getByRole("button", { name: "Close tag picker" }));
  await page.reload();
  await expect(page.locator(".header-tags").getByRole("button", { name: `Filter sessions by ${tag}`, exact: true })).toBeVisible();
  await assign(page, current, tag, false);
  await assign(page, other, tag, false);
});

test("background actions and overflow edit tags without navigating, and stale editor loads cannot change another session", async ({ page, isMobile }, testInfo) => {
  const activate = (control) => isMobile ? control.tap() : control.click();
  const { current, other } = await fixtureSessions(page);
  const tags = ["alpha", "beta", "gamma"].map((tag) => `${tag}-${testInfo.project.name}`);
  for (const tag of tags) await assign(page, other, tag);
  try {
    await page.goto(`/?${new URLSearchParams({ session: current.path, session_search: "History" })}`);
    if (isMobile) await activate(page.locator('label[aria-label="Open sessions"]'));
    const row = page.locator(".sessions-list .session-row");
    await expect(row).toHaveAttribute("data-session-path", other.path);
    await expect(row.locator(".tag-chip")).toHaveCount(2);
    await activate(row.getByRole("button", { name: "Edit all 3 tags" }));
    const dialog = dialogFor(page);
    for (const tag of tags) await expect(dialog.getByRole("checkbox", { name: tag, exact: true })).toBeChecked();
    await activate(dialog.getByRole("button", { name: "Close tag picker" }));
    await expect(page.locator(".session-header-name")).toHaveText(current.name);
    await activate(row.getByRole("button", { name: `Session actions for ${other.name}` }));
    await activate(page.getByRole("menuitem", { name: "Tags…", exact: true }));
    await expect(dialog.getByRole("checkbox", { name: tags[0], exact: true })).toBeChecked();
    await activate(dialog.getByRole("button", { name: "Close tag picker" }));
    await activate(row.locator("a.session"));
    for (const tag of tags) await expect(page.locator(".header-tags").getByRole("button", { name: `Filter sessions by ${tag}`, exact: true })).toBeVisible();

    let release;
    const pending = new Promise((resolve) => { release = resolve; });
    let requested;
    const started = new Promise((resolve) => { requested = resolve; });
    await page.route("**/sessions/tags?*", async (route) => {
      if (new URL(route.request().url()).searchParams.get("session") !== other.path) return route.continue();
      const response = await route.fetch();
      requested();
      await pending;
      await route.fulfill({ response });
    });
    try {
      await activate(page.getByRole("button", { name: "Edit session tags", exact: true }));
      await started;
      await expect(dialog.getByRole("status")).toHaveText("Loading tags…");
      await page.keyboard.press("Escape");
      await page.goBack();
      await expect(page.locator(".session-header-name")).toHaveText(current.name);
      await activate(page.getByRole("button", { name: "Edit session tags", exact: true }));
      await expect(dialog.getByRole("checkbox", { name: tags[0], exact: true })).not.toBeChecked();
      release();
      await page.unrouteAll({ behavior: "wait" });
      await expect(dialog.locator("[data-tag-context]")).toHaveText(current.name);
      await expect(dialog.getByRole("checkbox", { name: tags[0], exact: true })).not.toBeChecked();
    } finally {
      release();
      await page.unrouteAll({ behavior: "wait" });
    }
  } finally {
    for (const tag of tags) await assign(page, other, tag, false);
  }
});

test("finishing a tag write does not cancel a newer filter request", async ({ page, isMobile }, testInfo) => {
  const activate = (control) => isMobile ? control.tap() : control.click();
  const { current, other } = await fixtureSessions(page);
  const filterTag = `existing-${testInfo.project.name}`;
  const addedTag = `pending-${testInfo.project.name}`;
  await assign(page, current, filterTag);
  await assign(page, other, addedTag);
  let releaseWrite;
  const writePending = new Promise((resolve) => { releaseWrite = resolve; });
  let releaseFilter;
  const filterPending = new Promise((resolve) => { releaseFilter = resolve; });
  let requestedFilter;
  const filterStarted = new Promise((resolve) => { requestedFilter = resolve; });
  try {
    await page.goto(`/?session=${encodeURIComponent(current.path)}`);
    await activate(page.getByRole("button", { name: "Edit session tags", exact: true }));
    await page.route("**/sessions/tags", async (route) => { await writePending; await route.continue(); });
    const dialog = dialogFor(page);
    await activate(dialog.getByRole("checkbox", { name: addedTag, exact: true }));
    await expect(dialog.getByRole("checkbox", { name: addedTag, exact: true })).toBeDisabled();
    await page.keyboard.press("Escape");
    await page.route("**/sidebar?*", async (route) => {
      if (new URL(route.request().url()).searchParams.get("tag") !== filterTag) return route.continue();
      requestedFilter();
      await filterPending;
      await route.continue();
    });
    await activate(page.locator(".header-tags").getByRole("button", { name: `Filter sessions by ${filterTag}`, exact: true }));
    await filterStarted;
    releaseWrite();
    await expect(page.locator(".header-tags").getByRole("button", { name: `Filter sessions by ${addedTag}`, exact: true })).toBeVisible();
    releaseFilter();
    await expect.poll(() => new URL(page.url()).searchParams.get("tag")).toBe(filterTag);
    await expect(page.locator(".sessions-list .session-row")).toHaveAttribute("data-session-path", current.path);
  } finally {
    releaseWrite();
    releaseFilter();
    await page.unrouteAll({ behavior: "wait" });
    await assign(page, current, filterTag, false);
    await assign(page, current, addedTag, false);
    await assign(page, other, addedTag, false);
  }
});

test("polling preserves focused chips and cancelled filtering leaves a newer editor open", async ({ page, isMobile }, testInfo) => {
  const activate = (control) => isMobile ? control.tap() : control.click();
  const { current } = await fixtureSessions(page);
  const tag = `focus-${testInfo.project.name}`;
  await assign(page, current, tag);
  let releaseFilter;
  const pending = new Promise((resolve) => { releaseFilter = resolve; });
  let requestedFilter;
  const started = new Promise((resolve) => { requestedFilter = resolve; });
  try {
    await page.clock.install();
    await page.goto(`/?session=${encodeURIComponent(current.path)}`);
    await page.clock.runFor(100);
    const chip = page.locator(".header-tags").getByRole("button", { name: `Filter sessions by ${tag}`, exact: true });
    await chip.focus();
    await page.evaluate(() => {
      window.sidebarTagRefreshes = 0;
      document.addEventListener("gripi:sidebar-tags", () => { window.sidebarTagRefreshes += 1; });
    });
    await page.clock.runFor(10_100);
    await expect.poll(() => page.evaluate(() => window.sidebarTagRefreshes)).toBeGreaterThan(0);
    await expect(chip).toBeFocused();
    await page.route("**/sidebar?*", async (route) => {
      if (new URL(route.request().url()).searchParams.get("tag") !== tag) return route.continue();
      requestedFilter();
      await pending;
      await route.continue();
    });
    await activate(chip);
    await started;
    await activate(page.getByRole("button", { name: "Edit session tags", exact: true }));
    const dialog = dialogFor(page);
    const search = dialog.getByRole("searchbox");
    await search.fill("Keep this editor search");
    releaseFilter();
    await page.unrouteAll({ behavior: "wait" });
    await expect(dialog).toBeVisible();
    await expect(search).toHaveValue("Keep this editor search");
    await expect(search).toBeFocused();
    await activate(dialog.getByRole("button", { name: "Close tag picker", exact: true }));
    await chip.focus();
    await assign(page, current, tag, false);
    await page.clock.runFor(10_100);
    await expect(page.getByRole("button", { name: "Edit session tags", exact: true })).toBeFocused();
  } finally {
    releaseFilter();
    await page.unrouteAll({ behavior: "wait" });
    await assign(page, current, tag, false);
  }
});
