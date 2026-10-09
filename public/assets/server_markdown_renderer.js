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
    if (body.dataset.plainText === text && (this.jobs.has(body) || body.dataset.rendering !== "pending")) return;
    body.dataset.plainText = text;
    body.dataset.rendering = "pending";
    // A pending render picks up the latest text, so a streaming reply isn't restarted on every update.
    if (this.jobs.has(body)) return;

    const job = { body, epoch: this.epoch, timer: null, controller: null };
    job.timer = setTimeout(() => this.request(job), delay);
    this.jobs.set(body, job);
  }

  async request(job) {
    job.timer = null;
    if (!this.current(job)) return;

    const text = job.body.dataset.plainText;
    job.controller = new AbortController();
    const formData = new FormData();
    formData.set("text", text);
    try {
      const response = await fetch("/markdown", { method: "POST", body: formData, signal: job.controller.signal });
      if (!response.ok) return this.fail(job);
      if (!this.current(job)) return;
      const payload = await response.json();
      if (!this.current(job)) return;

      const previousHeight = job.body.offsetHeight;
      job.body.innerHTML = payload.html;
      enhanceMarkdownCodeBlocks(job.body, this.document);
      this.conversationController.afterMarkdownRender(job.body, previousHeight);
      if (job.body.dataset.plainText !== text) return this.request(job);
      delete job.body.dataset.rendering;
      this.jobs.delete(job.body);
    } catch (error) {
      if (error?.name !== "AbortError") this.fail(job);
    }
  }

  fail(job) {
    if (!this.current(job)) return;
    job.body.textContent = job.body.dataset.plainText;
    delete job.body.dataset.plainText;
    delete job.body.dataset.rendering;
    this.jobs.delete(job.body);
  }

  current(job) {
    return job.epoch === this.epoch && this.jobs.get(job.body) === job;
  }

  cancel(job) {
    if (!job) return;
    if (job.timer !== null) clearTimeout(job.timer);
    job.controller?.abort();
  }
}
