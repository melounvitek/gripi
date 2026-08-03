import {
  SettingsManager,
  type ExtensionAPI,
  type ExtensionCommandContext,
  type SessionEntry,
  type SessionTreeNode,
} from "@earendil-works/pi-coding-agent";

type RequestPayload = Record<string, unknown>;
type BridgeHandler = (payload: RequestPayload, ctx: ExtensionCommandContext) => Promise<RequestPayload> | RequestPayload;

const SUMMARY_MODES = new Set(["none", "default", "custom"]);
const TREE_FILTER_MODES = new Set(["default", "no-tools", "user-only", "labeled-only", "all"]);
const SETTINGS_ENTRY_TYPES = new Set(["label", "custom", "model_change", "thinking_level_change", "session_info"]);
const TREE_ENTRY_LIMIT = 1_000;
// Leave headroom below 1 MiB for settings and the bridge response envelope.
const TREE_ENTRIES_BYTES = 900 * 1024;
const TREE_PREVIEW_BYTES = 512;
const TREE_METADATA_BYTES = 1_024;
const ENTRY_ID_BYTES = 1024;
const LABEL_BYTES = 4096;
const CUSTOM_INSTRUCTIONS_BYTES = 64 * 1024;

type TreeFilterMode = "default" | "no-tools" | "user-only" | "labeled-only" | "all";

type ProjectedTreeEntry = {
  entryId: string;
  parentId: string | null;
  depth: number;
  type: string;
  role: string;
  text: string;
  timestamp: string;
  current: boolean;
  latest: boolean;
  messageKind?: "user" | "assistant-final";
  label?: string;
  labelTimestamp?: string;
};

type TreeSnapshotPayload = {
  entries: ProjectedTreeEntry[];
  leafId: string | null;
  truncated: boolean;
  totalEntries: number;
};

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

function boundedText(value: unknown, byteLimit: number): string {
  const text = String(value ?? "");
  if (Buffer.byteLength(text, "utf8") <= byteLimit) return text;

  const omission = "…";
  const bytes = Buffer.from(text, "utf8");
  let end = byteLimit - Buffer.byteLength(omission, "utf8");
  while (end > 0) {
    try {
      return `${new TextDecoder("utf-8", { fatal: true }).decode(bytes.subarray(0, end))}${omission}`;
    } catch {
      end -= 1;
    }
  }
  return omission;
}

function boundedMetadata(value: unknown): string | null {
  return value === null || value === undefined ? null : boundedText(value, TREE_METADATA_BYTES);
}

function previewText(value: unknown): string {
  return boundedText(String(value ?? "").replace(/\s+/g, " ").trim(), TREE_PREVIEW_BYTES);
}

function assistantTextPhase(part: { textSignature?: unknown }): "commentary" | "final_answer" | undefined {
  if (typeof part.textSignature !== "string" || !part.textSignature.startsWith("{")) return undefined;

  try {
    const parsed: unknown = JSON.parse(part.textSignature);
    if (!parsed || typeof parsed !== "object" || !("v" in parsed) || parsed.v !== 1 || !("id" in parsed) || typeof parsed.id !== "string") return undefined;
    if (!("phase" in parsed) || (parsed.phase !== "commentary" && parsed.phase !== "final_answer")) return undefined;
    return parsed.phase;
  } catch {
    return undefined;
  }
}

function messageKind(entry: SessionEntry): ProjectedTreeEntry["messageKind"] {
  if (entry.type !== "message") return undefined;
  if (entry.message.role === "user") return "user";
  if (entry.message.role !== "assistant") return undefined;

  const stopReason = "stopReason" in entry.message ? entry.message.stopReason : undefined;
  if (stopReason !== undefined && stopReason !== "stop" && stopReason !== "length") return undefined;
  const content = entry.message.content;
  const parts = Array.isArray(content) ? content : [content];
  const finalText = parts
    .filter((part): part is string | { type: "text"; text: string; textSignature?: unknown } =>
      typeof part === "string" || !!part && typeof part === "object" && "type" in part && part.type === "text" && "text" in part && typeof part.text === "string")
    .filter((part) => typeof part === "string" || assistantTextPhase(part) !== "commentary")
    .map((part) => typeof part === "string" ? part : part.text)
    .join("\n")
    .trim();
  return finalText ? "assistant-final" : undefined;
}

function entryRole(entry: SessionEntry): string {
  if (entry.type === "message") return entry.message.role || "message";
  if (entry.type === "custom_message") return "custom";
  if (entry.type === "branch_summary") return "summary";
  if (entry.type === "compaction") return "compact";
  return entry.type;
}

