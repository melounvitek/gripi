import { VoiceConversation } from "./voice_conversation.js";

const LABELS = { listening: "listening", transcribing: "transcribing", thinking: "hermes is working", speaking: "speaking" };
const HINTS = { listening: "speak, pause to send", speaking: "talk to interrupt" };

// Owns a Hermes session's voice button, the call view in the conversation's place and the call line with its controls,
// and the voice conversation they run.
export class VoiceCallController {
  constructor(window, { send, busy, messages, failed }) {
    this.window = window;
    this.send = send;
    this.busy = busy;
    this.messages = messages;
    this.failed = failed;
    this.conversation = null;
    this.elements = null;
  }

  bind(form) {
    const button = form?.querySelector("[data-voice-button]");
    const line = form?.querySelector("[data-voice-call]");
    const view = this.window.document.querySelector("[data-voice-call-view]");
    this.elements = button && line && view ? {
      form,
      button,
      line,
      view,
      conversation: this.window.document.getElementById("conversation-scroll"),
      inputRow: form.querySelector(".composer-input-row"),
      bars: view.querySelector("[data-voice-call-bars]"),
      state: view.querySelector("[data-voice-call-state]"),
      note: view.querySelector("[data-voice-call-note]"),
      caption: view.querySelector("[data-voice-call-sentence]"),
      mute: line.querySelector("[data-voice-call-mute]"),
      end: line.querySelector("[data-voice-call-end]")
    } : null;
    if (!this.elements) return;

    const { navigator, MediaRecorder, isSecureContext } = this.window;
    button.hidden = !isSecureContext || !navigator.mediaDevices?.getUserMedia || !MediaRecorder;
    button.addEventListener("click", () => this.start());
    this.elements.mute.addEventListener("click", () => this.conversation?.setMuted(!this.conversation.muted));
    this.elements.end.addEventListener("click", () => this.end());
  }

  // Whether the call waits for Hermes's reply.
  get waiting() {
    return this.conversation?.state === "thinking";
  }

  start() {
    if (this.conversation) return;
    this.conversation = new VoiceConversation({ window: this.window, send: this.send, onChange: (change) => this.render(change) });
    // Nothing may be awaited before this: iOS plays only audio started within the tap.
    this.conversation.start({ working: () => (this.busy() ? lastSaid(this.messages()) : null) });
  }

  replyFinished(text) {
    this.conversation?.replyFinished(text);
  }

  // Ends the wait with the page of an idle Hermes, for a refresh that skipped the events that would end it. The page must
  // hold the awaited message, because a page from before Hermes took it shows an older turn. A turn that ended without a
  // reply, such as a stopped one, ends the wait with nothing to say.
  replyFinishedOnPage() {
    const messages = this.messages();
    if (!this.waiting || lastSaid(messages) !== this.conversation.awaited) return;
    const last = messages.at(-1);
    if (last?.dataset.finalAssistantResponse !== "true") {
      this.replyFinished("");
      return;
    }
    const reply = last.querySelector(".message-body").cloneNode(true);
    // Code and tables are left out, as speaking a reply's Markdown leaves them out.
    reply.querySelectorAll("pre, table, .code-block-copy-button").forEach((element) => element.remove());
    // A copy has no layout to separate its blocks, so each one ends a paragraph.
    reply.querySelectorAll("p, li, h1, h2, h3, h4, h5, h6").forEach((block) => block.append("\n\n"));
    this.replyFinished(reply.textContent);
  }

  end() {
    this.conversation?.end();
  }

  render({ state, level, muted, error, sentence }) {
    const { form, button, line, view, conversation, inputRow, bars, note, caption, mute, end } = this.elements;
    const active = state !== "off";
    const focused = this.window.document.activeElement;
    form.toggleAttribute("data-voice-call", active);
    line.hidden = view.hidden = !active;
    // The covered conversation can't be focused, also by the composer's own focus handling while Hermes works.
    conversation.inert = active;
    inputRow.hidden = active;
    if (!active) {
      this.conversation = null;
      delete view.dataset.state;
      if (line.contains(focused)) button.focus();
      if (error) this.failed(error);
      return;
    }
    if (focused === button) end.focus();

    if (view.dataset.state !== state) {
      view.dataset.state = state;
      this.elements.state.textContent = LABELS[state];
    }
    bars.style.setProperty("--level", Math.sqrt(level).toFixed(3));
    // Only changes are set, so that screen readers announce nothing at every level.
    const noteText = error || (muted ? "muted" : HINTS[state] || "");
    if (note.textContent !== noteText) note.textContent = noteText;
    note.classList.toggle("is-error", !!error);
    if (caption.textContent !== (sentence ?? "")) caption.textContent = sentence ?? "";
    mute.textContent = muted ? "unmute" : "mute";
  }
}

function lastSaid(messages) {
  return messages.findLast((message) => message.dataset.role === "user")?.querySelector(".message-body")?.textContent.trim() ?? "";
}
