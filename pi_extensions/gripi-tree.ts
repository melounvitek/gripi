import {
  SettingsManager,
  type ExtensionAPI,
  type ExtensionCommandContext,
  type SessionEntry,
} from "@earendil-works/pi-coding-agent";

type RequestPayload = Record<string, unknown>;
type BridgeHandler = (payload: RequestPayload, ctx: ExtensionCommandContext) => Promise<RequestPayload> | RequestPayload;

const SUMMARY_MODES = new Set(["none", "default", "custom"]);
const ENTRY_ID_BYTES = 1024;
const LABEL_BYTES = 4096;
const CUSTOM_INSTRUCTIONS_BYTES = 64 * 1024;

function exceedsBytes(value: string, limit: number): boolean {
  return Buffer.byteLength(value, "utf8") > limit;
}

type NavigationPayload = RequestPayload & {
  entryId?: unknown;
  summary?: unknown;
  customInstructions?: unknown;
};

type LabelPayload = RequestPayload & {
  entryId?: unknown;
  label?: unknown;
};

function requestIdFrom(args: string): string | undefined {
  const requestId = args.trim().split(/\s+/, 1)[0];
  return requestId && /^[a-f0-9]+$/i.test(requestId) ? requestId : undefined;
}

function parsePayload(args: string): RequestPayload {
  const [, encodedPayload, ...extra] = args.trim().split(/\s+/);
  if (!encodedPayload || extra.length > 0) throw new Error("Invalid extension request payload");

  try {
    const payload = JSON.parse(Buffer.from(encodedPayload, "base64url").toString("utf8"));
    if (!payload || typeof payload !== "object" || Array.isArray(payload)) throw new Error();
    return payload as RequestPayload;
  } catch {
    throw new Error("Invalid extension request payload");
  }
}

function respond(ctx: ExtensionCommandContext, command: string, requestId: string, result: RequestPayload): void {
  ctx.ui.setStatus(`${command}:${requestId}`, JSON.stringify(result));
}

function errorMessage(error: unknown): string {
  return error instanceof Error && error.message ? error.message : "Extension command failed";
}

function contentText(content: unknown): string {
  if (typeof content === "string") return content;
  if (!Array.isArray(content)) return "";
  return content
    .filter((part): part is { type: "text"; text: string } =>
      !!part && typeof part === "object" && "type" in part && part.type === "text" && "text" in part && typeof part.text === "string")
    .map((part) => part.text)
    .join("");
}

function editorTextForEntry(entry: SessionEntry | undefined): string | undefined {
  if (entry?.type === "message" && entry.message.role === "user") return contentText(entry.message.content);
  if (entry?.type === "custom_message") return contentText(entry.content);
  return undefined;
}

function registerBridgeCommand(pi: ExtensionAPI, name: string, description: string, handler: BridgeHandler): void {
  pi.registerCommand(name, {
    description,
    handler: async (args, ctx) => {
      const requestId = requestIdFrom(args);
      if (!requestId) return;

      try {
        respond(ctx, name, requestId, { ok: true, ...await handler(parsePayload(args), ctx) });
      } catch (error) {
        respond(ctx, name, requestId, { ok: false, error: errorMessage(error) });
      }
    },
  });
}

export default function (pi: ExtensionAPI) {
  let settingsManager: SettingsManager;

  pi.on("session_start", (_event, ctx) => {
    settingsManager = SettingsManager.create(ctx.cwd, undefined, { projectTrusted: ctx.isProjectTrusted() });
  });

  registerBridgeCommand(pi, "gripi_tree_navigate", "Navigate the current session tree from GRIPi", async (requestPayload, ctx) => {
    if (!ctx.isIdle()) throw new Error("Session is busy");
    const payload = requestPayload as NavigationPayload;
    if (typeof payload.entryId !== "string" || !payload.entryId) throw new Error("Tree entry id is required");
    if (exceedsBytes(payload.entryId, ENTRY_ID_BYTES)) throw new Error("Tree entry id is too long");
    if (typeof payload.summary !== "string" || !SUMMARY_MODES.has(payload.summary)) throw new Error("Invalid summary mode");
    if (payload.summary === "custom" && (typeof payload.customInstructions !== "string" || !payload.customInstructions.trim())) {
      throw new Error("Custom summary instructions are required");
    }
    const customInstructions = payload.summary === "custom" ? (payload.customInstructions as string).trim() : undefined;
    if (customInstructions && exceedsBytes(customInstructions, CUSTOM_INSTRUCTIONS_BYTES)) throw new Error("Custom summary instructions are too long");

    const editorText = payload.entryId === ctx.sessionManager.getLeafId()
      ? undefined
      : editorTextForEntry(ctx.sessionManager.getEntry(payload.entryId));
    const result = await ctx.navigateTree(payload.entryId, {
      summarize: payload.summary !== "none",
      customInstructions,
    });
    return { cancelled: result.cancelled, editorText: result.cancelled ? undefined : editorText };
  });

  registerBridgeCommand(pi, "gripi_tree_settings", "Report effective tree settings to GRIPi", () => ({
    settings: {
      treeFilterMode: settingsManager.getTreeFilterMode(),
      branchSummary: { skipPrompt: settingsManager.getBranchSummarySkipPrompt() },
    },
  }));

  registerBridgeCommand(pi, "gripi_tree_label", "Set or clear a native Pi tree label from GRIPi", (requestPayload, ctx) => {
    if (!ctx.isIdle()) throw new Error("Session is busy");
    const payload = requestPayload as LabelPayload;
    if (typeof payload.entryId !== "string" || !payload.entryId) throw new Error("Tree entry id is required");
    if (exceedsBytes(payload.entryId, ENTRY_ID_BYTES)) throw new Error("Tree entry id is too long");
    if (!ctx.sessionManager.getEntry(payload.entryId)) throw new Error(`Tree entry not found: ${payload.entryId}`);
    if (payload.label !== null && typeof payload.label !== "string") throw new Error("Invalid label");
    const label = typeof payload.label === "string" ? payload.label.trim() || undefined : undefined;
    if (label && exceedsBytes(label, LABEL_BYTES)) throw new Error("Label is too long");
    pi.setLabel(payload.entryId, label);
    return { entryId: payload.entryId, label: label ?? null };
  });
}