function entryText(entry: SessionEntry): string {
  if (entry.type === "message") return contentText(entry.message.content);
  if (entry.type === "custom_message") return contentText(entry.content);
  if (entry.type === "branch_summary" || entry.type === "compaction") return entry.summary;
  return "";
}

function visibleEntry(entry: SessionEntry, node: SessionTreeNode, leafId: string | null, filterMode: TreeFilterMode): boolean {
  if (entry.type === "message" && entry.message.role === "assistant" && entry.id !== leafId) {
    const hasText = contentText(entry.message.content).trim().length > 0;
    const stopReason = "stopReason" in entry.message ? entry.message.stopReason : undefined;
    const unusualStop = !!stopReason && stopReason !== "stop" && stopReason !== "toolUse";
    if (!hasText && !unusualStop) return false;
  }

  if (filterMode === "user-only") return entry.type === "message" && entry.message.role === "user";
  if (filterMode === "no-tools") {
    return !SETTINGS_ENTRY_TYPES.has(entry.type) && !(entry.type === "message" && entry.message.role === "toolResult");
  }
  if (filterMode === "labeled-only") return node.label !== undefined;
  if (filterMode === "all") return true;
  return !SETTINGS_ENTRY_TYPES.has(entry.type);
}

function activeSubtrees(roots: SessionTreeNode[], leafId: string | null): Map<SessionTreeNode, boolean> {
  const nodes: SessionTreeNode[] = [];
  const stack = [...roots].reverse();
  while (stack.length > 0) {
    const node = stack.pop()!;
    nodes.push(node);
    for (let index = node.children.length - 1; index >= 0; index -= 1) stack.push(node.children[index]);
  }

  const result = new Map<SessionTreeNode, boolean>();
  for (let index = nodes.length - 1; index >= 0; index -= 1) {
    const node = nodes[index];
    result.set(node, leafId !== null && (node.entry.id === leafId || node.children.some((child) => result.get(child))));
  }
  return result;
}

function activeFirst(nodes: SessionTreeNode[], active: Map<SessionTreeNode, boolean>): SessionTreeNode[] {
  return [...nodes.filter((node) => active.get(node)), ...nodes.filter((node) => !active.get(node))];
}

function projectedEntry(entry: SessionEntry, node: SessionTreeNode, parentId: string | null, depth: number): ProjectedTreeEntry {
  const projected: ProjectedTreeEntry = {
    entryId: boundedMetadata(entry.id)!,
    parentId: boundedMetadata(parentId),
    depth,
    type: boundedMetadata(entry.type)!,
    role: boundedMetadata(entryRole(entry))!,
    text: previewText(entryText(entry)),
    timestamp: boundedMetadata(entry.timestamp)!,
    current: false,
    latest: false,
  };
  const kind = messageKind(entry);
  if (kind) projected.messageKind = kind;
  if (node.label !== undefined) projected.label = boundedText(node.label, TREE_PREVIEW_BYTES);
  if (node.labelTimestamp !== undefined) projected.labelTimestamp = boundedMetadata(node.labelTimestamp)!;
  return projected;
}

function serializedEntryBytes(entry: ProjectedTreeEntry): number {
  return Buffer.byteLength(JSON.stringify(entry), "utf8");
}

function retainMarkedEntries(entries: ProjectedTreeEntry[], markedEntries: Array<ProjectedTreeEntry | null>): void {
  const marked = [...new Map(markedEntries.filter((entry): entry is ProjectedTreeEntry => entry !== null).map((entry) => [entry.entryId, entry])).values()];
  const markedIds = new Set(marked.map((entry) => entry.entryId));
  const retainedIds = new Set(entries.map((entry) => entry.entryId));
  const missing = marked.filter((entry) => !retainedIds.has(entry.entryId));
  const missingBytes = missing.reduce((total, entry) => total + serializedEntryBytes(entry), 0);
  let entriesBytes = entries.reduce((total, entry) => total + serializedEntryBytes(entry), 0);
  while (entries.length + missing.length > TREE_ENTRY_LIMIT || entriesBytes + missingBytes > TREE_ENTRIES_BYTES) {
    let removableIndex = entries.length - 1;
    while (removableIndex >= 0 && markedIds.has(entries[removableIndex].entryId)) removableIndex -= 1;
    if (removableIndex < 0) break;
    entriesBytes -= serializedEntryBytes(entries[removableIndex]);
    entries.splice(removableIndex, 1);
  }
  entries.push(...missing.map((entry) => ({ ...entry })));

  const finalIds = new Set(entries.map((entry) => entry.entryId));
  const depths = new Map<string, number>();
  for (const entry of entries) {
    if (entry.parentId === null || !finalIds.has(entry.parentId) || !depths.has(entry.parentId)) entry.parentId = null;
    entry.depth = entry.parentId === null ? 0 : depths.get(entry.parentId)! + 1;
    depths.set(entry.entryId, entry.depth);
  }
}

