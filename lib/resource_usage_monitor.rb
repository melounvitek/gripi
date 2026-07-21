class ResourceUsageMonitor
  def initialize(proc_root: "/proc", cgroup_root: "/sys/fs/cgroup", pid: Process.pid)
    @proc_root = proc_root
    @cgroup_root = cgroup_root
    @pid = pid
  end

  def snapshot
    cgroup_path = unified_cgroup_path
    return unless cgroup_path && cgroup_path != "/"

    cgroup_directory = File.expand_path(cgroup_path.delete_prefix("/"), @cgroup_root)
    return unless cgroup_directory.start_with?("#{File.expand_path(@cgroup_root)}/")

    memory_bytes = integer_file(File.join(cgroup_directory, "memory.current"))
    inactive_file_bytes = stat_value(File.join(cgroup_directory, "memory.stat"), "inactive_file")
    pi_rss_bytes = 0
    pi_process_count = 0
    process_ids(cgroup_directory).each do |pid|
      next unless process_name(pid) == "pi"

      rss_bytes = process_rss_bytes(pid)
      next unless rss_bytes

      pi_rss_bytes += rss_bytes
      pi_process_count += 1
    end

    {
      memory_bytes: memory_bytes,
      working_set_bytes: [memory_bytes - inactive_file_bytes, 0].max,
      inactive_file_bytes: inactive_file_bytes,
      cpu_usage_usec: cpu_usage_usec(cgroup_directory),
      puma_rss_bytes: process_rss_bytes(@pid),
      pi_rss_bytes: pi_rss_bytes,
      pi_process_count: pi_process_count
    }
  rescue Errno::EACCES, Errno::ENOENT, Errno::ENOTDIR, ArgumentError, TypeError
    nil
  end

  private

  def unified_cgroup_path
    File.foreach(File.join(@proc_root, "self", "cgroup")) do |line|
      return line.split(":", 3).last.strip if line.start_with?("0::")
    end
    nil
  end

  def process_ids(cgroup_directory)
    File.readlines(File.join(cgroup_directory, "cgroup.procs"), chomp: true).filter_map do |value|
      Integer(value, 10)
    rescue ArgumentError
      nil
    end
  end

  def process_name(pid)
    File.read(File.join(@proc_root, pid.to_s, "comm")).strip
  rescue Errno::EACCES, Errno::ENOENT, Errno::ENOTDIR
    nil
  end

  def process_rss_bytes(pid)
    File.foreach(File.join(@proc_root, pid.to_s, "status")) do |line|
      rss_kib = line[/\AVmRSS:\s+(\d+)\s+kB\s*\z/, 1]
      return Integer(rss_kib, 10) * 1024 if rss_kib
    end
    nil
  rescue Errno::EACCES, Errno::ENOENT, Errno::ENOTDIR
    nil
  end

  def integer_file(path)
    Integer(File.read(path).strip, 10)
  end

  def cpu_usage_usec(cgroup_directory)
    stat_value(File.join(cgroup_directory, "cpu.stat"), "usage_usec")
  end

  def stat_value(path, key)
    line = File.foreach(path).find { |entry| entry.start_with?("#{key} ") }
    Integer(line&.split&.last, 10)
  end
end
