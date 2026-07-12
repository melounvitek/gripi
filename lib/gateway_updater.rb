require "open3"

class GatewayUpdater
  Status = Struct.new(
    :state,
    :reason,
    :current_sha,
    :target_sha,
    :target_revision,
    :ahead_count,
    :behind_count,
    :summary,
    :message,
    keyword_init: true
  )
  UpdateResult = Struct.new(:state, :status, :rolled_back, :message, keyword_init: true)
  CommandResult = Struct.new(:stdout, :stderr, :success, :exit_status, keyword_init: true)

  def initialize(directory, dependency_installer: nil)
    @directory = directory
    @dependency_installer = dependency_installer || method(:bundle_install)
  end

  def status
    branch = git("branch", "--show-current")
    return operational_error(:repository, branch, "Could not determine the current Git branch") unless branch.success

    current_revision = revision_sha("HEAD")
    return current_revision if current_revision.is_a?(Status)
    current_sha = shorten(current_revision)

    unless branch.stdout.strip == "master"
      return Status.new(
        state: :blocked,
        reason: :branch,
        current_sha: current_sha,
        message: "Updates require the master branch"
      )
    end

    fetch = git("fetch", "--no-tags", "origin", "master")
    return operational_error(:fetch, fetch, "Could not fetch origin master", current_sha:) unless fetch.success

    target_revision = revision_sha("origin/master")
    return target_revision if target_revision.is_a?(Status)
    target_sha = shorten(target_revision)

    dirty = git("status", "--porcelain", "--untracked-files=all")
    return operational_error(:repository, dirty, "Could not inspect the checkout", current_sha:, target_sha:) unless dirty.success
    unless dirty.stdout.empty?
      return Status.new(
        state: :blocked,
        reason: :dirty,
        current_sha: current_sha,
        target_sha: target_sha,
        target_revision: target_revision,
        message: "The checkout has tracked or untracked changes"
      )
    end

    if current_revision == target_revision
      return Status.new(
        state: :up_to_date,
        current_sha: current_sha,
        target_sha: target_sha,
        target_revision: target_revision,
        ahead_count: 0,
        behind_count: 0,
        summary: ""
      )
    end

    head_is_ancestor = git("merge-base", "--is-ancestor", "HEAD", "origin/master")
    if head_is_ancestor.success
      behind_count = commit_count("HEAD..origin/master")
      return behind_count if behind_count.is_a?(Status)

      return Status.new(
        state: :available,
        current_sha: current_sha,
        target_sha: target_sha,
        target_revision: target_revision,
        ahead_count: 0,
        behind_count: behind_count,
        summary: commit_summary("HEAD..origin/master"),
        message: "#{behind_count} update commit#{"s" unless behind_count == 1} available"
      )
    end
    return ancestry_error(head_is_ancestor, current_sha, target_sha) unless expected_ancestor_miss?(head_is_ancestor)

    target_is_ancestor = git("merge-base", "--is-ancestor", "origin/master", "HEAD")
    if target_is_ancestor.success
      ahead_count = commit_count("origin/master..HEAD")
      return ahead_count if ahead_count.is_a?(Status)

      return Status.new(
        state: :blocked,
        reason: :ahead,
        current_sha: current_sha,
        target_sha: target_sha,
        target_revision: target_revision,
        ahead_count: ahead_count,
        behind_count: 0,
        summary: commit_summary("origin/master..HEAD"),
        message: "The checkout has #{ahead_count} local commit#{"s" unless ahead_count == 1}"
      )
    end
    return ancestry_error(target_is_ancestor, current_sha, target_sha) unless expected_ancestor_miss?(target_is_ancestor)

    ahead_count = commit_count("origin/master..HEAD")
    return ahead_count if ahead_count.is_a?(Status)
    behind_count = commit_count("HEAD..origin/master")
    return behind_count if behind_count.is_a?(Status)

    Status.new(
      state: :blocked,
      reason: :diverged,
      current_sha: current_sha,
      target_sha: target_sha,
      target_revision: target_revision,
      ahead_count: ahead_count,
      behind_count: behind_count,
      summary: commit_summary("HEAD...origin/master"),
      message: "The checkout has diverged from origin/master"
    )
  rescue SystemCallError => error
    Status.new(state: :error, reason: :repository, message: error.message)
  end

  def update
    precondition = status
    return UpdateResult.new(state: precondition.state, status: precondition, rolled_back: false, message: precondition.message) unless precondition.state == :available

    old_revision = git("rev-parse", "HEAD")
    unless old_revision.success
      return UpdateResult.new(state: :error, status: precondition, rolled_back: false, message: command_error("Could not read the current revision", old_revision))
    end

    fast_forward = git("merge", "--ff-only", precondition.target_revision)
    unless fast_forward.success
      return UpdateResult.new(state: :error, status: precondition, rolled_back: false, message: command_error("Could not fast-forward to origin/master", fast_forward))
    end

    @dependency_installation_error = nil
    installation_error = nil
    installed = begin
      @dependency_installer.call(@directory)
    rescue StandardError => error
      installation_error = error.message
      false
    end
    installation_error ||= @dependency_installation_error

    if installed
      return UpdateResult.new(state: :updated, status: precondition, rolled_back: false, message: "Updated to #{precondition.target_sha}")
    end

    rollback = git("reset", "--hard", old_revision.stdout.strip)
    unless rollback.success
      message = command_error("Dependency installation failed and the checkout could not be rolled back", rollback)
      message = "#{installation_error}. #{message}" if installation_error
      return UpdateResult.new(state: :rollback_failed, status: precondition, rolled_back: false, message: message)
    end

    message = "Dependency installation failed; restored #{precondition.current_sha}"
    message = "#{message}: #{installation_error}" if installation_error
    UpdateResult.new(state: :dependency_failed, status: precondition, rolled_back: true, message: message)
  rescue SystemCallError => error
    UpdateResult.new(state: :error, status: precondition, rolled_back: false, message: error.message)
  end

  private

  def git(*arguments)
    stdout, stderr, process_status = Open3.capture3("git", *arguments, chdir: @directory)
    CommandResult.new(stdout: stdout, stderr: stderr, success: process_status.success?, exit_status: process_status.exitstatus)
  rescue SystemCallError => error
    CommandResult.new(stdout: "", stderr: error.message, success: false, exit_status: nil)
  end

  def bundle_install(directory)
    stdout, stderr, process_status = Open3.capture3("bundle", "install", chdir: directory)
    unless process_status.success?
      output = [stdout, stderr].reject(&:empty?).join("\n").strip
      @dependency_installation_error = output.length > 2_000 ? output[-2_000..] : output
    end
    process_status.success?
  end

  def revision_sha(revision)
    result = git("rev-parse", revision)
    return result.stdout.strip if result.success

    operational_error(:repository, result, "Could not resolve #{revision}")
  end

  def shorten(revision)
    revision[0, 8]
  end

  def commit_count(range)
    result = git("rev-list", "--count", range)
    return result.stdout.to_i if result.success

    operational_error(:repository, result, "Could not count commits")
  end

  def commit_summary(range)
    result = git("log", "--format=%h %s", range)
    result.success ? result.stdout.strip : ""
  end

  def expected_ancestor_miss?(result)
    result.exit_status == 1
  end

  def ancestry_error(result, current_sha, target_sha)
    operational_error(:repository, result, "Could not compare Git revisions", current_sha:, target_sha:)
  end

  def operational_error(reason, result, fallback, current_sha: nil, target_sha: nil)
    Status.new(
      state: :error,
      reason: reason,
      current_sha: current_sha,
      target_sha: target_sha,
      message: command_error(fallback, result)
    )
  end

  def command_error(fallback, result)
    detail = result.stderr.to_s.strip
    detail.empty? ? fallback : "#{fallback}: #{detail}"
  end
end
