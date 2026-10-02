import { expect, test } from "@playwright/test";
import { sessions } from "../support/contract.mjs";
import { selectSession, sendPrompt } from "../support/ui.mjs";

test("open, label, and navigate the native Pi session tree", async ({ page }) => {
  await page.goto("/");
  await selectSession(page, sessions.tree);
  const composer = page.getByPlaceholder("Ask Pi…");
  await composer.fill("/tree");
  const treeCommand = page.locator('.command[data-command-name="tree"]');
  await expect(treeCommand).toBeVisible();
  await treeCommand.click();
  await composer.press("Enter");

  const dialog = page.getByRole("dialog", { name: "Session tree" });
  await expect(dialog).toBeVisible();
  const status = dialog.locator("[data-tree-session-status]");
  await expect(status).toHaveText("(1/2)");
  const entries = dialog.locator("[data-tree-entry-id]");
  await expect(entries.nth(0)).toHaveText(`›user: Fixture question for ${sessions.tree}`);
  await expect(entries.nth(1)).toHaveText(`›assistant: Fixture answer for ${sessions.tree}`);
  await expect(entries.nth(0).locator(".picker-cursor")).toBeVisible();
  await expect(entries.nth(1).locator(".picker-cursor")).toBeHidden();
  await page.keyboard.press("ArrowDown");
  await expect(status).toHaveText("(2/2)");
  await entries.nth(0).click();
  await expect(status).toHaveText("(1/2)");

  const labelInput = dialog.getByLabel("Label (empty to remove):");
  await expect(labelInput).toBeHidden();
  await page.keyboard.press("Shift+L");
  await expect(labelInput).toBeFocused();
  await labelInput.fill("E2E checkpoint");
  await labelInput.press("Enter");
  await expect(status).toHaveText("Label updated.");
  await expect(labelInput).toBeHidden();
  await expect(entries.nth(0)).toHaveText(`›[E2E checkpoint] user: Fixture question for ${sessions.tree}`);

  await dialog.getByRole("button", { name: "labeled-only" }).click();
  await expect(status).toHaveText("(1/1) [labeled]");
  await expect(dialog.getByRole("button", { name: "labeled-only" })).toHaveAttribute("aria-pressed", "true");
  await dialog.getByRole("button", { name: "default", exact: true }).click();
  await expect(status).toHaveText("(1/2)");

  await page.keyboard.press("/");
  const search = dialog.getByLabel("Type to search:");
  await expect(search).toBeFocused();
  await search.fill("answer");
  await expect(entries).toHaveCount(1);
  await page.keyboard.press("Escape");
  await expect(entries).toHaveCount(2);
  await entries.nth(0).click();

  const navigate = dialog.getByRole("button", { name: "navigate" });
  await navigate.click();
  await expect(dialog.getByText("Summarize branch?")).toBeVisible();
  // The tree's shortcuts stay off while the summary step covers it.
  await page.keyboard.press("Shift+L");
  await page.keyboard.press("Escape");
  await expect(navigate).toBeVisible();
  await expect(dialog.getByRole("treeitem").first()).toBeFocused();
  await page.keyboard.press("Enter");
  const choices = dialog.getByRole("option");
  await expect(choices).toHaveText(["→No summary", "→Summarize", "→Summarize with custom prompt"]);
  await expect(choices.nth(0)).toBeFocused();
  await page.keyboard.press("ArrowUp");
  await expect(choices.nth(2)).toHaveAttribute("aria-selected", "true");
  await page.keyboard.press("Enter");
  await expect(dialog.getByLabel("Custom summarization instructions")).toBeFocused();
  await page.keyboard.press("Escape");
  await expect(choices.nth(0)).toBeFocused();
  await page.keyboard.press("Enter");

  await expect(dialog).toBeHidden();
  await expect(page.getByPlaceholder("Ask Pi…")).toHaveValue(`Fixture question for ${sessions.tree}`);
});

test.describe("with touch", () => {
  test.use({ hasTouch: true });

  test("draw branches with Pi's connectors, markers, and fold glyphs", async ({ page }) => {
    const entry = (entryId, parentId, role, text, extra = {}) => ({ entryId, parentId, type: "message", role, text, current: false, latest: false, ...extra });
    await page.route("**/sessions/tree_entries?*", (route) => route.fulfill({
      json: {
        entries: [
          entry("root", null, "user", "Start"),
          entry("plan", "root", "assistant", "Plan"),
          entry("api", "plan", "user", "Try API", { label: "kept" }),
          entry("api-done", "api", "assistant", "API done", { current: true }),
          entry("docs", "plan", "user", "Try docs"),
          entry("docs-done", "docs", "toolResult", "Docs read", { latest: true })
        ],
        filter: "all",
        truncated: false,
        totalEntries: 6,
        settings: { treeFilterMode: "default", branchSummary: { skipPrompt: false } }
      }
    }));
    await page.goto("/");
    await selectSession(page, sessions.tree);
    await sendPrompt(page, "/tree");

    const dialog = page.getByRole("dialog", { name: "Session tree" });
    const lines = () => dialog.locator(".tree-session-line").allTextContents();
    await expect.poll(lines).toEqual([
      "• user: Start",
      "• assistant: Plan",
      "├⊟ • [kept] user: Try API",
      "│     • assistant: API done",
      "└⊟ user: Try docs",
      "      [toolResult]: Docs read (latest)"
    ]);
    await expect(dialog.locator("[data-tree-session-status]")).toHaveText("(4/6) [all]");

    // The fold glyph is one character wide, so its tap target reaches beyond it.
    const fold = await dialog.getByRole("button", { name: "Collapse branch" }).first().boundingBox();
    await page.touchscreen.tap(fold.x - 6, fold.y + fold.height + 4);
    await expect.poll(lines).toEqual([
      "• user: Start",
      "• assistant: Plan",
      "├⊞ • [kept] user: Try API",
      "└⊟ user: Try docs",
      "      [toolResult]: Docs read (latest)"
    ]);

    await dialog.locator("[data-tree-entry-id]").first().tap();
    await expect(dialog.locator("[data-tree-session-status]")).toHaveText("(1/5) [all]");
    await dialog.getByRole("button", { name: "navigate" }).tap();
    await expect(dialog.getByRole("option")).toHaveCount(3);
  });
});
