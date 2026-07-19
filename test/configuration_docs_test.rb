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

  def test_documents_fixed_request_body_limit_and_reverse_proxy_cap
    configuration = File.read(File.expand_path("../docs/configuration.md", __dir__))

    assert_includes configuration, "fixed 64 MiB request-body limit"
    assert_includes configuration, "reverse proxy"
    assert_includes configuration, "unbounded request-body buffering"
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

  def test_documents_automatic_project_approval_and_its_opt_out
    readme = File.read(File.expand_path("../README.md", __dir__))
    configuration = File.read(File.expand_path("../docs/configuration.md", __dir__))

    assert_includes readme, "automatically approves project-local resources"
    assert_includes readme, "arbitrary code as the gateway OS user"
    assert_includes configuration, "GRIPI_AUTO_APPROVE_PROJECTS=0"
    assert_includes configuration, "does not modify Pi’s saved trust decisions"
    assert_includes configuration, "native Pi CLI default"
  end

  def test_documents_opt_in_linux_resource_monitoring
    configuration = File.read(File.expand_path("../docs/configuration.md", __dir__))

    assert_includes configuration, "GRIPI_RESOURCE_MONITORING=1"
    assert_includes configuration, "Linux cgroup v2"
    assert_includes configuration, "100% represents one logical CPU core"
  end

  def test_documents_composer_parity_controls
    readme = File.read(File.expand_path("../README.md", __dir__))

    assert_includes readme, "Pi-style `@` file search and path completion"
    assert_includes readme, "`!command`"
    assert_includes readme, "`!!command`"
    assert_includes readme, "appears when the RPC command completes rather than streaming"
    assert_includes readme, "the send button steers by default"
    assert_includes readme, "use its menu to select Follow-up mode for the next message"
    assert_includes readme, "Enter steers by default"
    assert_includes readme, "Alt+Enter"
    assert_includes readme, "Shift+Enter inserts a newline"
  end

  def test_documents_direct_shell_access_for_every_approved_user
    readme = File.read(File.expand_path("../README.md", __dir__))
    configuration = File.read(File.expand_path("../docs/configuration.md", __dir__))

    assert_includes readme, "Every approved user can directly execute arbitrary shell commands as the gateway OS user"
    assert_includes configuration, "Every approved user can directly execute arbitrary shell commands as the gateway OS user"
    assert_includes configuration, "Multi-user mode does not give shell commands a different OS identity"
  end
end
