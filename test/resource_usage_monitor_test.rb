require "minitest/autorun"
require "tmpdir"
require "fileutils"
require_relative "../lib/resource_usage_monitor"

class ResourceUsageMonitorTest < Minitest::Test
  def setup
    @root = Dir.mktmpdir
    @proc_root = File.join(@root, "proc")
    @cgroup_root = File.join(@root, "cgroup")
    FileUtils.mkdir_p(File.join(@proc_root, "self"))
    File.write(File.join(@proc_root, "self", "cgroup"), "0::/user.slice/gripi.service\n")
    FileUtils.mkdir_p(File.join(@cgroup_root, "user.slice", "gripi.service"))
    File.write(File.join(@cgroup_root, "user.slice", "gripi.service", "memory.current"), "637181952\n")
    File.write(File.join(@cgroup_root, "user.slice", "gripi.service", "memory.stat"), "anon 400000000\nfile 200000000\ninactive_file 134217728\n")
    File.write(File.join(@cgroup_root, "user.slice", "gripi.service", "cpu.stat"), "usage_usec 1234567\nuser_usec 1000000\nsystem_usec 234567\n")
    File.write(File.join(@cgroup_root, "user.slice", "gripi.service", "cgroup.procs"), "100\n101\n102\n103\n")
    write_process(100, "ruby", 371_124)
    write_process(101, "pi", 183_184)
    write_process(102, "pi", 182_668)
    write_process(103, "bash", 3_820)
  end

  def teardown
    FileUtils.remove_entry(@root)
  end

  def test_reads_service_puma_and_pi_usage
    snapshot = monitor.snapshot

    assert_equal(
      {
        memory_bytes: 637_181_952,
        working_set_bytes: 502_964_224,
        inactive_file_bytes: 134_217_728,
        cpu_usage_usec: 1_234_567,
        puma_rss_bytes: 371_124 * 1024,
        pi_rss_bytes: (183_184 + 182_668) * 1024,
        pi_process_count: 2
      },
      snapshot
    )
  end

  def test_ignores_processes_that_exit_during_collection
    FileUtils.rm_rf(File.join(@proc_root, "102"))

    snapshot = monitor.snapshot

    assert_equal 183_184 * 1024, snapshot.fetch(:pi_rss_bytes)
    assert_equal 1, snapshot.fetch(:pi_process_count)
  end

  def test_returns_nil_without_a_linux_v2_cgroup
    File.write(File.join(@proc_root, "self", "cgroup"), "2:memory:/legacy\n")

    assert_nil monitor.snapshot
  end

  def test_returns_nil_for_the_root_cgroup
    File.write(File.join(@proc_root, "self", "cgroup"), "0::/\n")

    assert_nil monitor.snapshot
  end

  def test_clamps_working_set_when_inactive_file_exceeds_current_memory
    File.write(File.join(@cgroup_root, "user.slice", "gripi.service", "memory.stat"), "inactive_file 700000000\n")

    assert_equal 0, monitor.snapshot.fetch(:working_set_bytes)
  end

  def test_returns_nil_when_required_cgroup_data_is_invalid
    File.write(File.join(@cgroup_root, "user.slice", "gripi.service", "memory.current"), "invalid\n")

    assert_nil monitor.snapshot
  end

  def test_returns_nil_when_inactive_file_data_is_missing
    File.write(File.join(@cgroup_root, "user.slice", "gripi.service", "memory.stat"), "anon 400000000\n")

    assert_nil monitor.snapshot
  end

  private

  def monitor
    ResourceUsageMonitor.new(proc_root: @proc_root, cgroup_root: @cgroup_root, pid: 100)
  end

  def write_process(pid, name, rss_kib)
    directory = File.join(@proc_root, pid.to_s)
    FileUtils.mkdir_p(directory)
    File.write(File.join(directory, "comm"), "#{name}\n")
    File.write(File.join(directory, "status"), "Name:\t#{name}\nVmRSS:\t#{rss_kib} kB\n")
  end
end
