require "minitest/autorun"
require "tmpdir"
require "fileutils"
require "shellwords"
require_relative "../lib/suggest_composer_paths"

class SuggestComposerPathsTest < Minitest::Test
  def setup
    @root = Dir.mktmpdir
    @project = File.join(@root, "project")
    @home = File.join(@root, "home")
    FileUtils.mkdir_p([@project, @home])
  end

  def teardown
    FileUtils.remove_entry(@root) if Dir.exist?(@root)
  end

  def test_fuzzy_suggestions_match_hidden_files_but_respect_ignores_and_exclude_git
    skip "fd is unavailable" unless fd_available?

    File.write(File.join(@project, "visible-note.txt"), "")
    File.write(File.join(@project, ".hidden-note.txt"), "")
    File.write(File.join(@project, ".gitignore"), "ignored-note.txt\n")
    File.write(File.join(@project, "ignored-note.txt"), "")
    FileUtils.mkdir_p(File.join(@project, ".git"))
    File.write(File.join(@project, ".git", "git-note.txt"), "")

    paths = service.call(@project, "fuzzy", "note").map { |suggestion| suggestion.fetch(:path) }

    assert_includes paths, "visible-note.txt"
    assert_includes paths, ".hidden-note.txt"
    refute_includes paths, "ignored-note.txt"
    refute paths.any? { |path| path.include?(".git/") }
  end

  def test_fuzzy_invocation_does_not_interpret_shell_metacharacters
    marker = File.join(@project, "created-by-shell")

    service.call(@project, "fuzzy", "$(touch created-by-shell)")

    refute_path_exists marker
  end

  def test_fuzzy_requests_100_candidates_and_returns_at_most_20_ranked_results
    args_path = File.join(@root, "args")
    fd_path = write_executable("fake-fd", <<~SH)
      printf '%s\n' "$@" > #{Shellwords.escape(args_path)}
      i=1
      while [ "$i" -le 100 ]; do
        printf 'nested/item-query-%03d.txt\n' "$i"
        i=$((i + 1))
      done
    SH

    suggestions = SuggestComposerPaths.new(fd_path: fd_path).call(@project, "fuzzy", "query")

    assert_equal 20, suggestions.length
    args = File.readlines(args_path, chomp: true)
    assert_equal "100", args[args.index("--max-results") + 1]
    assert_includes args, "--hidden"
    assert_equal ["f", "d"], args.each_index.filter_map { |index| args[index + 1] if args[index] == "--type" }
  end

  def test_fuzzy_resolves_fd_from_a_tilde_pi_agent_directory
    bin_dir = File.join(@home, "custom-agent", "bin")
    FileUtils.mkdir_p(bin_dir)
    fd_path = File.join(bin_dir, "fd")
    File.write(fd_path, "#!/bin/sh\nprintf 'from-custom-fd.txt\\n'\n")
    FileUtils.chmod(0o755, fd_path)
    custom_service = SuggestComposerPaths.new(env: { "HOME" => @home, "PATH" => "", "PI_CODING_AGENT_DIR" => "~/custom-agent" })

    assert_equal "from-custom-fd.txt", custom_service.call(@project, "fuzzy", "custom").first.fetch(:path)
  end

  def test_fuzzy_returns_no_suggestions_when_fd_times_out_exceeds_output_limit_or_is_missing
    slow_fd = write_executable("slow-fd", "sleep 2\nprintf 'late.txt\\n'\n")
    closed_stdout_fd = write_executable("closed-stdout-fd", "exec 1>&-\nsleep 2\n")
    noisy_fd = write_executable("noisy-fd", "yes x | head -c 70000\nsleep 2\n")
    started_at = Process.clock_gettime(Process::CLOCK_MONOTONIC)

    assert_empty SuggestComposerPaths.new(fd_path: slow_fd, timeout_seconds: 0.05).call(@project, "fuzzy", "late")
    assert_empty SuggestComposerPaths.new(fd_path: closed_stdout_fd, timeout_seconds: 0.05).call(@project, "fuzzy", "late")
    assert_operator Process.clock_gettime(Process::CLOCK_MONOTONIC) - started_at, :<, 1
    assert_empty SuggestComposerPaths.new(fd_path: noisy_fd).call(@project, "fuzzy", "x")
    assert_empty SuggestComposerPaths.new(env: { "HOME" => @home, "PATH" => "" }).call(@project, "fuzzy", "anything")
  end

  def test_path_mode_completes_relative_parent_absolute_and_home_prefixes_with_directories_first
    FileUtils.mkdir_p(File.join(@project, "alpha-dir"))
    File.write(File.join(@project, "alpha-file"), "")
    FileUtils.mkdir_p(File.join(@root, "parent-dir"))
    FileUtils.mkdir_p(File.join(@home, "home-dir"))

    relative = service.call(@project, "path", "alpha")
    parent = service.call(@project, "path", "../parent")
    absolute = service.call(@project, "path", File.join(@root, "par"))
    home = service.call(@project, "path", "~/home")

    assert_equal [
      { path: "alpha-dir/", directory: true },
      { path: "alpha-file", directory: false }
    ], relative
    assert_equal "../parent-dir/", parent.first.fetch(:path)
    assert_equal File.join(@root, "parent-dir/"), absolute.first.fetch(:path)
    assert_equal "~/home-dir/", home.first.fetch(:path)
  end

  def test_path_mode_keeps_only_the_first_100_sorted_matches
    110.times { |index| File.write(File.join(@project, format("item-%03d", index)), "") }

    suggestions = service.call(@project, "path", "item")

    assert_equal 100, suggestions.length
    assert_equal "item-000", suggestions.first.fetch(:path)
    assert_equal "item-099", suggestions.last.fetch(:path)
  end

  def test_path_mode_rejects_invalid_queries_and_handles_inaccessible_paths
    invalid = "bad".dup.force_encoding(Encoding::UTF_16LE)

    assert_empty service.call(@project, "path", invalid)
    assert_empty service.call(@project, "path", "missing/path")
    assert_empty service.call(File.join(@root, "missing"), "path", "")
  end

  private

  def service
    @service ||= SuggestComposerPaths.new(env: ENV.to_h.merge("HOME" => @home))
  end

  def fd_available?
    !SuggestComposerPaths.new(env: ENV).send(:fd_path).nil?
  end

  def write_executable(name, contents)
    path = File.join(@root, name)
    File.write(path, "#!/bin/sh\n#{contents}")
    FileUtils.chmod(0o755, path)
    path
  end
end