function projectTree(tree: SessionTreeNode[], leafId: string | null, filterMode: TreeFilterMode): TreeSnapshotPayload {
  const visible: ProjectedTreeEntry[] = [];
  let visibleBytes = 0;
  let totalEntries = 0;
  let currentProjection: ProjectedTreeEntry | null = null;
  let latestProjection: ProjectedTreeEntry | null = null;
  let latestTimestamp: string | null = null;
  const active = activeSubtrees(tree, leafId);
  const stack: Array<[SessionTreeNode, string | null, number, ProjectedTreeEntry | null]> = activeFirst(tree, active)
    .reverse()
    .map((node) => [node, null, 0, null]);

  while (stack.length > 0) {
    const [node, visibleParentId, visibleDepth, visibleAncestor] = stack.pop()!;
    const entry = node.entry;
    const isVisible = visibleEntry(entry, node, leafId, filterMode);
    const projection = isVisible ? projectedEntry(entry, node, visibleParentId, visibleDepth) : null;
    const nearestVisible = projection ?? visibleAncestor;
    if (entry.id === leafId) currentProjection = nearestVisible;
    if (entry.id && (latestTimestamp === null || entry.timestamp >= latestTimestamp)) {
      latestProjection = nearestVisible;
      latestTimestamp = entry.timestamp;
    }

    const childParentId = isVisible ? entry.id : visibleParentId;
    const childDepth = isVisible ? visibleDepth + 1 : visibleDepth;
    for (const child of activeFirst(node.children, active).reverse()) {
      stack.push([child, childParentId, childDepth, nearestVisible]);
    }
    if (!projection) continue;

    totalEntries += 1;
    const projectionBytes = serializedEntryBytes(projection);
    if (visible.length < TREE_ENTRY_LIMIT && visibleBytes + projectionBytes <= TREE_ENTRIES_BYTES) {
      visible.push(projection);
      visibleBytes += projectionBytes;
    }
  }

  retainMarkedEntries(visible, [currentProjection, latestProjection]);
  const currentId = currentProjection?.entryId;
  const latestId = latestProjection?.entryId;
  for (const entry of visible) {
    entry.current = entry.entryId === currentId;
    entry.latest = entry.entryId === latestId;
  }
  return {
    entries: visible,
    leafId: boundedMetadata(leafId),
    truncated: totalEntries > visible.length,
    totalEntries,
  };
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

  pi.registerCommand("gripi_reload", {
    description: "Reload Pi resources from Gripi",
    handler: async (args, ctx) => {
      const requestId = requestIdFrom(args);
      if (!requestId) return;
      const ui = ctx.ui;

      try {
        parsePayload(args);
        if (!ctx.isIdle()) throw new Error("Session is busy");

        await ctx.reload();
        ui.setStatus(`gripi_reload:${requestId}`, JSON.stringify({ ok: true }));
      } catch (error) {
        ui.setStatus(`gripi_reload:${requestId}`, JSON.stringify({ ok: false, error: errorMessage(error) }));
      }
    },
  });

  registerBridgeCommand(pi, "gripi_tree_navigate", "Navigate the current session tree from Gripi", async (requestPayload, ctx) => {
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

  registerBridgeCommand(pi, "gripi_tree_snapshot", "Report a bounded session tree snapshot to Gripi", (requestPayload, ctx) => {
    const requestedFilter = requestPayload.filter;
    if (requestedFilter !== undefined && (typeof requestedFilter !== "string" || !TREE_FILTER_MODES.has(requestedFilter))) {
      throw new Error("Invalid tree filter");
    }
    const effectiveFilter = (requestedFilter ?? settingsManager.getTreeFilterMode()) as TreeFilterMode;
    const snapshot = projectTree(ctx.sessionManager.getTree(), ctx.sessionManager.getLeafId(), effectiveFilter);
    return {
      ...snapshot,
      filter: effectiveFilter,
      settings: {
        treeFilterMode: settingsManager.getTreeFilterMode(),
        branchSummary: { skipPrompt: settingsManager.getBranchSummarySkipPrompt() },
      },
    };
  });

  registerBridgeCommand(pi, "gripi_tree_leaf", "Report the current session tree leaf to Gripi", (_requestPayload, ctx) => ({
    leafId: boundedMetadata(ctx.sessionManager.getLeafId()),
  }));

  registerBridgeCommand(pi, "gripi_tree_label", "Set or clear a native Pi tree label from Gripi", (requestPayload, ctx) => {
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
