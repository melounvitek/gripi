const test = require("node:test");
const assert = require("node:assert/strict");
const { createPcAccess } = require("./pc_access");

const GATEWAY = { id: "home", name: "home-server", url: "https://home.example.test/" };
const SESSION = "/home/vitek/.pi/sessions/a.jsonl";
const LIST = Buffer.from([11]).toString("base64");
const FIVE_MINUTES = 5 * 60 * 1000;

// The fake guard forwards [11], asks to sign for [13, JSON], and refuses anything else.
const fakeGuard = {
  createConnectionGuard() {
    let listed = false;
    return {
      check(body) {
        if (body[0] === 11) return { action: "forward" };
        if (body[0] !== 13) return { action: "refuse", reason: `agent request ${body[0]} not allowed` };

        const { key, host, user } = JSON.parse(body.subarray(1));
        return { action: "sign", key: Buffer.from(key), hostKey: Buffer.from(host), user };
      },
      observe(request) {
        if (request[0] === 11) listed = true;
      },
      comment: (key) => (listed ? `${key}@laptop` : "")
    };
  },
  findKnownHost(hostKey, texts) {
    const lines = texts.join("\n").split("\n");
    if (lines.includes(`@revoked ${hostKey}`)) return { status: "revoked" };
    const line = lines.find((candidate) => candidate.endsWith(` ${hostKey}`));
    return line ? { status: "known", names: [line.split(" ")[0]] } : { status: "unknown" };
  },
  fingerprint: (blob) => `SHA256:${blob}`,
  keyType: () => "ssh-ed25519"
};

function sign({ key = "key-a", host = "host-gh", user = "git" } = {}) {
  return Buffer.concat([Buffer.from([13]), Buffer.from(JSON.stringify({ key, host, user }))]).toString("base64");
}

function ssh(id, message, fields = {}) {
  return { type: "ssh", id, session: SESSION, session_name: "Fix login redirect", connection: "c1", message, ...fields };
}

function agentReply(message) {
  return Buffer.concat([Buffer.from("re:"), Buffer.from(message, "base64")]).toString("base64");
}

async function settle() {
  for (let round = 0; round < 5; round++) await new Promise(setImmediate);
}

function setup(t, { authenticate = async () => ({ ok: true }) } = {}) {
  const harness = { polls: [], posts: [], prompts: [], agents: [], auths: 0, time: 0 };
  harness.pc = createPcAccess({
    request(_gateway, path, body, signal) {
      if (path !== "/pc/link") {
        harness.posts.push({ path, body });
        return Promise.resolve({ status: 200, body: {} });
      }
      return new Promise((resolve, reject) => {
        const poll = { body, resolve, reject };
        harness.polls.push(poll);
        signal.addEventListener("abort", () => {
          harness.polls = harness.polls.filter((pending) => pending !== poll);
          reject(new Error("aborted"));
        });
      });
    },
    // Like Electron's message box, an aborted dialog answers with its cancel button.
    confirm: (prompt) => new Promise((resolve) => {
      harness.prompts.push({ ...prompt, answer: resolve });
      prompt.signal.addEventListener("abort", () => resolve("deny"));
    }),
    authenticate(signal) {
      harness.auths += 1;
      return authenticate(signal);
    },
    async connectAgent() {
      const agent = {
        requests: [],
        closed: false,
        async request(body) {
          agent.requests.push(body.toString("base64"));
          return Buffer.concat([Buffer.from("re:"), body]);
        },
        close() {
          agent.closed = true;
        }
      };
      harness.agents.push(agent);
      return agent;
    },
    readKnownHosts: async () => ["github.com host-gh\ngitlab.com host-gl", "@revoked host-bad"],
    now: () => harness.time,
    sshAgentGuard: fakeGuard
  });
  harness.link = () => harness.polls.at(-1).body.link;
  harness.replies = () => harness.posts.filter((post) => post.path === "/pc/link/reply").map((post) => post.body);
  harness.deliver = async (...messages) => {
    harness.polls.shift().resolve({ status: 200, body: { messages } });
    await settle();
  };
  harness.answer = async (answer) => {
    harness.prompts.at(-1).answer(answer);
    await settle();
  };
  t.after(() => harness.pc.stopAll());
  harness.pc.start(GATEWAY);
  return harness;
}

