import { expect, test } from "@playwright/test";
import { sessions } from "../support/contract.mjs";

// No worker runs in these specs: the gateway's live state of the session and its worker routes are answered here, as
// a gateway with a worker would answer them. With rendered, the session's page has that state, as the gateway renders
// it with one, and its live state waits for the first poll.
async function openOnFakeWorker(page, isMobile, { rendered = null } = {}) {
  const live = { worker: null, notices: [] };
  const posted = [];
  const answers = new Map();
  let held = rendered ? [] : null;
  // Events, such as of a shell command, which the page asks for after the last that it has.
  const events = [];
  const answer = (route) => {
    const after = Number(new URL(route.request().url()).searchParams.get("after"));
    return route.fulfill({ json: { events: events.slice(after), last_seq: events.length, missed: false, sudo_requests: [], worker: live.worker, worker_notices: live.notices } });
  };
  await page.route(/\/events(?:\?|$)/, (route) => (held ? held.push(route) : answer(route)));
  await page.route(/\/workers\/(move|return|forward|unforward|dismiss_notices)$/, async (route) => {
    const action = new URL(route.request().url()).pathname.split("/").pop();
    posted.push({ action, form: Object.fromEntries(new URLSearchParams(route.request().postData())) });
    const answer = answers.get(action) || (() => ({ status: 200, json: {} }));
    await route.fulfill(await answer());
  });
  await page.route(/\/workers\?session=/, async (route) => route.fulfill(await answers.get("list")()));
  const title = isMobile ? sessions.workerMobile : sessions.worker;
  await page.goto(`/?session_search=${encodeURIComponent(title)}`);
  const url = await page.getByRole("link", { name: new RegExp(title) }).getAttribute("href");
  if (rendered) {
    Object.assign(live, rendered);
    await page.route((target) => target.pathname === "/" && target.searchParams.has("session"), async (route) => {
      const response = await route.fetch();
      const attribute = (value) => JSON.stringify(value).replaceAll("&", "&amp;").replaceAll('"', "&#34;");
      const body = (await response.text())
        .replace('data-worker="null"', `data-worker="${attribute(rendered.worker)}"`)
        .replace('data-worker-notices="[]"', `data-worker-notices="${attribute(rendered.notices)}"`);
      await route.fulfill({ response, body });
    });
  }
  await page.goto(url);
  await expect(page.getByRole("heading", { level: 1, name: title })).toBeVisible();
  const session = await page.locator('.prompt-form input[name="session"]').inputValue();
  const poll = ({ events: added = [], ...changes }) => {
    events.push(...added);
    Object.assign(live, changes);
    held?.forEach(answer);
    held = null;
    return page.evaluate(() => window.dispatchEvent(new Event("pageshow")));
  };
  return { live, posted, answers, session, poll };
}

const block = (page) => page.getByRole("region", { name: "Worker" });
const footer = (page) => page.locator('.session-status-item[data-status-key="worker"]');
const composer = (page) => page.getByLabel("Message to Pi");
const openPalette = async (page, search) => {
  await page.keyboard.press("Control+k");
  const dialog = page.getByRole("dialog", { name: "Go to or run" });
  await dialog.getByRole("combobox").fill(search);
  return dialog;
};
const palette = async (page, command) => {
  const dialog = await openPalette(page, command);
  await expect(dialog.getByRole("option", { name: command, exact: true })).toBeVisible();
  await page.keyboard.press("Enter");
};
const press = (locator, isMobile) => (isMobile ? locator.tap() : locator.click());
const at = (seconds) => new Date(Date.now() + seconds * 1000).toISOString();
const connected = { worker: "beelink", state: "connected", repository: "/work/storefront", ports: [{ port: 5173, local: 5173 }] };
const listed = {
  workers: [{ name: "nuc", destination: "e2e@nuc.invalid", state: "unreachable" }, { name: "beelink", destination: "e2e@worker.invalid", state: "connected" }],
  repository: { main: "/work/storefront", roots: ["/work/storefront", "/work/.worktrees/storefront/coupons"], worktrees: 1, sessions: 1 }
};

