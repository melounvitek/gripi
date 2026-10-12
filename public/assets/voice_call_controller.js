import { VoiceConversation } from "./voice_conversation.js";

const LABELS = { listening: "listening", transcribing: "transcribing", thinking: "hermes is working", speaking: "speaking" };
const HINTS = { listening: "speak, pause to send", speaking: "talk to interrupt" };
const METER_BARS = "▁▂▃▄▅▆▇█";
const METER_LENGTH = 12;

// Owns a Hermes session's voice button and call line, and the voice conversation they run.
export class VoiceCallController {
  constructor(window, { send, busy, failed }) {
    this.window = window;
    this.send = send;
    this.busy = busy;
    this.failed = failed;
    this.conversation = null;
    this.elements = null;
  }

  bind(form) {
    const button = form?.querySelector("[data-voice-button]");
    const line = form?.querySelector("[data-voice-call]");
    this.elements = button && line ? {
      form,
      button,
      line,
      inputRow: form.querySelector(".composer-input-row"),
      state: line.querySelector("[data-voice-call-state]"),
      meter: line.querySelector("[data-voice-call-meter]"),
      note: line.querySelector("[data-voice-call-note]"),
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

  // Names the call's wait for the reply to a turn Hermes is working on, or null when it isn't waiting.
  get awaitedReply() {
    return this.conversation?.awaitingReply ? this.conversation.generation : null;
  }

  start() {
    if (this.conversation) return;
    this.conversation = new VoiceConversation({ window: this.window, send: this.send, onChange: (change) => this.render(change) });
    // Nothing may be awaited before this: iOS plays only audio started within the tap.
    this.conversation.start({ busy: this.busy });
  }

  replyFinished(text) {
    this.conversation?.replyFinished(text);
  }

  // Ends the wait with the page's last message, for a refresh that skipped the events that would end it. Only a wait that
  // began before the refresh counts: an older page can't hold the reply to a later turn.
  replyFinishedOnPage(awaitedReply, lastMessage) {
    if (awaitedReply === null || awaitedReply !== this.awaitedReply) return;
    const reply = lastMessage?.dataset.finalAssistantResponse === "true" ? lastMessage.querySelector(".message-body").cloneNode(true) : null;
    // Code and tables are left out, as speaking a reply's Markdown leaves them out.
    reply?.querySelectorAll("pre, table, .code-block-copy-button").forEach((element) => element.remove());
    // A copy has no layout to separate its blocks, so each one ends a paragraph.
    reply?.querySelectorAll("p, li, h1, h2, h3, h4, h5, h6").forEach((block) => block.append("\n\n"));
    this.replyFinished(reply?.textContent ?? "");
  }

  end() {
    this.conversation?.end();
  }

  render({ state, level, muted, error }) {
    const { form, button, line, inputRow, meter, note, mute, end } = this.elements;
    const active = state !== "off";
    const focused = this.window.document.activeElement;
    form.toggleAttribute("data-voice-call", active);
    line.hidden = !active;
    inputRow.hidden = active;
    if (!active) {
      this.conversation = null;
      delete line.dataset.state;
      if (line.contains(focused)) button.focus();
      if (error) this.failed(error);
      return;
    }
    if (focused === button) end.focus();

    if (line.dataset.state !== state) {
      line.dataset.state = state;
      this.elements.state.textContent = LABELS[state];
      this.levels = [];
    }
    this.levels = muted ? [] : [...this.levels, level].slice(-METER_LENGTH);
    meter.textContent = this.levels.map((value) => METER_BARS[Math.round(Math.sqrt(value) * (METER_BARS.length - 1))]).join("").padStart(METER_LENGTH, METER_BARS[0]);
    // Only changes are set, so that screen readers announce nothing at every level.
    const noteText = error || (muted ? "" : HINTS[state] || "");
    if (note.textContent !== noteText) note.textContent = noteText;
    note.title = error || "";
    note.classList.toggle("is-error", !!error);
    mute.textContent = muted ? "unmute" : "mute";
  }
}
