// Scripted model provider for Pi extension tests; it never touches the network.
// A prompt `RUN [[call, ...], ...]` makes the model send each list of tool calls in turn, as parallel
// calls of one response. After the last one it replies with the tool results it received, as JSON.
// Its `nested` tool runs a bash command through another tool call, as codemode does.
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";
import { createFauxCore, fauxAssistantMessage, fauxText, fauxToolCall, type ToolCall } from "@earendil-works/pi-ai";
import { Type } from "typebox";

function text(content: unknown): string {
  if (typeof content === "string") return content;
  if (!Array.isArray(content)) return "";
  return content.filter((part) => part?.type === "text").map((part) => part.text).join("");
}

function respond(context: { messages: any[] }) {
  const messages = context.messages;
  const promptIndex = messages.findLastIndex((message) => message.role === "user");
  const script = /^RUN (.*)$/s.exec(text(messages[promptIndex]?.content))?.[1];
  if (!script) return fauxAssistantMessage(fauxText("plain reply"));

  const steps: Pick<ToolCall, "name" | "arguments">[][] = JSON.parse(script);
  const since = messages.slice(promptIndex + 1);
  const step = since.filter((message) => message.role === "assistant").length;
  if (step < steps.length) {
    const calls = steps[step].map((call, index) => fauxToolCall(call.name, call.arguments, { id: `call_${step}_${index}` }));
    return fauxAssistantMessage(calls, { stopReason: "toolUse" });
  }
  const results = since
    .filter((message) => message.role === "toolResult")
    .map((message) => ({ id: message.toolCallId, isError: message.isError, text: text(message.content) }));
  return fauxAssistantMessage(fauxText(JSON.stringify(results)));
}

export default function (pi: ExtensionAPI) {
  pi.registerTool({
    name: "nested",
    label: "Nested",
    description: "Runs a bash command through another tool call",
    parameters: Type.Object({ command: Type.String() }),
    async execute(_toolCallId, params, signal, _onUpdate, ctx) {
      const { result } = await ctx.executeTool("bash", { command: params.command }, { signal });
      return { content: result.content, details: undefined };
    },
  });

  const core = createFauxCore({ provider: "fake", api: "fake-api", models: [{ id: "fake-1" }] });
  pi.registerProvider("fake", {
    baseUrl: "http://127.0.0.1:9",
    apiKey: "not-a-secret",
    api: core.api as any,
    streamSimple: (model, context, options) => {
      if (core.getPendingResponseCount() === 0) core.appendResponses([respond]);
      return core.streamSimple(model, context, options);
    },
    models: [{
      id: "fake-1",
      name: "Fake 1",
      reasoning: false,
      input: ["text"],
      cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
      contextWindow: 100_000,
      maxTokens: 4096,
    }],
  });
}
