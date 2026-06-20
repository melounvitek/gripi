import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";

export default function (pi: ExtensionAPI) {
  pi.registerCommand("pi_web_tree", {
    description: "Navigate the current session tree from pi-web-gateway",
    handler: async (args, ctx) => {
      const [entryId, requestId] = args.trim().split(/\s+/, 2);
      if (!entryId) {
        ctx.ui.notify("Tree entry id is required", "error");
        return;
      }

      const result = await ctx.navigateTree(entryId, { summarize: false });
      if (requestId) {
        ctx.ui.setStatus(`pi_web_tree:${requestId}`, result.cancelled ? "cancelled" : "ok");
      }
    },
  });

  pi.registerCommand("pi_web_tree_leaf", {
    description: "Report the current session tree leaf to pi-web-gateway",
    handler: async (args, ctx) => {
      const requestId = args.trim();
      if (!requestId) return;

      ctx.ui.setStatus(`pi_web_tree_leaf:${requestId}`, ctx.sessionManager.getLeafId() || "__root__");
    },
  });
}
