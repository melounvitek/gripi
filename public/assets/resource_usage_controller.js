const INITIAL_CPU_SAMPLE_DELAY_MS = 1000;
const POLL_INTERVAL_MS = 10000;

export class ResourceUsageController {
  constructor(document, window, fetcher = (...args) => fetch(...args), clock = () => performance.now()) {
    this.document = document;
    this.window = window;
    this.fetcher = fetcher;
    this.clock = clock;
    this.timer = null;
    this.started = false;
    this.generation = 0;
    this.previousCpuSample = null;

    window.addEventListener("visibilitychange", () => {
      if (document.hidden) {
        this.pause();
        return;
      }

      this.previousCpuSample = null;
      return this.start({ restart: true });
    });
  }

  start({ restart = false } = {}) {
    if (this.document.hidden || !this.element()) return Promise.resolve();
    if (this.started && !restart) return Promise.resolve();

    this.started = true;
    this.generation += 1;
    this.window.clearTimeout(this.timer);
    return this.poll(this.generation);
  }

  pause() {
    this.started = false;
    this.generation += 1;
    this.window.clearTimeout(this.timer);
    this.timer = null;
  }

  async poll(generation = this.generation) {
    try {
      const response = await this.fetcher("/resource-usage", {
        headers: { "Accept": "application/json" },
        cache: "no-store"
      });
      if (!response.ok) throw new Error("Could not read resource usage");

      const payload = await response.json();
      if (generation !== this.generation || this.document.hidden) return;
      if (!payload.supported) {
        const element = this.element();
        if (element) element.hidden = true;
        this.started = false;
        return;
      }

      const sampledAt = this.clock();
      const cpuPercent = this.cpuPercent(payload.cpuUsageUsec, sampledAt);
      this.previousCpuSample = { usageUsec: payload.cpuUsageUsec, sampledAt };
      this.render(payload, cpuPercent);
      this.schedule(cpuPercent === null ? INITIAL_CPU_SAMPLE_DELAY_MS : POLL_INTERVAL_MS, generation);
    } catch (_error) {
      if (generation === this.generation && !this.document.hidden) this.schedule(POLL_INTERVAL_MS, generation);
    }
  }

  cpuPercent(usageUsec, sampledAt) {
    if (!this.previousCpuSample) return null;

    const elapsedMs = sampledAt - this.previousCpuSample.sampledAt;
    const usedUsec = usageUsec - this.previousCpuSample.usageUsec;
    if (elapsedMs <= 0 || usedUsec < 0) return null;
    return Math.round(usedUsec / (elapsedMs * 1000) * 100);
  }

  schedule(delay, generation) {
    this.window.clearTimeout(this.timer);
    this.timer = this.window.setTimeout(() => this.poll(generation), delay);
  }

  render(payload, cpuPercent) {
    const element = this.element();
    const total = element?.querySelector("[data-resource-usage-total]");
    const breakdown = element?.querySelector("[data-resource-usage-breakdown]");
    if (!element || !total || !breakdown) return;

    element.hidden = false;
    element.title = `Cgroup total ${formatBytes(payload.memoryBytes)}; approximate working set excludes inactive file cache; Puma and Pi RSS do not sum to the cgroup working set; CPU 100% equals one logical core`;
    total.textContent = `RAM ~${formatBytes(payload.workingSetBytes)} working set · CPU ${cpuPercent === null ? "—" : `${cpuPercent}%`}`;
    breakdown.textContent = `Puma ${formatBytes(payload.pumaRssBytes)} · Pi ${formatBytes(payload.piRssBytes)} (${payload.piProcessCount}) · inactive file cache ${formatBytes(payload.inactiveFileBytes)}`;
  }

  element() {
    return this.document.querySelector("[data-resource-usage]");
  }
}

function formatBytes(bytes) {
  const gibibyte = 1024 ** 3;
  if (bytes >= gibibyte) return `${trimmedDecimal(bytes / gibibyte)} GB`;
  return `${Math.round(bytes / 1024 ** 2)} MB`;
}

function trimmedDecimal(value) {
  return value.toFixed(2).replace(/\.00$/, "").replace(/(\.\d)0$/, "$1");
}
