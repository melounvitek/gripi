import { expect, test } from "@playwright/test";

const dialog = (page) => page.getByRole("dialog", { name: "New session", exact: true });
const input = (page) => dialog(page).getByRole("combobox", { name: "Project or path" });
const option = (page, name) => dialog(page).getByRole("option", { name, exact: true });
const activate = (control, mobile) => mobile ? control.tap() : control.click();

async function open(page, mobile) {
  await page.goto("/");
  if (mobile) await activate(page.locator('label[aria-label="Open sessions"]'), mobile);
  await activate(page.getByRole("button", { name: "New session", exact: true }), mobile);
  await expect(dialog(page)).toBeVisible();
}

async function expectStartedIn(page, project) {
  await expect(page.getByRole("heading", { level: 1, name: "New session (pending first assistant response)" })).toBeVisible();
  await expect(page.locator(".session-header-project-label")).toHaveText(project);
  // Leftover sessions push seeded ones off the first sidebar page for later tests.
  const deleted = await page.request.post("/sessions/delete", { form: { session: new URL(page.url()).searchParams.get("session") } });
  expect(deleted.ok()).toBe(true);
}

test("typing filters the projects and Enter starts in the highlighted one", async ({ page, isMobile }) => {
  test.skip(isMobile, "Keyboard flow");
  await open(page, isMobile);
  await expect(input(page)).toBeFocused();
  await expect(dialog(page).getByRole("option").first()).toHaveAttribute("aria-selected", "true");

  // The input is the only focus stop, so Tab never lands on a secondary control.
  for (const key of ["Tab", "Shift+Tab"]) {
    await page.keyboard.press(key);
    await expect(input(page)).toBeFocused();
  }
  await page.keyboard.press("ArrowDown");
  await expect(dialog(page).getByRole("option").nth(1)).toHaveAttribute("aria-selected", "true");

  await page.keyboard.type("SESSION-mob");
  await expect(dialog(page).getByRole("option")).toHaveText([/new-session-mobile/, /Add new path…/]);
  await page.keyboard.press("Enter");
  await expectStartedIn(page, "new-session-mobile");
});

test("the first activation of a project starts a session there", async ({ page, isMobile }) => {
  await open(page, isMobile);
  // On touch screens the keyboard stays down until the input is tapped.
  if (isMobile) await expect(input(page)).not.toBeFocused();
  const project = dialog(page).locator("[data-new-session-project]").first();
  const cwd = await project.getAttribute("data-new-session-project");
  await activate(project, isMobile);
  await expectStartedIn(page, cwd.split("/").pop());
});

test("a new path is browsed in the same list", async ({ page, isMobile }) => {
  await open(page, isMobile);
  await activate(option(page, "Add new path…"), isMobile);
  await expect(input(page)).toHaveValue(/\/$/);
  const parent = await input(page).inputValue();

  await input(page).fill(`${parent}new-session-d`);
  await activate(option(page, "new-session-desktop/"), isMobile);
  await expect(input(page)).toHaveValue(`${parent}new-session-desktop/`);
  const start = dialog(page).getByRole("option", { name: /^Start in .*new-session-desktop$/ });
  await expect(start).toHaveAttribute("aria-selected", "true");

  await activate(option(page, "Back to projects"), isMobile);
  await expect(input(page)).toHaveValue("");
  await expect(dialog(page).locator("[data-new-session-project]").first()).toBeVisible();

  await input(page).fill(`${parent}new-session-desktop`);
  await activate(start, isMobile);
  await expectStartedIn(page, "new-session-desktop");
});

test("a missing path is explained and cannot be started", async ({ page, isMobile }) => {
  test.skip(isMobile, "Keyboard flow");
  await open(page, isMobile);
  await input(page).fill("/gripi-e2e/missing");
  await expect(dialog(page).getByRole("status")).toHaveText("Path must be an existing directory.");
  await page.keyboard.press("Enter");
  await expect(input(page)).toHaveValue("/gripi-e2e/missing");
  await expect(dialog(page)).toBeVisible();
});
