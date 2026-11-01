import { expect, test } from "@playwright/test";
import { sessions } from "../support/contract.mjs";

// No worker runs in these specs: the gateway's live state of the session and its worker routes are answered here, as
// a gateway with a worker would answer them.
async function openOnFakeWorker(page, isMobile) {
  const live = { worker: null, notices: [] };
  const posted = [];
  const answers = new Map();
  await page.route(/\/events(?:\?|$)/, (route) => route.fulfill({ json: { events: [], last_seq: 0, missed: false, sudo_requests: [], worker: live.worker, worker_notices: live.notices } }));
  await page.route(/\/workers\/(move|return|forward|unforward|dismiss_notices)$/, async (route) => {
    const action = new URL(route.request().url()).pathname.split("/").pop();
    posted.push({ action, form: Object.fromEntries(new URLSearchParams(route.request().postData())) });
    const answer = answers.get(action) || (() => ({ status: 200, json: {} }));
    await route.fulfill(await answer());
  });
  const title = isMobile ? sessions.workerMobile : sessions.worker;
  await page.goto(`/?session_search=${encodeURIComponent(title)}`);
  await page.goto(await page.getByRole("link", { name: new RegExp(title) }).getAttribute("href"));
  await expect(page.getByRole("heading", { level: 1, name: title })).toBeVisible();
  const session = await page.locator('.prompt-form input[name="session"]').inputValue();
  const poll = (changes) => {
    Object.assign(live, changes);
    return page.evaluate(() => window.dispatchEvent(new Event("pageshow")));
  };
  return { live, posted, answers, session, poll };
}

const block = (page) => page.getByRole("region", { name: "Worker" });
const footer = (page) => page.locator('.session-status-item[data-status-key="worker"]');
const composer = (page) => page.getByLabel("Message to Pi");
const palette = async (page, command) => {
  await page.keyboard.press("Control+k");
  const dialog = page.getByRole("dialog", { name: "Go to or run" });
  await dialog.getByRole("combobox").fill(command);
  await expect(dialog.getByRole("option", { name: command, exact: true })).toBeVisible();
  await page.keyboard.press("Enter");
};
const press = (locator, isMobile) => (isMobile ? locator.tap() : locator.click());
const connected = { worker: "beelink", state: "connected", repository: "/work/storefront", ports: [{ port: 5173, local: 5173 }] };

test("a lost worker takes the editor's place, counts down, and one tap continues on main", async ({ page, isMobile }) => {
  const { posted, session, poll } = await openOnFakeWorker(page, isMobile);
  await poll({ worker: connected });
  await expect(footer(page)).toContainText("beelink connected");
  await expect(page.locator(".session-header [data-worker-badge]")).toBeVisible();
  await expect(block(page)).toBeHidden();

  await poll({ worker: { ...connected, state: "unreachable", gone_at: new Date(Date.now() + 2500).toISOString(), ports: [] } });
  await expect(block(page)).toContainText("beelink is not answering");
  await expect(composer(page)).toBeHidden();
  const proceed = block(page).getByRole("button", { name: /^continue on main/ });
  await expect(proceed).toBeDisabled();
  await expect(proceed).toHaveText(/continue on main in 0:0\d/);

  await expect(block(page)).toContainText("beelink is gone");
  await expect(proceed).toBeEnabled();
  await press(proceed, isMobile);
  await expect.poll(() => posted).toEqual([{ action: "return", form: { session } }]);
  await poll({ worker: null });
  await expect(block(page)).toBeHidden();
  await expect(composer(page)).toBeVisible();
  await expect(footer(page)).toHaveCount(0);
});

test("Run on worker says what moves, and the editor's place shows the copy and a refusal", async ({ page, isMobile }) => {
  const { posted, answers, session, poll } = await openOnFakeWorker(page, isMobile);
  let refuse;
  answers.set("move", () => new Promise((resolve) => { refuse = () => resolve({ status: 409, json: { error: "session E2E is working; wait until it finishes or stop it" } }); }));

  await palette(page, "Run on worker…");
  const card = page.getByRole("dialog", { name: "Run on worker" });
  const worker = card.getByRole("option", { name: /beelink/ });
  await expect(worker).toContainText("e2e@worker.invalid");
  await expect(card).toContainText("moves");
  await expect(card).toContainText("stays read-only until it comes back");
  // The first tap points at the worker; the move has its own button.
  await press(worker, isMobile);
  await expect(worker).toHaveAttribute("aria-selected", "true");
  await press(card.getByRole("button", { name: "move to beelink" }), isMobile);

  await expect(card).toBeHidden();
  await expect(block(page)).toContainText(/Copying .* to beelink/);
  await expect.poll(() => posted).toEqual([{ action: "move", form: { session, worker: "beelink" } }]);
  await poll({ worker: { ...connected, state: "copying", copying: new Date().toISOString(), ports: [] } });
  await expect(block(page)).toContainText("copying the repository and its sessions");
  await poll({ worker: null });
  refuse();
  await expect(block(page)).toContainText("is working; wait until it finishes");
  await press(block(page).getByRole("button", { name: "ok" }), isMobile);
  await expect(block(page)).toBeHidden();
  await expect(composer(page)).toBeVisible();
});

test("Forward a port forwards it, and the editor's footer lists it until it is stopped", async ({ page, isMobile }) => {
  const { live, posted, answers, session, poll } = await openOnFakeWorker(page, isMobile);
  await poll({ worker: connected });
  await expect(footer(page)).toContainText("beelink connected");
  answers.set("forward", () => {
    live.worker = { ...connected, ports: [...connected.ports, { port: 3000, local: 3001 }] };
    return { status: 200, json: { port: 3000, local: 3001 } };
  });

  await palette(page, "Forward a port…");
  const card = page.getByRole("dialog", { name: "Forward a port" });
  await card.getByLabel("Port on the worker").fill("3000");
  await expect(card).toContainText("beelink's localhost:3000");
  await press(card.getByRole("button", { name: "forward" }), isMobile);
  await expect(card).toBeHidden();
  if (!isMobile) await expect(footer(page)).toContainText("ports 5173, 3000→3001");

  await palette(page, "Stop forwarding 3000");
  await expect.poll(() => posted.map(({ action, form }) => [action, form.port, form.session])).toEqual([["forward", "3000", session], ["unforward", "3000", session]]);
});

test("main's files saved aside show above the editor until dismissed", async ({ page, isMobile }) => {
  const { posted, session, poll } = await openOnFakeWorker(page, isMobile);
  await poll({ worker: connected, notices: [{ repository: "/work/storefront", aside: "/state/mirrors/5f1c/aside/1/main/7", paths: ["/work/storefront/src/cart/totals.ts"] }] });
  const notice = page.locator("[data-worker-notice]");
  await expect(notice).toContainText("1 file changed on main while storefront ran on beelink.");
  await expect(notice).toContainText("src/cart/totals.ts is saved in /state/mirrors/5f1c/aside/1/main/7");
  await press(notice.getByRole("button", { name: "Dismiss" }), isMobile);
  await expect(notice).toBeHidden();
  expect(posted).toEqual([{ action: "dismiss_notices", form: { session } }]);
});
