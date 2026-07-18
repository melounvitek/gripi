require "minitest/autorun"

class ConfigurationDocsTest < Minitest::Test
  def test_documents_explicit_reverse_proxy_trust_and_tailscale_serve_migration
    configuration = File.read(File.expand_path("../docs/configuration.md", __dir__))
    examples = File.read(File.expand_path("../docs/examples.md", __dir__))

    assert_includes configuration, "GRIPI_PERMITTED_HOSTS="
    assert_includes configuration, "GRIPI_TRUST_PROXY_HEADERS=1"
    assert_includes configuration, "Gripi does not read the RFC `Forwarded` header"
    assert_includes configuration, "Wildcard binds (`0.0.0.0` or `::`)"
    assert_includes configuration, "GRIPI_ALLOW_INSECURE_REMOTE_HTTP=1"
    assert_includes examples, "automatic legacy proxy compatibility has been removed"
  end

  def test_documents_pi_authentication_and_configuration_prerequisites
    readme = File.read(File.expand_path("../README.md", __dir__))
    configuration = File.read(File.expand_path("../docs/configuration.md", __dir__))
    examples = File.read(File.expand_path("../docs/examples.md", __dir__))

    assert_includes readme, "working, authenticated, and configured"
    assert_includes readme, "same OS user that runs Gripi"
    assert_includes readme, "Gripi setup does not install or configure Pi"
    assert_includes configuration, "Selecting executables only chooses the Pi runtime"
    assert_includes configuration, "does not authenticate or configure Pi"
    assert_includes examples, "service user"
    assert_includes examples, "credentials and environment variables"
  end

  def test_documents_pi_cli_project_trust_prerequisite_and_reload_behavior
    readme = File.read(File.expand_path("../README.md", __dir__))

    assert_includes readme, "`.pi/themes/`, `SYSTEM.md`, `APPEND_SYSTEM.md`, or `.agents/skills/`"
    assert_includes readme, "project packages configured through `.pi/settings.json`"
    assert_includes readme, "trust the project in Pi CLI before opening or starting it in Gripi"
    assert_includes readme, "restart the gateway after active work finishes"
  end

  def test_documents_composer_parity_controls
    readme = File.read(File.expand_path("../README.md", __dir__))

    assert_includes readme, "Pi-style `@` file search and path completion"
    assert_includes readme, "use the **Steer** or **Follow-up** control"
    assert_includes readme, "Enter steers by default"
    assert_includes readme, "Alt+Enter"
    assert_includes readme, "Shift+Enter inserts a newline"
  end
end
