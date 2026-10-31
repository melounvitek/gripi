// Holds every tool result and `!` command of a session running on a remote worker until Gripi's
// helper confirms that main has mirrored the worker's changes, so the session is never ahead of main.
import net from "node:net";
import { createLocalBashOperations, SettingsManager, type ExtensionAPI } from "@earendil-works/pi-coding-agent";

const SOCKET_PATH = process.env.GRIPI_WORKER_SOCKET;
const ANSWER_TIMEOUT_MS = Number(process.env.GRIPI_WORKER_TIMEOUT_MS) || 60_000;
// Pi's abort waits for held results, so Gripi's abort timeout (rpc/client.go) must stay longer.
const ABORT_GRACE_MS = 5_000;

type Answer = { ok: true } | { ok: false; error: string };
type Connection = { socket: net.Socket; pending: Map<number, (answer: Answer) => void> };

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

export default function (pi: ExtensionAPI) {
  if (!SOCKET_PATH) return;
  const socketPath = SOCKET_PATH;

  let connection: Connection | undefined;
  let lastId = 0;
  let shutDown = false;

  function connect(): Connection {
    const socket = net.createConnection(socketPath);
    const opened: Connection = { socket, pending: new Map() };
    let buffer = "";
    let closeReason = "the connection closed";
    socket.setEncoding("utf8");
    socket.on("data", (chunk: string) => {
      buffer += chunk;
      let newline;
      while ((newline = buffer.indexOf("\n")) >= 0) {
        const line = buffer.slice(0, newline);
        buffer = buffer.slice(newline + 1);
        let message;
        try {
          message = JSON.parse(line);
        } catch {
          socket.destroy(new Error("unreadable answer"));
          return;
        }
        opened.pending.get(message?.id)?.(message.ok === true ? { ok: true } : { ok: false, error: String(message.error || "refused") });
      }
    });
    socket.on("error", (error) => { closeReason = error.message; });
    socket.on("close", () => {
      if (connection === opened) connection = undefined;
      for (const finish of opened.pending.values()) finish({ ok: false, error: `Gripi's worker helper is unreachable: ${closeReason}` });
    });
    return opened;
  }

  // Never rejects: every failure becomes an answer that isn't ok.
  function request(type: "barrier" | "health"): Promise<Answer> {
    if (!connection?.socket.writable) connection = connect();
    const { socket, pending } = connection;
    const id = ++lastId;
    return new Promise((resolve) => {
      const timer = setTimeout(() => finish({ ok: false, error: `no answer within ${ANSWER_TIMEOUT_MS / 1000} s` }), ANSWER_TIMEOUT_MS);
      function finish(answer: Answer) {
        clearTimeout(timer);
        pending.delete(id);
        if (shutDown && pending.size === 0) socket.end();
        resolve(answer);
      }
      pending.set(id, finish);
      socket.write(`${JSON.stringify({ id, type })}\n`);
    });
  }

  let sent: Promise<Answer> | undefined;
  let queued: Promise<Answer> | undefined;

  function sendBarrier(): Promise<Answer> {
    const barrier = request("barrier").then((answer) => {
      if (sent === barrier) sent = undefined;
      return answer;
    });
    sent = barrier;
    return barrier;
  }

  // A barrier covers every change made before it was sent, so callers that arrive while one is in
  // flight share the next one.
  function barrier(): Promise<Answer> {
    if (!sent) return sendBarrier();
    queued ??= sent.then(() => {
      queued = undefined;
      return sent ?? sendBarrier();
    });
    return queued;
  }

  function untilAborted(answer: Promise<Answer>, signal: AbortSignal | undefined, graceMs: number): Promise<Answer> {
    if (!signal) return answer;
    return new Promise((resolve) => {
      let grace: NodeJS.Timeout | undefined;
      const startGrace = () => {
        grace = setTimeout(() => resolve({ ok: false, error: "aborted before the helper answered" }), graceMs);
      };
      if (signal.aborted) startGrace();
      else signal.addEventListener("abort", startGrace, { once: true });
      answer.then((result) => {
        clearTimeout(grace);
        signal.removeEventListener("abort", startGrace);
        resolve(result);
      });
    });
  }

  // After an abort, keeps waiting for a short grace period, so the session doesn't get ahead of main.
  const hold = (signal: AbortSignal | undefined) => untilAborted(barrier(), signal, ABORT_GRACE_MS);

  // Set when a result was released without main's confirmation, until the helper reports that the
  // mirror is healthy again.
  let failure: { reason: string } | undefined;

  function failClosed(reason: string): string {
    failure = { reason };
    return `[Gripi] This ran on the worker, but main did not confirm its file changes (${reason}). Treat it as failed: the changes may be missing on main.`;
  }

  pi.on("session_shutdown", () => {
    shutDown = true;
    if (connection?.pending.size === 0) connection.socket.end();
  });

  // Pi checks for an abort only after this handler, so the health check must not outlast one.
  pi.on("tool_call", async (_event, ctx) => {
    try {
      const checked = failure;
      if (!checked) return undefined;
      const health = await untilAborted(request("health"), ctx.signal, 0);
      if (health.ok) {
        // A failure while the health check was pending needs a check of its own.
        if (failure === checked) failure = undefined;
        if (!failure) return undefined;
      }
      return {
        block: true,
        reason: `[Gripi] Not run: main did not confirm earlier changes from this worker (${(failure ?? checked).reason}), and the mirror to main is still not working. Stop and tell the user.`,
      };
    } catch (error) {
      return { block: true, reason: `[Gripi] Not run: ${errorMessage(error)}` };
    }
  });

  // Pi swallows errors thrown here and the model sees success, so every failure must be returned.
  // Never touch ctx after waiting: after a reload it throws, and Pi swallows that too.
  pi.on("tool_result", async (event, ctx) => {
    try {
      // The outer call's hold covers calls made by other tools.
      if (event.parentToolCallId) return undefined;
      const answer = await hold(ctx.signal);
      if (answer.ok) return undefined;
      return {
        isError: true,
        content: [{ type: "text" as const, text: `${failClosed(answer.error)} Original output:\n` }, ...event.content],
      };
    } catch (error) {
      return { isError: true, content: [{ type: "text" as const, text: failClosed(errorMessage(error)) }] };
    }
  });

  // Throwing from `!` commands would lose them from the session, so failures go into their output.
  pi.on("user_bash", () => {
    try {
      // Pi's own settings, as its local `!` commands use them.
      const shellPath = SettingsManager.inMemory(pi.getSettings()).getShellPath();
      const local = createLocalBashOperations({ shellPath });
      return {
        operations: {
          async exec(command, cwd, options) {
            let exitCode: number | null = 1;
            let reason: string;
            try {
              try {
                ({ exitCode } = await local.exec(command, cwd, options));
              } catch (error) {
                if (!options.signal?.aborted) options.onData(Buffer.from(`${errorMessage(error)}\n`));
              }
              const answer = await hold(options.signal);
              if (answer.ok) return { exitCode };
              reason = answer.error;
            } catch (error) {
              reason = errorMessage(error);
            }
            options.onData(Buffer.from(`\n${failClosed(reason)}\n`));
            return { exitCode: exitCode || 1 };
          },
        },
      };
    } catch (error) {
      const output = `[Gripi] Not run: ${errorMessage(error)}\n`;
      return { result: { output, exitCode: 1, cancelled: false, truncated: false } };
    }
  });
}
