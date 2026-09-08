import { expect, test } from "@playwright/test";
import { prompts, replies, sessions } from "../support/contract.mjs";
import { expectRunFinished, message, sendPrompt } from "../support/ui.mjs";

const newDialog = (page) => page.getByRole("dialog", { name: "New session", exact: true });
const picker = (page) => page.getByRole("dialog", { name: "New session tags", exact: true });
const activate = (control, mobile) => mobile ? control.tap() : control.click();

async function setup(page, testInfo) {
  await page.goto("/?show_all_sessions=1");
  const row = page.locator(".session-row").filter({ has: page.locator(".session-title", { hasText: sessions.marker }) });
  const session = await row.getAttribute("data-session-path");
  const tag = `draft-filter-${testInfo.project.name}`;
  const reusable = `draft-reuse-${testInfo.project.name}`;
  for (const name of [tag, reusable]) {
    expect((await page.request.post("/sessions/tags", { form: { session, tag: name, assigned: "true" } })).ok()).toBe(true);
  }
  await page.goto(`/?${new URLSearchParams({ session, tag })}`);
  return { session, tag, reusable };
}

async function openNew(page, mobile) {
  if (mobile && !(await page.locator("#mobile-session-toggle").isChecked())) {
    await activate(page.locator('label[aria-label="Open sessions"]'), mobile);
  }
  await activate(page.getByRole("button", { name: "New session", exact: true }), mobile);
  await expect(newDialog(page)).toBeVisible();
}

async function cleanup(page, session, tags) {
  for (const tag of tags) await page.request.post("/sessions/tags", { form: { session, tag, assigned: "false" } });
}

test("new-session tags are removable local drafts, with reusable tags, retry and nested keyboard focus", async ({ page, isMobile }, testInfo) => {
  const { session, tag, reusable } = await setup(page, testInfo);
  const created = `cancelled-${testInfo.project.name}`;
  const catalogBefore = await (await page.request.get("/tags")).json();
  const savedBefore = await (await page.request.get(`/sessions/tags?${new URLSearchParams({ session })}`)).json();
  const writes = [];
  page.on("request", (request) => {
    if (request.method() === "POST" && new URL(request.url()).pathname === "/sessions/tags") writes.push(request);
  });
  try {
    await openNew(page, isMobile);
    const modal = newDialog(page);
    const add = modal.getByRole("button", { name: "Add tag", exact: true });
    await expect(modal.getByRole("button", { name: /^Remove / })).toHaveCount(0);
    await page.route("**/tags", (route) => route.fulfill({ status: 503, json: { error: "Catalog unavailable" } }));
    await activate(add, isMobile);
    await expect(picker(page).getByRole("alert")).toHaveText("Catalog unavailable");
    await page.unroute("**/tags");
    await activate(picker(page).getByRole("button", { name: "Retry", exact: true }), isMobile);
    const search = picker(page).getByRole("searchbox");
    await search.fill(reusable.toUpperCase());
    await activate(picker(page).getByRole("checkbox", { name: reusable, exact: true }), isMobile);
    await expect(picker(page).getByRole("checkbox", { name: reusable, exact: true })).toBeChecked();
    await search.fill(`  ${created.toUpperCase()}  `);
    await activate(picker(page).getByRole("button", { name: `Create “${created}”` }), isMobile);
    await expect(picker(page).getByRole("checkbox", { name: created, exact: true })).toBeChecked();
    await search.press("Enter");
    await expect(picker(page)).toBeVisible();
    await search.focus();
    await page.keyboard.press("Tab");
    await expect(picker(page).getByRole("checkbox", { name: created, exact: true })).toBeFocused();
    await page.keyboard.press("Escape");
    await expect(picker(page)).toBeHidden();
    await expect(modal).toBeVisible();
    await expect(add).toBeFocused();
    for (const name of [reusable, created]) await expect(modal.getByRole("button", { name: `Remove ${name}`, exact: true })).toBeVisible();
    await page.keyboard.press("Shift+Tab");
    await expect(modal.getByRole("button", { name: `Remove ${created}`, exact: true })).toBeFocused();
    await page.keyboard.press("Tab");
    await page.keyboard.press("Tab");
    await expect(modal.getByRole("button", { name: "Start session" })).toBeFocused();
    await page.screenshot({ path: testInfo.outputPath("new-session-draft-tags.png"), animations: "disabled" });
    await activate(modal.getByRole("button", { name: `Remove ${created}`, exact: true }), isMobile);
    await expect(add).toBeFocused();
    await page.keyboard.press("Escape");
    await expect(modal).toBeHidden();
    expect(writes).toHaveLength(0);
    expect(await (await page.request.get("/tags")).json()).toEqual(catalogBefore);
    expect(await (await page.request.get(`/sessions/tags?${new URLSearchParams({ session })}`)).json()).toEqual(savedBefore);
    await openNew(page, isMobile);
    await expect(modal.getByRole("button", { name: /^Remove / })).toHaveCount(0);
    await activate(add, isMobile);
    await expect(search).toHaveValue("");
    await expect(picker(page).getByRole("checkbox", { name: tag, exact: true })).not.toBeChecked();
    await page.keyboard.press("Escape");
    await page.keyboard.press("Escape");
    if (isMobile && !(await page.locator("#mobile-session-toggle").isChecked())) await activate(page.locator('label[aria-label="Open sessions"]'), isMobile);
    await activate(page.getByRole("button", { name: "Clear tag filter", exact: true }), isMobile);
    await expect.poll(() => new URL(page.url()).searchParams.get("tag")).toBe(null);
    await openNew(page, isMobile);
    await expect(modal.getByRole("button", { name: /^Remove / })).toHaveCount(0);
  } finally {
    await cleanup(page, session, [tag, reusable]);
  }
});

