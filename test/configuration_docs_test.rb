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
end
