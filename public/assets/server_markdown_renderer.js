import { enhanceMarkdownCodeBlocks } from "./dom.js";

export class ServerMarkdownRenderer {
  constructor(document, conversationController) {
    this.document = document;
    this.conversationController = conversationController;
    this.epoch = 0;
    this.jobs = new Map();
  }

  bind() {
    this.epoch += 1;
    this.jobs.forEach((job) => this.cancel(job));
    this.jobs.clear();
  }

  render(body, text, delay = 120) {
    this.cancel(this.jobs.get(body));
    body.dataset.plainText = text;
    body.dataset.rendering = "pending";

    const job = { body, text, epoch: this.epoch, timer: null, controller: null };
    job.timer = setTimeout(() => this.request(job), delay);
    this.jobs.set(body, job);
  }

  async request(job) {
    job.timer = null;
    if (!this.current(job)) return;

    job.controller = new AbortController();
    const formData = new FormData();
    formData.set("text", job.text);
    try {
      const response = await fetch("/markdown", { method: "POST", body: formData, signal: job.controller.signal });
      if (!response.ok) return this.fail(job);
      if (!this.current(job)) return;
      const payload = await response.json();
      if (!this.current(job)) return;

      job.body.innerHTML = payload.html;
      enhanceMarkdownCodeBlocks(job.body, this.document);
      delete job.body.dataset.rendering;
      this.jobs.delete(job.body);
      if (this.conversationController.autoScrollEnabled && job.body.closest(".message") === this.conversationController.latestReadableAssistantMessage()) {
        this.conversationController.scheduleAutoScroll();
      }
    } catch (error) {
      if (error?.name !== "AbortError") this.fail(job);
    }
  }

  fail(job) {
    if (!this.current(job)) return;
    job.body.textContent = job.text;
    delete job.body.dataset.rendering;
    this.jobs.delete(job.body);
  }

  current(job) {
    return job.epoch === this.epoch && this.jobs.get(job.body) === job && job.body.dataset.plainText === job.text;
  }

  cancel(job) {
    if (!job) return;
    if (job.timer !== null) clearTimeout(job.timer);
    job.controller?.abort();
  }
}
