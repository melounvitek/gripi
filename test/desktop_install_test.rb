require "fileutils"
require "minitest/autorun"
require "open3"
require "tmpdir"

class DesktopInstallTest < Minitest::Test
  def test_linux_install_fails_with_guidance_before_building_when_fuse_2_is_missing
    Dir.mktmpdir do |dir|
      bin_dir = File.join(dir, "bin")
      build_started_path = File.join(dir, "build-started")
      installer_path = copy_installer(dir)
      Dir.mkdir(bin_dir)

      write_executable(bin_dir, "uname", "#!/bin/sh\necho Linux\n")
      write_executable(bin_dir, "ruby", "#!/bin/sh\nexit 1\n")
      write_executable(bin_dir, "mise", "#!/bin/sh\ntouch \"$BUILD_STARTED_PATH\"\nexit 1\n")

      env = {
        "PATH" => "#{bin_dir}:#{ENV.fetch("PATH")}",
        "BUILD_STARTED_PATH" => build_started_path
      }
      _stdout, stderr, status = Open3.capture3(env, installer_path)

      refute status.success?
      assert_includes stderr, "Linux desktop app requires FUSE 2 (libfuse.so.2)"
      assert_includes stderr, "sudo pacman -S fuse2"
      refute_path_exists build_started_path
    end
  end

  def test_mac_install_does_not_check_for_fuse_2
    Dir.mktmpdir do |dir|
      bin_dir = File.join(dir, "bin")
      fuse_checked_path = File.join(dir, "fuse-checked")
      build_started_path = File.join(dir, "build-started")
      installer_path = copy_installer(dir)
      Dir.mkdir(bin_dir)

      write_executable(bin_dir, "uname", "#!/bin/sh\necho Darwin\n")
      write_executable(bin_dir, "ruby", "#!/bin/sh\ntouch \"$FUSE_CHECKED_PATH\"\nexit 1\n")
      write_executable(bin_dir, "mise", "#!/bin/sh\ntouch \"$BUILD_STARTED_PATH\"\nexit 1\n")
      write_executable(bin_dir, "npm", "#!/bin/sh\nexit 0\n")

      env = {
        "PATH" => "#{bin_dir}:#{ENV.fetch("PATH")}",
        "FUSE_CHECKED_PATH" => fuse_checked_path,
        "BUILD_STARTED_PATH" => build_started_path
      }
      _stdout, stderr, status = Open3.capture3(env, installer_path)

      refute status.success?
      refute_includes stderr, "FUSE 2"
      refute_path_exists fuse_checked_path
      assert_path_exists build_started_path
    end
  end

  private

  def copy_installer(dir)
    project_bin_dir = File.join(dir, "project", "bin")
    FileUtils.mkdir_p(project_bin_dir)
    destination = File.join(project_bin_dir, "install-desktop")
    FileUtils.cp(File.join(repo_root, "bin", "install-desktop"), destination)
    destination
  end

  def write_executable(dir, name, content)
    path = File.join(dir, name)
    File.write(path, content)
    File.chmod(0o755, path)
  end

  def repo_root
    File.expand_path("..", __dir__)
  end
end