test("explicit draft tags survive first response and reload while the URL tag remains a separate filter", async ({ page, isMobile }, testInfo) => {
  const { session, tag, reusable } = await setup(page, testInfo);
  const created = `created-${testInfo.project.name}`;
  try {
    await openNew(page, isMobile);
    const modal = newDialog(page);
    await activate(modal.getByRole("combobox", { name: "Project" }), isMobile);
    await activate(page.getByRole("option", { name: new RegExp(`new-session-${testInfo.project.name}`) }), isMobile);
    await activate(modal.getByRole("button", { name: "Add tag", exact: true }), isMobile);
    await activate(picker(page).getByRole("checkbox", { name: reusable, exact: true }), isMobile);
    await picker(page).getByRole("searchbox").fill(created);
    await activate(picker(page).getByRole("button", { name: `Create “${created}”` }), isMobile);
    await activate(picker(page).getByRole("button", { name: "Close tag picker" }), isMobile);
    await activate(modal.getByRole("button", { name: "Start session" }), isMobile);
    await expect(modal).toBeHidden();
    await expect(page.locator(".header-tags .tag-chip")).toHaveText([created, reusable]);
    expect(new URL(page.url()).searchParams.get("tag")).toBe(tag);
    await sendPrompt(page, prompts.newSession);
    await expect(message(page, "assistant", replies.newSession)).toBeVisible();
    await expectRunFinished(page);
    await page.reload();
    await expect(page.locator(".header-tags .tag-chip")).toHaveText([created, reusable]);
    expect(new URL(page.url()).searchParams.get("tag")).toBe(tag);
    const createdSession = new URL(page.url()).searchParams.get("session");
    expect((await (await page.request.get(`/sessions/tags?${new URLSearchParams({ session: createdSession })}`)).json()).tags).toEqual([created, reusable]);
  } finally {
    await cleanup(page, session, [tag, reusable]);
  }
});

test("starting without selecting tags creates an untagged session and navigation keeps subsequent drafts empty", async ({ page, isMobile }, testInfo) => {
  const { session, tag, reusable } = await setup(page, testInfo);
  try {
    await openNew(page, isMobile);
    const modal = newDialog(page);
    await activate(modal.getByRole("button", { name: "Start session" }), isMobile);
    await expect(modal).toBeHidden();
    await expect(page.locator(".session-header-name")).toHaveText("New session (pending first assistant response)");
    expect(new URL(page.url()).searchParams.get("tag")).toBe(tag);
    const createdSession = new URL(page.url()).searchParams.get("session");
    expect((await (await page.request.get(`/sessions/tags?${new URLSearchParams({ session: createdSession })}`)).json()).tags || []).toEqual([]);
    await openNew(page, isMobile);
    await expect(modal.getByRole("button", { name: /^Remove / })).toHaveCount(0);
    await page.keyboard.press("Escape");
    await page.goBack();
    await expect(page.locator(".session-header-name")).toHaveText(sessions.marker);
    await openNew(page, isMobile);
    await expect(modal.getByRole("button", { name: /^Remove / })).toHaveCount(0);
  } finally {
    await cleanup(page, session, [tag, reusable]);
  }
});