test("a lost worker takes the editor's place, counts down, and one tap continues on main", async ({ page, isMobile }) => {
  const { posted, answers, session, poll } = await openOnFakeWorker(page, isMobile);
  let returned;
  answers.set("return", () => new Promise((resolve) => { returned = () => resolve({ status: 200, json: { session } }); }));
  await poll({ worker: connected });
  await expect(footer(page)).toContainText("beelink connected");
  await expect(page.locator(".session-header [data-worker-badge]")).toBeVisible();
  await expect(page.locator(".session-header [data-worker-badge]")).toHaveAttribute("title", "Runs on beelink · connected · on a private copy of storefront");
  await expect(block(page)).toBeHidden();

  await poll({ worker: { ...connected, state: "unreachable", gone_at: at(2.5), ports: [] } });
  await expect(block(page)).toContainText("beelink is not answering");
  await expect(block(page)).toContainText("then this session can continue on main");
  await expect(composer(page)).toBeHidden();
  const proceed = block(page).getByRole("button", { name: /^continue on main/ });
  await expect(proceed).toBeDisabled();
  await expect(proceed).toHaveText(/continue on main in 0:0\d/);

  await expect(block(page)).toContainText("beelink is gone");
  await expect(block(page)).toContainText("this session, with its changes up to the last step that main confirmed");
  await expect(proceed).toBeEnabled();
  await press(proceed, isMobile);
  await expect(block(page)).toContainText("Bringing this session back from beelink");
  await expect.poll(() => posted).toEqual([{ action: "return", form: { session } }]);
  await poll({ worker: null });
  returned();
  await expect(block(page)).toBeHidden();
  await expect(composer(page)).toBeVisible();
  await expect(footer(page)).toHaveCount(0);
});

test("Run on worker says that only this session moves, and the editor's place shows the wait, the copy and a refusal", async ({ page, isMobile }) => {
  const { posted, answers, session, poll } = await openOnFakeWorker(page, isMobile);
  answers.set("list", () => ({ status: 200, json: listed }));
  let refuse;
  answers.set("move", () => new Promise((resolve) => { refuse = () => resolve({ status: 409, json: { error: "session Coupons is still working; try again once it has finished" } }); }));

  await palette(page, "Run on worker…");
  const card = page.getByRole("dialog", { name: "Run on worker" });
  const worker = card.getByRole("option", { name: /beelink/ });
  const other = card.getByRole("option", { name: /nuc/ });
  await expect(worker).toContainText("e2e@worker.invalid");
  await expect(worker).toHaveAttribute("aria-selected", "true");
  await expect(card).toContainText("this session only, to a private copy of storefront");
  await expect(card).toContainText("storefront and /work/.worktrees/storefront/coupons");
  await expect(card).toContainText("storefront's other sessions keep running");
  await expect(card).toContainText("brings its changes back; what main changed too is kept aside");
  // The first tap points at a worker; the move has its own button.
  await press(other, isMobile);
  await expect(other).toHaveAttribute("aria-selected", "true");
  await expect(card.getByRole("button", { name: "move to nuc" })).toBeVisible();
  await press(worker, isMobile);
  await expect(worker).toHaveAttribute("aria-selected", "true");
  await press(card.getByRole("button", { name: "move to beelink" }), isMobile);

  await expect(card).toBeHidden();
  await expect(block(page)).toContainText("Moving this session to beelink");
  await expect.poll(() => posted).toEqual([{ action: "move", form: { session, worker: "beelink" } }]);
  await poll({ worker: { ...connected, state: "connected", copying: at(0), waiting: "Allow lowercase coupon codes in the checkout and the admin", ports: [] } });
  await expect(block(page)).toContainText("Waiting for “Allow lowercase coupon codes in the checkout and…” to finish");
  await expect(block(page)).toContainText("new prompts to storefront's sessions wait meanwhile");
  // A long name wraps on a phone, rather than widening the editor's place past its screen.
  const { x, width } = await block(page).boundingBox();
  expect(x + width).toBeLessThanOrEqual(page.viewportSize().width);
  await poll({ worker: { ...connected, state: "copying", copying: at(0), ports: [] } });
  await expect(block(page)).toContainText("copying the session and its copy of storefront there");
  await poll({ worker: null });
  refuse();
  await expect(block(page)).toContainText("Could not move this session");
  await expect(block(page)).toContainText("is still working; try again once it has finished");
  await press(block(page).getByRole("button", { name: "ok" }), isMobile);
  await expect(block(page)).toBeHidden();
  await expect(composer(page)).toBeVisible();
});

test("Run on worker says why a session can't move, and isn't offered to one on a worker", async ({ page, isMobile }) => {
  const { answers, poll } = await openOnFakeWorker(page, isMobile);
  answers.set("list", () => ({ status: 409, json: { error: "/work/notes is not in a Git repository; only sessions in one can move to a worker" } }));

  await palette(page, "Run on worker…");
  const card = page.getByRole("dialog", { name: "Run on worker" });
  await expect(card).toContainText("/work/notes is not in a Git repository; only sessions in one can move to a worker");
  await expect(card.getByRole("option")).toHaveCount(0);
  await page.keyboard.press("Escape");
  await expect(card).toBeHidden();

  await poll({ worker: connected });
  await expect(footer(page)).toContainText("beelink connected");
  const dialog = await openPalette(page, "");
  const commands = dialog.getByRole("group", { name: "beelink · connected" });
  await expect(commands.getByRole("option")).toHaveText([/Continue on main/, /Forward a port…/, /Stop forwarding 5173/]);
  await expect(dialog.getByRole("option", { name: "Run on worker…", exact: true })).toHaveCount(0);
});

