require "minitest/autorun"

class DockerfileTest < Minitest::Test
  def setup
    @dockerfile = File.read(File.expand_path("../Dockerfile", __dir__))
  end

  def test_pi_wrapper_pins_pi_to_image_node
    assert_includes @dockerfile, 'PATH=/usr/local/bundle/bin:/home/piuser/.local/bin:/home/piuser/.local/share/mise/shims:$PATH'
    assert_includes @dockerfile, 'pi_bin="$(command -v pi)"'
    assert_includes @dockerfile, 'exec /usr/bin/node %s "$@"'
    assert_includes @dockerfile, '> /home/piuser/.local/bin/pi'
    assert_includes @dockerfile, 'chmod +x /home/piuser/.local/bin/pi'
  end
end
