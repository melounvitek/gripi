require "timeout"

class SuggestComposerPaths
  QUERY_BYTES = 1_024
  FUZZY_CANDIDATE_LIMIT = 100
  FUZZY_SUGGESTION_LIMIT = 20
  PATH_SUGGESTION_LIMIT = 100
  MAX_FD_OUTPUT_BYTES = 64 * 1_024
  DEFAULT_TIMEOUT_SECONDS = 0.5
  AUTOMATIC_FD = Object.new.freeze

  def self.call(cwd, mode, query)
    new.call(cwd, mode, query)
  end

  def initialize(env: ENV, fd_path: AUTOMATIC_FD, timeout_seconds: DEFAULT_TIMEOUT_SECONDS)
    @env = env
    @configured_fd_path = fd_path
    @timeout_seconds = timeout_seconds
  end

  def call(cwd, mode, query)
    return [] unless valid_input?(cwd, query)

    case mode
    when "fuzzy" then fuzzy_suggestions(cwd, query)
    when "path" then path_suggestions(cwd, query)
    else []
    end
  rescue ArgumentError, EncodingError, SystemCallError
    []
  end

  private

  def valid_input?(cwd, query)
    cwd.is_a?(String) && query.is_a?(String) &&
      cwd.valid_encoding? && query.valid_encoding? &&
      query.bytesize <= QUERY_BYTES && !query.include?("\0") &&
      File.directory?(cwd) && File.readable?(cwd) && File.executable?(cwd)
  end

  def fuzzy_suggestions(cwd, query)
    executable = fd_path
    return [] unless executable

    scope = fuzzy_scope(cwd, query)
    return [] unless scope

    entries = run_fd(executable, scope.fetch(:base_dir), scope.fetch(:query))
    ranked = entries.each_with_index.filter_map do |entry, index|
      path = entry.delete_suffix("/")
      directory = entry.end_with?("/")
      score = scope.fetch(:query).empty? ? 1 : score(path, scope.fetch(:query), directory)
      next unless score.positive?

      [score, index, fuzzy_result(scope.fetch(:display_base), path, directory)]
    end
    ranked.sort_by { |entry_score, index, _result| [-entry_score, index] }
      .first(FUZZY_SUGGESTION_LIMIT)
      .map(&:last)
  end

  def fuzzy_scope(cwd, query)
    slash_index = query.rindex("/")
    return { base_dir: cwd, query: query, display_base: "" } unless slash_index

    display_base = query[0..slash_index]
    base_dir = expand_search_path(cwd, display_base)
    return unless accessible_directory?(base_dir)

    { base_dir: base_dir, query: query[(slash_index + 1)..], display_base: display_base }
  end

  def fuzzy_result(display_base, path, directory)
    display_path = if display_base == "/"
      "/#{path}"
    else
      "#{display_base}#{path}"
    end
    display_path = "#{display_path}/" if directory
    { path: display_path, directory: directory }
  end

  def score(path, query, directory)
    file_name = File.basename(path).downcase
    normalized_query = query.downcase
    value = if file_name == normalized_query
      100
    elsif file_name.start_with?(normalized_query)
      80
    elsif file_name.include?(normalized_query)
      50
    elsif path.downcase.include?(normalized_query)
      30
    else
      0
    end
    value += 10 if directory && value.positive?
    value
  end

  def run_fd(executable, base_dir, query)
    arguments = [
      "--base-directory", base_dir,
      "--max-results", FUZZY_CANDIDATE_LIMIT.to_s,
      "--type", "f",
      "--type", "d",
      "--follow",
      "--hidden",
      "--exclude", ".git",
      "--exclude", ".git/*",
      "--exclude", ".git/**"
    ]
    arguments.concat(["--", query]) unless query.empty?

    reader, writer = IO.pipe
    pid = Process.spawn(@env, executable, *arguments, in: File::NULL, out: writer, err: File::NULL, pgroup: true)
    writer.close
    output, status = Timeout.timeout(@timeout_seconds) do
      output = reader.read(MAX_FD_OUTPUT_BYTES + 1) || String.new
      if output.bytesize > MAX_FD_OUTPUT_BYTES
        Process.kill("KILL", -pid)
        Process.wait(pid)
        [output, nil]
      else
        [output, Process.wait2(pid).last]
      end
    end
    pid = nil
    return [] unless status&.success?

    output.force_encoding(Encoding::UTF_8)
    return [] unless output.valid_encoding?

    output.lines(chomp: true).first(FUZZY_CANDIDATE_LIMIT).reject do |line|
      normalized = line.delete_suffix("/")
      normalized == ".git" || normalized.start_with?(".git/") || normalized.include?("/.git/")
    end
  rescue Timeout::Error, Errno::ENOENT, Errno::EACCES, Errno::ESRCH, Errno::ECHILD
    []
  ensure
    reader&.close
    writer&.close unless writer&.closed?
    if pid
      begin
        Process.kill("KILL", -pid)
        Process.wait(pid)
      rescue Errno::ESRCH, Errno::ECHILD
        nil
      end
    end
  end

  def path_suggestions(cwd, query)
    search_dir, prefix, display_base = path_scope(cwd, query)
    return [] unless accessible_directory?(search_dir)

    suggestions = []
    Dir.each_child(search_dir) do |name|
      next unless name.downcase.start_with?(prefix.downcase)

      full_path = File.join(search_dir, name)
      directory = File.directory?(full_path)
      path = "#{display_base}#{name}#{directory ? "/" : ""}"
      suggestions << { path: path, directory: directory }
      if suggestions.length > PATH_SUGGESTION_LIMIT
        suggestions.sort_by! { |suggestion| [suggestion.fetch(:directory) ? 0 : 1, suggestion.fetch(:path).downcase] }
        suggestions.pop
      end
    rescue SystemCallError
      next
    end
    suggestions.sort_by { |suggestion| [suggestion.fetch(:directory) ? 0 : 1, suggestion.fetch(:path).downcase] }
  end

  def path_scope(cwd, query)
    if ["", "./", "../", "/", "~", "~/"].include?(query) || query.end_with?("/")
      display_base = query == "~" ? "~/" : query
      [expand_search_path(cwd, display_base), "", display_base]
    else
      slash_index = query.rindex("/")
      display_base = slash_index ? query[0..slash_index] : ""
      prefix = slash_index ? query[(slash_index + 1)..] : query
      [expand_search_path(cwd, display_base), prefix, display_base]
    end
  end

  def expand_search_path(cwd, path)
    if path == "~" || path.start_with?("~/")
      File.join(@env.fetch("HOME", Dir.home), path.delete_prefix("~/").delete_prefix("~"))
    elsif path.start_with?("/")
      path
    else
      File.join(cwd, path)
    end
  end

  def accessible_directory?(path)
    File.directory?(path) && File.readable?(path) && File.executable?(path)
  rescue SystemCallError
    false
  end

  def fd_path
    return executable_file(@configured_fd_path) unless @configured_fd_path.equal?(AUTOMATIC_FD)

    configured_dir = @env["PI_CODING_AGENT_DIR"].to_s
    if configured_dir == "~" || configured_dir.start_with?("~/")
      configured_dir = File.join(@env.fetch("HOME", Dir.home), configured_dir.delete_prefix("~/").delete_prefix("~"))
    end
    candidates = []
    candidates << File.join(configured_dir, "bin", "fd") unless configured_dir.empty?
    candidates << File.join(@env.fetch("HOME", Dir.home), ".pi", "agent", "bin", "fd")
    candidates.concat(path_executables("fd"))
    candidates.concat(path_executables("fdfind"))
    candidates.find { |path| executable_file(path) }
  end

  def path_executables(name)
    @env.fetch("PATH", "").split(File::PATH_SEPARATOR).map { |directory| File.join(directory, name) }
  end

  def executable_file(path)
    path if path && File.file?(path) && File.executable?(path)
  rescue SystemCallError
    nil
  end
end