test("relays agent messages over one agent connection per relayed connection and polls again at once", async (t) => {
  const h = setup(t);
  const link = h.link();

  await h.deliver(ssh("1", LIST), ssh("2", LIST), ssh("3", LIST, { connection: "c2" }));

  assert.deepEqual(h.replies().sort((a, b) => a.id.localeCompare(b.id)), [
    { link, id: "1", message: agentReply(LIST) },
    { link, id: "2", message: agentReply(LIST) },
    { link, id: "3", message: agentReply(LIST) }
  ]);
  assert.deepEqual(h.agents.map((agent) => agent.requests.length), [2, 1]);
  assert.equal(h.polls.length, 1);
  assert.equal(h.link(), link);

  await h.deliver({ type: "ssh_close", session: SESSION, connection: "c1" });

  assert.deepEqual(h.agents.map((agent) => agent.closed), [true, false]);
});

test("passes on the guard's refusal without reaching the agent", async (t) => {
  const h = setup(t);

  await h.deliver(ssh("1", Buffer.from([17]).toString("base64")));

  assert.deepEqual(h.replies(), [{ link: h.link(), id: "1", error: "agent request 17 not allowed" }]);
  assert.equal(h.agents.length, 0);
});

test("refuses servers whose host key the PC doesn't know or has revoked", async (t) => {
  const h = setup(t);

  await h.deliver(ssh("1", sign({ host: "host-new" })), ssh("2", sign({ host: "host-bad" }), { connection: "c2" }));

  const [unknown, revoked] = h.replies().sort((a, b) => a.id.localeCompare(b.id));
  assert.match(unknown.error, /host key ssh-ed25519 SHA256:host-new is not in the PC's known_hosts.*~\/\.ssh\/known_hosts/);
  assert.match(revoked.error, /host key ssh-ed25519 SHA256:host-bad is marked @revoked/);
  assert.equal(h.prompts.length, 0);
});

test("asks with the request's details, escaped, and signs nothing when denied", async (t) => {
  const h = setup(t);

  await h.deliver(
    ssh("1", LIST),
    ssh("2", sign({ user: "git\u0000" }), { session_name: "Fix \u202elogin\n" })
  );

  const { answer, signal, ...prompt } = h.prompts[0];
  assert.deepEqual(prompt, {
    gateway: GATEWAY,
    sessionName: "Fix \\u202elogin\\u000a",
    key: { type: "ssh-ed25519", fingerprint: "SHA256:key-a", comment: "key-a@laptop" },
    server: { names: ["github.com"], hostKeyFingerprint: "SHA256:host-gh" },
    login: "git\\u0000"
  });

  await h.answer("deny");

  assert.deepEqual(h.replies().at(-1), { link: h.link(), id: "2", error: "denied on the PC" });
  assert.equal(h.agents[0].requests.length, 1);
  assert.equal(h.auths, 0);
});

test("allow once signs after the password check and asks again next time", async (t) => {
  const h = setup(t);

  await h.deliver(ssh("1", sign()));
  await h.answer("once");

  assert.equal(h.auths, 1);
  assert.deepEqual(h.replies(), [{ link: h.link(), id: "1", message: agentReply(sign()) }]);

  await h.deliver(ssh("2", sign()));

  assert.equal(h.prompts.length, 2);
});

test("a 5-minute grant covers only its session, key, host and login, until it expires", async (t) => {
  const h = setup(t);

  await h.deliver(ssh("1", sign()));
  await h.answer("grant");
  h.time += FIVE_MINUTES - 1;
  await h.deliver(ssh("2", sign(), { connection: "c2" }));

  assert.equal(h.prompts.length, 1);
  assert.equal(h.auths, 1);
  assert.deepEqual(h.replies().at(-1), { link: h.link(), id: "2", message: agentReply(sign()) });

  const others = [
    ssh("3", sign(), { session: "/home/vitek/.pi/sessions/b.jsonl" }),
    ssh("4", sign({ key: "key-b" }), { connection: "c3" }),
    ssh("5", sign({ host: "host-gl" }), { connection: "c4" }),
    ssh("6", sign({ user: "deploy" }), { connection: "c5" })
  ];
  for (const [index, message] of others.entries()) {
    await h.deliver(message);
    assert.equal(h.prompts.length, index + 2, `${message.id} asks again`);
    await h.answer("deny");
  }

  h.time += 1;
  await h.deliver(ssh("7", sign(), { connection: "c6" }));

  assert.equal(h.prompts.length, 6);
});

test("a failed password check denies and grants nothing", async (t) => {
  const h = setup(t, { authenticate: async () => ({ ok: false, reason: "the password dialog was cancelled" }) });

  await h.deliver(ssh("1", sign()));
  await h.answer("grant");

  assert.deepEqual(h.replies(), [{ link: h.link(), id: "1", error: "denied on the PC: the password dialog was cancelled" }]);
  assert.equal(h.agents.length, 0);

  await h.deliver(ssh("2", sign(), { connection: "c2" }));

  assert.equal(h.prompts.length, 2);
});

test("refuses at once when more than 5 requests wait for approval", async (t) => {
  const h = setup(t);

  await h.deliver(...[1, 2, 3, 4, 5, 6].map((id) => ssh(String(id), sign(), { connection: `c${id}` })));

  assert.equal(h.prompts.length, 1);
  assert.deepEqual(h.replies(), [{ link: h.link(), id: "6", error: "denied on the PC: too many requests are waiting for approval there" }]);

  await h.answer("deny");

  assert.equal(h.prompts.length, 2);
});

test("a cancel closes the request's dialog, or keeps it from showing, and signs nothing", async (t) => {
  const h = setup(t);

  await h.deliver(ssh("1", sign()), ssh("2", sign(), { connection: "c2" }));
  await h.deliver({ type: "cancel", id: "2" }, { type: "cancel", id: "1" });

  assert.equal(h.prompts[0].signal.aborted, true);
  assert.equal(h.prompts.length, 1);
  assert.deepEqual(h.replies(), []);
  assert.equal(h.agents.length, 0);
});

test("a cancel during the password check signs nothing", async (t) => {
  let confirmed;
  const h = setup(t, { authenticate: () => new Promise((resolve) => { confirmed = resolve; }) });

  await h.deliver(ssh("1", sign()));
  await h.answer("grant");
  await h.deliver({ type: "cancel", id: "1" });
  confirmed({ ok: true });
  await settle();

  assert.deepEqual(h.replies(), []);
  assert.equal(h.agents.length, 0);
});

test("stopping closes the link, its dialogs and agent connections, and forgets grants", async (t) => {
  const h = setup(t);
  const link = h.link();
  await h.deliver(ssh("1", LIST), ssh("2", sign()));
  await h.answer("grant");
  await h.deliver(ssh("3", sign({ user: "deploy" }), { connection: "c2" }));

  await h.pc.stop(GATEWAY.id);

  assert.equal(h.prompts[1].signal.aborted, true);
  assert.deepEqual(h.posts.at(-1), { path: "/pc/link/close", body: { link } });
  assert.equal(h.agents[0].closed, true);
  await settle();
  assert.equal(h.replies().length, 2);

  h.pc.start(GATEWAY);
  assert.notEqual(h.link(), link);
  await h.deliver(ssh("4", sign()));

  assert.equal(h.prompts.length, 3);
});

test("a dialog nobody answers in 2 minutes is denied", async (t) => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const h = setup(t);

  await h.deliver(ssh("1", sign()));
  t.mock.timers.tick(2 * 60 * 1000);
  await settle();

  assert.deepEqual(h.replies(), [{ link: h.link(), id: "1", error: "denied on the PC: nobody answered there in 2 minutes" }]);
  assert.equal(h.agents.length, 0);
});

