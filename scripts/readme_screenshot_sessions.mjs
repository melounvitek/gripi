// Invented sessions for the README screenshots, each last active minutesAgo.
const oneReply = (project, slug, title, minutesAgo, user, thinking, answer, extra = {}) => ({
  project, slug, title, minutesAgo, ...extra,
  turns: [{ user }, { thinking, text: answer }]
});

export const sessions = [
  oneReply("storefront", "checkout-redesign", "Plan the one-page checkout", 60 * 26, "Sketch a plan for moving checkout to a single page. Keep the current payment provider.", "I'll list what each current step collects before proposing a layout.", "The four steps collect contact, shipping, delivery and payment. They fit on one page as collapsible sections, validated as each one is completed.", { pinned: true }),
  {
    project: "storefront",
    slug: "discount-rounding",
    title: "Fix a cent off in discounted cart totals",
    minutesAgo: 12,
    tags: ["bug"],
    screenshot: "desktop",
    turns: [
      { user: "A few customers say their total is a cent off when a percentage discount applies to several items. Can you find out why and fix it?" },
      {
        thinking: "A cent off with several items smells like rounding each line instead of the total. I'll find where the discount is applied.",
        tools: [
          { bash: "rg -n \"applyDiscount|cartTotal\" src", output: "src/cart/totals.ts:18:export function applyDiscount(lines: CartLine[], percent: number) {\nsrc/cart/totals.ts:39:export function cartTotal(cart: Cart): number {\nsrc/cart/totals.ts:40:  const discounted = applyDiscount(cart.lines, cart.discountPercent);\nsrc/checkout/summary.tsx:27:  const total = cartTotal(order);" },
          { read: "src/cart/totals.ts", output: "export function applyDiscount(lines: CartLine[], percent: number) {\n  return lines.map((line) => ({\n    ...line,\n    total: roundToCents(line.price * line.quantity * (1 - percent / 100)),\n  }));\n}" }
        ]
      },
      {
        thinking: "Every line is rounded to whole cents before the lines are added up. I'll reproduce it with a cart of three lines first.",
        tools: [
          { bash: "npx vitest run src/cart", exitCode: 1, output: " FAIL  src/cart/totals.test.ts > cartTotal > rounds a discounted cart once\nAssertionError: expected 21.18 to be 21.17\n ❯ src/cart/totals.test.ts:58:29\n\n Test Files  1 failed (1)\n      Tests  1 failed | 11 passed (12)" }
        ]
      },
      {
        thinking: "Reproduced: each line rounds 0.5 cents up, so the cart comes out a cent high. I'll sum in cents and round once.",
        tools: [
          { edit: "src/cart/totals.ts", diff: "    ...\n 38 \n 39 export function cartTotal(cart: Cart): number {\n-40   const discounted = applyDiscount(cart.lines, cart.discountPercent);\n-41   return discounted.reduce((sum, line) => sum + line.total, 0);\n+40   // Rounding each line first can leave the cart a cent off.\n+41   const subtotal = cart.lines.reduce((sum, line) => sum + toCents(line.price) * line.quantity, 0);\n+42   return fromCents(Math.round(subtotal * (1 - cart.discountPercent / 100)));\n 42 }\n    ..." },
          { bash: "npx vitest run src/cart src/checkout", output: " ✓ src/cart/totals.test.ts (12 tests) 9ms\n ✓ src/checkout/summary.test.tsx (7 tests) 41ms\n\n Test Files  2 passed (2)\n      Tests  19 passed (19)\n   Start at  21:20:31\n   Duration  1.18s" }
        ]
      },
      { text: "Fixed. `cartTotal` rounded each discounted line to whole cents before adding them up. Lines ending in half a cent all rounded up, so the total came out a cent high.\n\n- **Fix:** sum in cents and round once, after the discount.\n- **Test:** €12.50 + €8.30 + €4.10 at 15% off now totals €21.17, not €21.18." }
    ]
  },
  {
    project: "homelab",
    slug: "backup",
    title: "Why did the nightly backup stop?",
    minutesAgo: 48,
    tags: ["urgent"],
    screenshot: "mobile",
    turns: [
      { user: "The nightly restic backup hasn’t run since Tuesday. Can you check what’s going on?" },
      {
        thinking: "I'll check whether the timer still fires and what the last runs logged.",
        tools: [
          { bash: "systemctl --user list-timers restic-backup.timer --no-pager", output: "NEXT                        LEFT     LAST                        PASSED  UNIT\nThu 2026-10-08 03:00:00 CEST 9h left  Wed 2026-10-07 03:00:04 CEST 15h ago restic-backup.timer" },
          { bash: "journalctl --user -u restic-backup.service -n 3 --no-pager", output: "Oct 07 03:00:04 atlas restic[48213]: Fatal: unable to open repository at sftp:nas:/volume1/backups\nOct 07 03:00:04 atlas restic[48213]: ssh: handshake failed: knownhosts: key mismatch\nOct 07 03:00:04 atlas systemd[1022]: restic-backup.service: Failed with result 'exit-code'." }
        ]
      },
      { text: "Every run since Tuesday fails to reach the NAS:\n\n```\nknownhosts: key mismatch\n```\n\nThe NAS got a new SSH host key with Tuesday’s system update. If that was you, I’ll trust the new key and rerun the backup." },
      { user: "Yes, I updated it on Tuesday. Go ahead." },
      {
        thinking: "I'll swap the host key, then start the service and check the new snapshot.",
        tools: [
          { bash: "ssh-keygen -R nas && ssh-keyscan -t ed25519 nas >> ~/.ssh/known_hosts", output: "# Host nas found: line 4\n/home/alex/.ssh/known_hosts updated.\n# nas:22 SSH-2.0-OpenSSH_9.8" },
          { bash: "systemctl --user start restic-backup.service && restic snapshots --latest 1", output: "ID        Time                 Host   Paths   Size\n4f1c2a9d  2026-10-07 18:12:40  atlas  /home   38.2 GiB" }
        ]
      },
      { text: "Done, the backup ran:\n\n- **Snapshot:** `4f1c2a9d`, 38.2 GiB\n- **Next run:** tonight at 03:00" }
    ]
  },
  oneReply("storefront", "order-emails", "Add dark mode to order emails", 95, "Our order confirmation emails look broken in dark mode on iOS Mail. Can you fix them?", "I'll check how the email template sets its colours.", "The template hard-codes white backgrounds on table cells. I added a `prefers-color-scheme: dark` block and dark-safe logo variant, and checked both modes in the preview.", { unread: true }),
  oneReply("dotfiles", "starship", "Move the zsh prompt to Starship", 170, "Replace my hand-written zsh prompt with Starship, but keep the git branch and exit code.", "I'll read the current prompt function so nothing it shows gets lost.", "Starship now draws the prompt. The git branch, dirty marker and last exit code stay, and the prompt renders in 9 ms instead of 70 ms."),
  oneReply("homelab", "grafana-password", "Rotate the Grafana admin password", 320, "Rotate the Grafana admin password and store the new one in pass.", "I'll change it through the Grafana CLI so no dashboards are touched.", "The admin password is rotated and stored as `homelab/grafana-admin` in pass. The old one no longer logs in."),
  oneReply("storefront", "pr-482", "Review PR #482: inventory sync retries", 60 * 20, "Review PR #482. Focus on how retries behave when the warehouse API is down for a long time.", "I'll read the retry loop and its tests before commenting.", "The retries back off correctly, but nothing caps the total wait, so a long outage queues jobs forever. I suggested a dead-letter queue after 24 hours.", { tags: ["review"], unread: true }),
  oneReply("storefront", "gift-cards", "Expire unused gift cards after 3 years", 60 * 22, "Write a migration that expires gift cards unused for 3 years.", "I'll check how gift card balances are stored first.", "Added a migration and a nightly job. Cards unused for 3 years get an `expired_at` date and keep their balance for support to restore."),
  oneReply("homelab", "tailscale-nas", "Set up Tailscale on the NAS", 60 * 23, "Put the NAS on my tailnet so backups work from anywhere.", "I'll check which Tailscale package the NAS supports.", "The NAS is on the tailnet as `nas`, with key expiry turned off. SSH and the backup share now work from outside the house."),
  oneReply("field-notes", "pi-on-a-server", "Draft: running Pi on a home server", 60 * 46, "Draft a short blog post about running Pi on my home server.", "I'll outline the setup before writing prose.", "Drafted the post in `posts/pi-on-a-server.md`: why a home server, the Tailscale setup, and what I’d do differently."),
  oneReply("storefront", "ci-cache", "Speed up CI dependency caching", 60 * 24 * 3, "The test workflow spends too long installing unchanged dependencies.", "I'll tie the cache key to the lockfile, not the commit.", "The cache key now uses the lockfile hash. Installs take 8 seconds instead of 2 minutes when dependencies don’t change."),
  oneReply("dotfiles", "neovim-lsp", "Fix Neovim LSP warnings after plugin update", 60 * 24 * 4, "Neovim shows deprecation warnings since I updated plugins.", "I'll find which plugin calls the deprecated API.", "The warnings came from the old `lspconfig` setup calls. I moved them to `vim.lsp.config`, and Neovim starts clean."),
  oneReply("field-notes", "talk-outline", "Outline a talk on coding agents", 60 * 24 * 6, "Outline a 20-minute talk on working with coding agents day to day.", "I'll structure it around three real sessions.", "The outline has three parts: delegating a bug hunt, reviewing the agent’s work, and when to stop and do it yourself.")
];
