require "minitest/autorun"
require "json"
require "open3"

class ResourceUsageControllerJsTest < Minitest::Test
  CONTROLLER = File.expand_path("../public/assets/resource_usage_controller.js", __dir__)

  def test_formats_usage_and_calculates_cpu_between_samples
    result = run_javascript(<<~JS)
      const { ResourceUsageController } = await import(#{module_url.to_json});
      const total = { textContent: "" };
      const breakdown = { textContent: "" };
      const control = {
        hidden: true,
        querySelector(selector) {
          return selector === "[data-resource-usage-total]" ? total : breakdown;
        }
      };
      const document = {
        hidden: false,
        querySelector: (selector) => selector === "[data-resource-usage]" ? control : null
      };
      const listeners = {};
      const timers = [];
      const window = {
        addEventListener: (name, callback) => { listeners[name] = callback; },
        setTimeout: (callback, delay) => { timers.push({ callback, delay }); return timers.length; },
        clearTimeout() {}
      };
      const payloads = [
        { supported: true, memoryBytes: 637181952, workingSetBytes: 502964224, inactiveFileBytes: 134217728, cpuUsageUsec: 1000000, pumaRssBytes: 380030976, piRssBytes: 374632448, piProcessCount: 2 },
        { supported: true, memoryBytes: 2147483648, workingSetBytes: 1610612736, inactiveFileBytes: 536870912, cpuUsageUsec: 1100000, pumaRssBytes: 1342177280, piRssBytes: 375390208, piProcessCount: 2 }
      ];
      const sampleTimes = [0, 1000];
      const requests = [];
      const controller = new ResourceUsageController(
        document,
        window,
        async (url, options) => {
          requests.push([url, options.cache, options.headers.Accept]);
          return { ok: true, json: async () => payloads.shift() };
        },
        () => sampleTimes.shift()
      );

      await controller.start();
      const first = { hidden: control.hidden, total: total.textContent, breakdown: breakdown.textContent, title: control.title, delay: timers.at(-1).delay };
      await timers.at(-1).callback();
      const second = { hidden: control.hidden, total: total.textContent, breakdown: breakdown.textContent, title: control.title, delay: timers.at(-1).delay };

      console.log(JSON.stringify({ first, second, requests }));
    JS

    assert_equal false, result.dig("first", "hidden")
    assert_equal "RAM ~480 MB working · CPU —", result.dig("first", "total")
    assert_equal "Puma 362 MB · Pi 357 MB (2) · inactive cache 128 MB", result.dig("first", "breakdown")
    assert_equal "Cgroup total 608 MB; approximate working set excludes inactive file cache; CPU 100% equals one logical core", result.dig("first", "title")
    assert_equal 1_000, result.dig("first", "delay")
    assert_equal "RAM ~1.5 GB working · CPU 10%", result.dig("second", "total")
    assert_equal "Puma 1.25 GB · Pi 358 MB (2) · inactive cache 512 MB", result.dig("second", "breakdown")
    assert_equal "Cgroup total 2 GB; approximate working set excludes inactive file cache; CPU 100% equals one logical core", result.dig("second", "title")
    assert_equal 10_000, result.dig("second", "delay")
    assert_equal [["/resource-usage", "no-store", "application/json"], ["/resource-usage", "no-store", "application/json"]], result.fetch("requests")
  end

  def test_pauses_while_hidden_and_restarts_with_a_fresh_cpu_baseline
    result = run_javascript(<<~JS)
      const control = { hidden: true, querySelector: () => ({ textContent: "" }) };
      const document = { hidden: false, querySelector: () => control };
      const listeners = {};
      const timers = [];
      const cleared = [];
      const window = {
        addEventListener: (name, callback) => { listeners[name] = callback; },
        setTimeout: (callback, delay) => { timers.push({ callback, delay }); return timers.length; },
        clearTimeout: (timer) => cleared.push(timer)
      };
      let requests = 0;
      const { ResourceUsageController } = await import(#{module_url.to_json});
      const controller = new ResourceUsageController(document, window, async () => {
        requests += 1;
        return { ok: true, json: async () => ({ supported: true, memoryBytes: 1, cpuUsageUsec: requests * 100, pumaRssBytes: 1, piRssBytes: 0, piProcessCount: 0 }) };
      }, () => requests * 1000);

      await controller.start();
      document.hidden = true;
      listeners.visibilitychange();
      const afterHide = { requests, cleared: [...cleared] };
      document.hidden = false;
      await listeners.visibilitychange();
      const afterShow = { requests, latestDelay: timers.at(-1).delay };

      console.log(JSON.stringify({ afterHide, afterShow }));
    JS

    assert_equal 1, result.dig("afterHide", "requests")
    assert_operator result.dig("afterHide", "cleared").length, :>=, 1
    assert_equal 2, result.dig("afterShow", "requests")
    assert_equal 1_000, result.dig("afterShow", "latestDelay")
  end

  def test_hides_the_indicator_when_a_later_sample_is_unsupported
    result = run_javascript(<<~JS)
      const control = { hidden: true, querySelector: () => ({ textContent: "" }) };
      const document = { hidden: false, querySelector: () => control };
      const timers = [];
      const window = { addEventListener() {}, setTimeout(callback) { timers.push(callback); return timers.length; }, clearTimeout() {} };
      const payloads = [
        { supported: true, memoryBytes: 1, cpuUsageUsec: 1, pumaRssBytes: 1, piRssBytes: 0, piProcessCount: 0 },
        { supported: false }
      ];
      const { ResourceUsageController } = await import(#{module_url.to_json});
      const controller = new ResourceUsageController(document, window, async () => ({ ok: true, json: async () => payloads.shift() }), () => 0);
      await controller.start();
      const initiallyVisible = !control.hidden;
      await timers.at(-1)();
      console.log(JSON.stringify({ initiallyVisible, hidden: control.hidden }));
    JS

    assert_equal true, result.fetch("initiallyVisible")
    assert_equal true, result.fetch("hidden")
  end

  def test_does_not_poll_until_an_initially_hidden_page_becomes_visible
    result = run_javascript(<<~JS)
      const control = { hidden: true, querySelector: () => ({ textContent: "" }) };
      const document = { hidden: true, querySelector: () => control };
      const listeners = {};
      const window = {
        addEventListener: (name, callback) => { listeners[name] = callback; },
        setTimeout() { return 1; },
        clearTimeout() {}
      };
      let requests = 0;
      const { ResourceUsageController } = await import(#{module_url.to_json});
      const controller = new ResourceUsageController(document, window, async () => {
        requests += 1;
        return { ok: true, json: async () => ({ supported: true, memoryBytes: 1, cpuUsageUsec: 1, pumaRssBytes: 1, piRssBytes: 0, piProcessCount: 0 }) };
      }, () => 0);

      await controller.start();
      const hiddenRequests = requests;
      document.hidden = false;
      await listeners.visibilitychange();
      console.log(JSON.stringify({ hiddenRequests, visibleRequests: requests }));
    JS

    assert_equal 0, result.fetch("hiddenRequests")
    assert_equal 1, result.fetch("visibleRequests")
  end

  private

  def module_url
    "file://#{CONTROLLER}?v=#{Process.pid}-#{rand(1_000_000)}"
  end

  def run_javascript(source)
    stdout, stderr, status = Open3.capture3("node", "--input-type=module", "-e", source)
    assert status.success?, stderr
    JSON.parse(stdout)
  end
end
