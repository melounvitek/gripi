require "minitest/autorun"
require_relative "../lib/security_error_page"

class SecurityErrorPageTest < Minitest::Test
  def test_renders_a_styled_script_free_page
    html = SecurityErrorPage.render(title: "Configuration required", message: "Add this setting:\n\nGRIPI_EXAMPLE=1")

    assert_includes html, "<!doctype html>"
    assert_includes html, "<style>"
    assert_includes html, "Configuration required"
    assert_includes html, "GRIPI_EXAMPLE=1"
    refute_includes html, "<script"
  end

  def test_escapes_dynamic_content
    html = SecurityErrorPage.render(title: "<bad>", message: "</div><script>alert(1)</script>")

    refute_includes html, "<bad>"
    refute_includes html, "<script>alert(1)</script>"
    assert_includes html, "&lt;script&gt;alert(1)&lt;/script&gt;"
  end
end