test("Run on worker isn't offered while a shell command runs, which a move would refuse", async ({ page, isMobile }) => {
  const { poll } = await openOnFakeWorker(page, isMobile);
  const offered = async (count) => {
    const dialog = await openPalette(page, "");
    await expect(dialog.getByRole("option", { name: "Choose model and thinking" })).toBeVisible();
    await expect(dialog.getByRole("option", { name: "Run on worker…", exact: true })).toHaveCount(count);
    await page.keyboard.press("Escape");
    await expect(dialog).toBeHidden();
  };
  const command = { bashId: "e2e-worker-bash", command: "sleep 30" };

  await poll({ events: [{ type: "bash_start", ...command }] });
  await expect(page.locator(".composer-state")).toHaveAttribute("data-state", "bash");
  await offered(0);
  await poll({ events: [{ type: "bash_end", ...command, result: { output: "", exitCode: 0 } }] });
  await expect(page.locator(".composer-state")).not.toHaveAttribute("data-state", "bash");
  await offered(1);
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

const kept = {
  repository: "/work/storefront", worker: "beelink", aside: "/state/returns/0a1b2c3d/worker",
  paths: ["/work/storefront/src/cart/totals.ts", "refs/heads/coupons"],
  items: [
    { path: "/work/storefront/src/cart/totals.ts", reason: "changed on both sides" },
    { path: "refs/heads/coupons", reason: "changed on both sides", ref: "refs/gripi/returns/0a1b2c3d/refs/heads/coupons" }
  ],
  refs: ["refs/gripi/returns/0a1b2c3d/refs/heads/coupons"],
  text: "Main kept its own version of what follows. The worker's is in /state/returns/0a1b2c3d/worker or under the ref named:\n- /work/storefront/src/cart/totals.ts: changed on both sides"
};

test("what a return kept aside shows above the editor, as the page renders it, until a confirmed dismiss", async ({ page, isMobile }) => {
  const { posted, session, poll } = await openOnFakeWorker(page, isMobile, { rendered: { worker: null, notices: [kept] } });
  const notice = page.locator("[data-worker-notice]");
  await expect(notice).toContainText("2 changes from beelink kept aside.");
  await expect(notice).toContainText("Main kept its own src/cart/totals.ts and refs/heads/coupons. The worker's versions, if any, are in /state/returns/0a1b2c3d/worker and refs/gripi/returns/0a1b2c3d/refs/heads/coupons.");
  await expect(composer(page)).toBeVisible();
  await poll({});
  const details = notice.getByText("- /work/storefront/src/cart/totals.ts: changed on both sides");
  await expect(details).toBeHidden();
  await press(notice.getByText("details", { exact: true }), isMobile);
  await expect(details).toBeVisible();

  const dismiss = notice.getByRole("button", { name: "Dismiss" });
  const warnings = [];
  page.once("dialog", (dialog) => {
    warnings.push(dialog.message());
    dialog.dismiss();
  });
  await press(dismiss, isMobile);
  await expect.poll(() => warnings).toEqual([expect.stringContaining("also deletes what was kept aside")]);
  await expect(notice).toBeVisible();

  page.once("dialog", (dialog) => dialog.accept());
  await press(dismiss, isMobile);
  await expect(notice).toBeHidden();
  expect(posted).toEqual([{ action: "dismiss_notices", form: { session } }]);
});

test("a return that waits, runs and stops before it finished keeps the editor's place, also as the page renders it", async ({ page, isMobile }) => {
  const returning = { ...connected, state: "returning", returning: at(0), ports: [] };
  const { posted, session, poll } = await openOnFakeWorker(page, isMobile, { rendered: { worker: { ...returning, waiting: "Coupons" }, notices: [] } });
  await expect(block(page)).toContainText("Waiting for “Coupons” to finish");
  await expect(block(page)).toContainText("this session comes back from beelink with its changes");
  await expect(composer(page)).toBeHidden();

  await poll({ worker: returning });
  await expect(block(page)).toContainText("Bringing this session back from beelink");
  await expect(block(page).getByRole("button")).toHaveCount(0);

  await poll({ worker: { ...connected, state: "returning", ports: [] } });
  await expect(block(page)).toContainText("Bringing this session back from beelink didn't finish");
  await press(block(page).getByRole("button", { name: "continue on main" }), isMobile);
  await expect.poll(() => posted).toEqual([{ action: "return", form: { session } }]);
  await poll({ worker: null });
  await expect(block(page)).toBeHidden();
  await expect(composer(page)).toBeVisible();
});