test("backs off on network errors from 1 second to 30, and polls at once after a success", async (t) => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const h = setup(t);
  const link = h.link();

  for (const delay of [1000, 2000, 4000, 8000, 16000, 30000, 30000]) {
    h.polls.shift().reject(new Error("offline"));
    await expectPollAfter(t, h, delay);
  }
  await h.deliver();
  assert.equal(h.polls.length, 1);
  h.polls.shift().reject(new Error("offline"));
  await expectPollAfter(t, h, 1000);

  assert.equal(h.link(), link);
});

test("retries after a minute when refused, keeping the link", async (t) => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const h = setup(t);
  const link = h.link();

  h.polls.shift().resolve({ status: 403, body: { error: "refused" } });
  await expectPollAfter(t, h, 60 * 1000);

  assert.equal(h.link(), link);
});

test("a link taken by another app is lost: it retries after 10 seconds with a new ID and forgets grants", async (t) => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const h = setup(t);
  const link = h.link();
  await h.deliver(ssh("1", sign()));
  await h.answer("grant");

  h.polls.shift().resolve({ status: 409, body: { error: "another link" } });
  await expectPollAfter(t, h, 10 * 1000);
  assert.notEqual(h.link(), link);
  await h.deliver(ssh("2", sign()));

  assert.equal(h.prompts.length, 2);
});

async function expectPollAfter(t, h, delay) {
  await settle();
  t.mock.timers.tick(delay - 1);
  await settle();
  assert.equal(h.polls.length, 0, `no poll before ${delay} ms`);
  t.mock.timers.tick(1);
  await settle();
  assert.equal(h.polls.length, 1, `a poll after ${delay} ms`);
}
