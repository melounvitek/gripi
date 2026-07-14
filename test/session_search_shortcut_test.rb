require "minitest/autorun"
require "json"
require "open3"

class SessionSearchShortcutTest < Minitest::Test
  ASSET_PATH = File.expand_path("../public/assets/app.js", __dir__)
  SHORTCUT_HELPERS_URL = "file://#{File.expand_path("../public/assets/shortcuts.js", __dir__)}"

  def test_shift_find_shortcut_accepts_ctrl_or_command_and_ignores_alt
    results = run_javascript(<<~JS)
      const { sessionSearchShortcut } = await import(#{SHORTCUT_HELPERS_URL.to_json});
      console.log(JSON.stringify([
        sessionSearchShortcut({ key: "f", ctrlKey: true, shiftKey: true }),
        sessionSearchShortcut({ key: "F", metaKey: true, shiftKey: true }),
        sessionSearchShortcut({ key: "f", ctrlKey: true }),
        sessionSearchShortcut({ key: "f", ctrlKey: true, shiftKey: true, altKey: true }),
        sessionSearchShortcut({ key: "f", shiftKey: true })
      ]));
    JS

    assert_equal [true, true, false, false, false], results
  end

  def test_opening_session_search_opens_mobile_drawer_and_focuses_existing_query
    results = run_javascript(<<~JS)
      const classes = (initial = []) => ({
        values: new Set(initial),
        add(value) { this.values.add(value); },
        remove(value) { this.values.delete(value); },
        toggle(value, force) { force ? this.add(value) : this.remove(value); },
        contains(value) { return this.values.has(value); }
      });
      const input = {
        value: "existing query",
        focused: false,
        selected: false,
        focus() { this.focused = true; },
        select() { this.selected = true; }
      };
      const form = {
        classList: classes(),
        querySelector(selector) { return selector === 'input[name="session_search"]' ? input : null; },
        closest() { return null; }
      };
      const button = {
        classList: classes(),
        expanded: "false",
        setAttribute(name, value) { if (name === "aria-expanded") this.expanded = value; }
      };
      const sidebar = {
        querySelector(selector) {
          if (selector === ".sidebar-session-search") return form;
          if (selector === "[data-sidebar-search-toggle]") return button;
          return null;
        }
      };
      const mobileToggle = { checked: false };
      const document = {
        querySelector(selector) { return selector === ".session-sidebar" ? sidebar : null; },
        getElementById(id) { return id === "mobile-session-toggle" ? mobileToggle : null; }
      };
      const { SidebarController } = await import("file://#{File.expand_path("../public/assets/sidebar_controller.js", __dir__)}");
      const { sessionSearchShortcut } = await import(#{SHORTCUT_HELPERS_URL.to_json});
      const controller = new SidebarController(document, { location: { href: "https://example.test/", origin: "https://example.test" } }, { isActive() {} }, { apply() {} }, () => {});
      controller.element = sidebar;
      const handleSessionSearchShortcut = (event) => {
        if (!sessionSearchShortcut(event) || !controller.openSearch()) return false;
        event.preventDefault();
        return true;
      };
      const event = {
        key: "f",
        ctrlKey: true,
        shiftKey: true,
        prevented: false,
        preventDefault() { this.prevented = true; }
      };
      const opened = handleSessionSearchShortcut(event);
      console.log(JSON.stringify([
        opened,
        event.prevented,
        mobileToggle.checked,
        form.classList.contains("is-open"),
        button.classList.contains("is-active"),
        button.expanded,
        input.focused,
        input.selected,
        input.value
      ]));
    JS

    assert_equal [true, true, true, true, true, "true", true, true, "existing query"], results
  end

  def test_escape_closes_open_session_search_without_clearing_active_filters
    results = run_javascript(<<~JS)
      const classes = (initial = []) => ({
        values: new Set(initial),
        add(value) { this.values.add(value); },
        remove(value) { this.values.delete(value); },
        toggle(value, force) { force ? this.add(value) : this.remove(value); },
        contains(value) { return this.values.has(value); }
      });
      const input = { value: "existing query", closest(selector) { return selector === ".sidebar-session-search" ? form : null; } };
      const projectSelect = { value: "" };
      const form = {
        classList: classes(["is-open"]),
        querySelector(selector) { return selector === 'input[name="session_search"]' ? input : null; },
        closest(selector) { return selector === ".recent-sessions" ? container : selector === ".session-sidebar" ? sidebar : null; }
      };
      const button = {
        classList: classes(["is-active"]),
        expanded: "true",
        setAttribute(name, value) { if (name === "aria-expanded") this.expanded = value; }
      };
      const container = {
        querySelector(selector) {
          if (selector === "[data-sidebar-search-toggle]") return button;
          if (selector === "[data-sidebar-project-filter]") return projectSelect;
          return null;
        }
      };
      const sidebar = { querySelector(selector) { return selector === "[data-sidebar-search-toggle]" ? button : null; } };
      const mobileToggle = { checked: true };
      const promptTextarea = { focusOptions: null, focus(options) { this.focusOptions = options; } };
      const document = {
        activeElement: input,
        getElementById(id) { return id === "mobile-session-toggle" ? mobileToggle : null; }
      };
      const { SidebarController } = await import("file://#{File.expand_path("../public/assets/sidebar_controller.js", __dir__)}");
      const controller = new SidebarController(document, { location: { href: "https://example.test/", origin: "https://example.test" } }, { isActive() {} }, { apply() {} }, () => {});
      controller.element = sidebar;
      const closeSessionSearch = (event) => {
        const closed = controller.closeSearch(event);
        if (closed) promptTextarea.focus({ preventScroll: true });
        return closed;
      };
      const event = {
        key: "Escape",
        target: input,
        prevented: false,
        stopped: false,
        immediatelyStopped: false,
        preventDefault() { this.prevented = true; },
        stopPropagation() { this.stopped = true; },
        stopImmediatePropagation() { this.immediatelyStopped = true; }
      };
      const closed = closeSessionSearch(event);
      console.log(JSON.stringify([
        closed,
        form.classList.contains("is-open"),
        input.value,
        button.expanded,
        button.classList.contains("is-active"),
        mobileToggle.checked,
        promptTextarea.focusOptions?.preventScroll,
        event.prevented,
        event.stopped,
        event.immediatelyStopped,
        (() => {
          form.classList.add("is-open");
          document.activeElement = { closest() { return null; } };
          return [closeSessionSearch({ key: "Escape", target: {}, preventDefault() {} }), form.classList.contains("is-open")];
        })()
      ]));
    JS

    assert_equal [true, false, "existing query", "false", true, false, true, true, true, true, [false, true]], results
  end

  def test_session_search_is_a_safe_no_op_without_sidebar_controls
    results = run_javascript(<<~JS)
      const document = {
        querySelector() { return null; },
        getElementById() { return null; }
      };
      const { SidebarController } = await import("file://#{File.expand_path("../public/assets/sidebar_controller.js", __dir__)}");
      const controller = new SidebarController(document, { location: { href: "https://example.test/", origin: "https://example.test" } }, { isActive() {} }, { apply() {} }, () => {});
      console.log(JSON.stringify(controller.openSearch()));
    JS

    assert_equal false, results
  end

  def test_find_requests_are_ignored_while_a_modal_is_open
    results = run_javascript(<<~JS)
      let currentSessionFindBar = {};
      let currentSessionFindInput = {};
      const modalIsOpen = () => true;
      #{javascript_function("requestCurrentSessionFind")}
      #{javascript_function("requestSessionSearch")}
      console.log(JSON.stringify([requestCurrentSessionFind(), requestSessionSearch()]));
    JS

    assert_equal [false, false], results
  end

  def test_keyboard_and_desktop_events_share_find_actions
    script = File.read(ASSET_PATH)

    assert_includes script, "if (handleSessionSearchShortcut(event)) return;"
    assert_includes script, 'window.addEventListener("gripi:current-session-find-requested", requestCurrentSessionFind);'
    assert_includes script, 'window.addEventListener("gripi:session-search-requested", requestSessionSearch);'
  end

  private

  def javascript_function(name)
    File.read(ASSET_PATH).match(/^function #{Regexp.escape(name)}\b.*?^}$/m)&.[](0) || flunk("Missing JavaScript function #{name}")
  end

  def run_javascript(source)
    stdout, stderr, status = Open3.capture3("node", "--input-type=module", "-e", source)
    assert status.success?, stderr
    JSON.parse(stdout)
  end
end
