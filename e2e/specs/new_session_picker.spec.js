import { expect, test } from "@playwright/test";
import { mkdtemp, rm } from "node:fs/promises";
import path from "node:path";

const dialog = (page) => page.getByRole("dialog", { name: "New session", exact: true });
const input = (page) => dialog(page).getByRole("combobox");
const label = (page) => dialog(page).locator("[data-new-session-label]");
const option = (page, name) => dialog(page).getByRole("option", { name, exact: true });
const folders = (page) => dialog(page).locator('[data-new-session-action="folder"]');
const activate = (control, mobile) => mobile ? control.tap() : control.click();
// "Other folder…" starts in the folder the first project sits in.
const projectsFolder = async (page) => path.dirname(await dialog(page).locator("[data-new-session-project]").first().getAttribute("data-new-session-project"));

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

  // The path is searched as well as the name.
  await page.keyboard.type("projects/NEW-SESSION-mob");
  await expect(dialog(page).getByRole("option")).toHaveText([/new-session-mobile/, /Other folder…/]);
  await expect(dialog(page).locator(`#${await input(page).getAttribute("aria-activedescendant")}`)).toHaveText(/new-session-mobile/);
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

test("another folder is picked from the list beside the projects", async ({ page, isMobile }) => {
  await open(page, isMobile);
  const where = await projectsFolder(page);
  await activate(option(page, "Other folder…"), isMobile);
  await expect(label(page)).toHaveText(`Folders in ${where}:`);
  // The listed folder itself can be started in, but is never the preselected row.
  await expect(folders(page).first()).toHaveAttribute("aria-selected", "true");
  await expect(option(page, `Start in ${where} itself`)).toHaveAttribute("aria-selected", "false");

  await activate(option(page, "Back to projects"), isMobile);
  await expect(label(page)).toHaveText("Project or path:");
  await expect(dialog(page).locator("[data-new-session-project]").first()).toBeVisible();

  await activate(option(page, "Other folder…"), isMobile);
  await activate(folders(page).filter({ hasText: "new-session-desktop" }), isMobile);
  await expectStartedIn(page, "new-session-desktop");
});

test("a typed path lists its folder and filters by the rest", async ({ page, isMobile }) => {
  test.skip(isMobile, "Keyboard flow");
  await open(page, isMobile);
  const where = await projectsFolder(page);
  await page.keyboard.type(`${where}/NEW-session-m`);
  await expect(label(page)).toHaveText(`Folders in ${where}:`);
  await expect(input(page)).toHaveValue("NEW-session-m");
  await expect(folders(page)).toHaveText([/new-session-mobile/]);
  await page.keyboard.press("Enter");
  await expectStartedIn(page, "new-session-mobile");
});

test("a folder that does not exist is explained", async ({ page, isMobile }) => {
  test.skip(isMobile, "Keyboard flow");
  await open(page, isMobile);
  await input(page).fill("/gripi-e2e-missing/");
  await expect(label(page)).toHaveText("Folders in /gripi-e2e-missing:");
  await expect(dialog(page).getByRole("status")).toHaveText("Path must be an existing directory.");
});

test("hidden folders are listed once the filter starts with a dot", async ({ page, isMobile }) => {
  test.skip(isMobile, "Not specific to touch");
  await open(page, isMobile);
  const hidden = await mkdtemp(path.join(await projectsFolder(page), ".picker-hidden-"));
  try {
    await activate(option(page, "Other folder…"), isMobile);
    await expect(folders(page).first()).toBeVisible();
    await expect(folders(page).filter({ hasText: path.basename(hidden) })).toHaveCount(0);
    await input(page).fill(".picker-h");
    await expect(folders(page).locator(".new-session-name")).toHaveText([path.basename(hidden)]);
  } finally {
    await rm(hidden, { recursive: true, force: true });
  }
});

test("a folder is looked inside and left again before starting", async ({ page, isMobile }) => {
  await open(page, isMobile);
  const where = await projectsFolder(page);
  await activate(option(page, "Other folder…"), isMobile);
  await activate(option(page, `Up to ${path.dirname(where)}`), isMobile);
  await expect(label(page)).toHaveText(`Folders in ${path.dirname(where)}:`);

  // The mark at the end of a row is its own target: it looks inside instead of starting.
  await activate(folders(page).filter({ hasText: "projects" }).locator("[data-new-session-inside]"), isMobile);
  await expect(label(page)).toHaveText(`Folders in ${where}:`);
  await activate(folders(page).filter({ hasText: "new-session-mobile" }), isMobile);
  await expectStartedIn(page, "new-session-mobile");
});

test("Tab looks inside the highlighted folder and Backspace goes up", async ({ page, isMobile }) => {
  test.skip(isMobile, "Keyboard flow");
  await open(page, isMobile);
  const where = await projectsFolder(page);
  await activate(option(page, "Other folder…"), isMobile);
  await expect(folders(page).first()).toBeVisible();
  await page.keyboard.press("Backspace");
  await expect(label(page)).toHaveText(`Folders in ${path.dirname(where)}:`);

  await page.keyboard.type("proj");
  await expect(folders(page).locator(".new-session-name")).toHaveText(["projects"]);
  await page.keyboard.press("Tab");
  await expect(label(page)).toHaveText(`Folders in ${where}:`);
  await expect(input(page)).toBeFocused();
  await expect(input(page)).toHaveValue("");

  await page.keyboard.type("new-session-d");
  await expect(folders(page).locator(".new-session-name")).toHaveText(["new-session-desktop"]);
  await page.keyboard.press("Enter");
  await expectStartedIn(page, "new-session-desktop");
});

test("only the most recent projects are listed until the rest are asked for", async ({ page, isMobile }) => {
  await open(page, isMobile);
  const total = await dialog(page).locator("[data-new-session-project]").count();
  await expect(dialog(page).locator("[data-new-session-project]:visible")).toHaveCount(5);
  await expect(option(page, "Other folder…")).toBeVisible();

  await activate(option(page, `${total - 5} more projects`), isMobile);
  await expect(dialog(page).getByRole("option")).toHaveCount(total + 1);
  await expect(dialog(page).getByRole("option").nth(5)).toHaveAttribute("aria-selected", "true");
  await expect(option(page, "Other folder…")).toBeInViewport();
});

test("Ctrl and a digit start directly in that visible row", async ({ page, isMobile }) => {
  test.skip(isMobile, "Keyboard flow");
  await open(page, isMobile);
  await expect(dialog(page).locator(".new-session-key:visible")).toHaveText(["1", "2", "3", "4", "5"]);

  await page.keyboard.type("new-session-");
  const second = dialog(page).getByRole("option").nth(1);
  await expect(second.locator(".new-session-key")).toHaveText("2");
  const cwd = await second.getAttribute("data-new-session-project");
  await page.keyboard.press("Control+2");
  await expectStartedIn(page, cwd.split("/").pop());
});
