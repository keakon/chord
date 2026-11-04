require "json"
require "open3"
require "tmpdir"
require "yaml"

ROOT = File.expand_path("..", __dir__)
WORKFLOW = YAML.load_file(File.join(ROOT, ".github/workflows/homebrew.yml"))
STEPS = WORKFLOW.fetch("jobs").fetch("update").fetch("steps")
VALIDATE = STEPS.find { |step| step["name"] == "Validate release" }.fetch("run")
UPDATE = STEPS.find { |step| step["name"] == "Create version pull request" }.fetch("run")

# Keep Git real, but confine its global config and every remote to the fixture.
# Homebrew's commit/push/PR ordering is significant for the interruption test.
MOCKS = <<~'BASH'
  git() {
    if [[ "$1 $2" == "config --global" ]]; then return; fi
    command git "$@"
  }
  gh() {
    case "$1 $2" in
      "auth setup-git") ;;
      "release view")
        [[ "${FAIL_API:-false}" == false ]] || return 22
        if [[ "$3" == --repo ]]; then
          printf '%s\n' "$LATEST"
        else
          printf '%s\n' "$RELEASE"
        fi
        ;;
      "pr list") cat "$PR_STATE" ;;
      "pr create")
        printf '%s\n' "$*" >> "$CALLS"
        [[ "${FAIL_PR:-false}" == false ]] || return 22
        printf '%s\n' "https://example.invalid/pull/1" > "$PR_STATE"
        cat "$PR_STATE"
        ;;
      *) return 98 ;;
    esac
  }
  brew() {
    case "$1" in
      tap|trust) ;;
      --repository) printf '%s\n' "$TAP_PATH" ;;
      info) printf '{"formulae":[{"versions":{"stable":"%s"}}]}\n' "$CURRENT" ;;
      bump-formula-pr)
        printf '%s\n' "$*" >> "$CALLS"
        [[ "${FAIL_BUMP:-false}" == false ]] || return 23
        cp "$EXPECTED_FORMULA" "$TAP_PATH/Formula/chord.rb"
        if [[ "$2" == --write-only ]]; then return; fi
        (
          cd "$TAP_PATH"
          branch="bump-chord-${RELEASE_TAG#v}"
          git checkout --no-track -b "$branch" origin/main
          git add Formula/chord.rb
          git commit -m "chord ${RELEASE_TAG#v}" -- Formula/chord.rb
          git push --set-upstream origin "$branch:$branch"
          git checkout --quiet -
          gh pr create
        )
        ;;
      *) return 97 ;;
    esac
  }
BASH

BASE_ENV = {
  "GIT_CONFIG_GLOBAL" => File::NULL,
  "GIT_CONFIG_NOSYSTEM" => "1",
  "GIT_AUTHOR_NAME" => "keakon",
  "GIT_AUTHOR_EMAIL" => "1155229+keakon@users.noreply.github.com",
  "GIT_COMMITTER_NAME" => "keakon",
  "GIT_COMMITTER_EMAIL" => "1155229+keakon@users.noreply.github.com",
  "GIT_AUTHOR_DATE" => "2026-01-01T00:00:00Z",
  "GIT_COMMITTER_DATE" => "2026-01-01T00:00:00Z",
  "LC_ALL" => "C",
  "GH_TOKEN" => "sample-token",
  "RELEASE_TAG" => "v0.2.0",
  "RELEASE" => { tagName: "v0.2.0", isDraft: false, isPrerelease: false }.to_json,
  "LATEST" => "v0.2.0",
  "CURRENT" => "0.1.0",
  "TAP_REPOSITORY" => "sample/homebrew-tap",
}.freeze

def check(condition, message)
  raise message unless condition
end

def git(env, *args)
  stdout, stderr, status = Open3.capture3(env, "git", *args)
  raise "git #{args.join(' ')} failed:\n#{stderr}" unless status.success?
  stdout.strip
end

def run_step(env, script, success: true)
  stdout, stderr, status = Open3.capture3(env, "bash", "-euo", "pipefail", "-c", MOCKS + script)
  check(status.success? == success, "unexpected exit #{status.exitstatus}:\n#{stdout}\n#{stderr}")
  [stdout, stderr, status.exitstatus]
end

def fixture
  Dir.mktmpdir("chord-homebrew-workflow-") do |dir|
    env = BASE_ENV.merge(
      "GITHUB_OUTPUT" => "#{dir}/output",
      "GITHUB_STEP_SUMMARY" => "#{dir}/summary",
      "PR_STATE" => "#{dir}/pr",
      "CALLS" => "#{dir}/calls",
      "EXPECTED_FORMULA" => "#{dir}/expected.rb",
      "TAP_PATH" => "#{dir}/tap",
    )
    %w[GITHUB_OUTPUT GITHUB_STEP_SUMMARY PR_STATE CALLS].each { |key| File.write(env.fetch(key), "") }
    yield dir, env
  end
end

def repository(dir, env)
  remote = "#{dir}/remote.git"
  seed = "#{dir}/seed"
  git(env, "init", "--bare", "--initial-branch=main", remote)
  git(env, "init", "--initial-branch=main", seed)
  Dir.mkdir("#{seed}/Formula")
  File.write("#{seed}/Formula/chord.rb", "class Chord < Formula\n  url \"https://example.invalid/v0.1.0.tar.gz\"\nend\n")
  File.write(env.fetch("EXPECTED_FORMULA"), "class Chord < Formula\n  url \"https://example.invalid/v0.2.0.tar.gz\"\nend\n")
  git(env, "-C", seed, "add", "Formula/chord.rb")
  git(env, "-C", seed, "commit", "-m", "Initial formula")
  git(env, "-C", seed, "push", remote, "main")
  git(env, "clone", remote, env.fetch("TAP_PATH"))
  remote
end

[
  ["stable", {}, true, "update=true\n"],
  ["prerelease tag", { "RELEASE_TAG" => "v0.2.0-rc.1", "GH_TOKEN" => "" }, true, ""],
  ["invalid tag", { "RELEASE_TAG" => '$(exit 9)' }, true, ""],
  ["missing token", { "GH_TOKEN" => "" }, false, ""],
  ["API failure", { "FAIL_API" => "true" }, false, ""],
  ["older release", { "LATEST" => "v0.3.0" }, true, ""],
  ["draft", { "RELEASE" => { isDraft: true, isPrerelease: false }.to_json }, true, ""],
  ["prerelease metadata", { "RELEASE" => { isDraft: false, isPrerelease: true }.to_json }, true, ""],
].each do |name, overrides, success, output|
  fixture do |_dir, env|
    run_step(env.merge(overrides), VALIDATE, success:)
    check(File.read(env.fetch("GITHUB_OUTPUT")) == output, "#{name}: unexpected output")
  end
  puts "PASS #{name}"
end

[
  ["same version", { "CURRENT" => "0.2.0" }, false],
  ["downgrade", { "CURRENT" => "0.3.0" }, false],
  ["open PR", {}, true],
].each do |name, overrides, existing|
  fixture do |_dir, env|
    File.write(env.fetch("PR_STATE"), "https://example.invalid/pull/1\n") if existing
    run_step(env.merge(overrides), UPDATE)
    check(File.read(env.fetch("CALLS")).empty?, "#{name}: unexpected update")
  end
  puts "PASS #{name}"
end

fixture do |dir, env|
  repository(dir, env)
  run_step(env, UPDATE)
  calls = File.read(env.fetch("CALLS"))
  check(calls.include?("bump-formula-pr --no-fork --no-browse"), "fresh update must use Homebrew PR creation")
  check(!File.read(env.fetch("PR_STATE")).empty?, "fresh update did not create PR")
end
puts "PASS fresh update"

fixture do |dir, env|
  repository(dir, env)
  run_step(env.merge("FAIL_BUMP" => "true"), UPDATE, success: false)
  check(File.read(env.fetch("PR_STATE")).empty?, "failed bump created PR")
end
puts "PASS bump failure"

fixture do |dir, env|
  remote = repository(dir, env)
  _, _, exit_code = run_step(env.merge("FAIL_PR" => "true"), UPDATE, success: false)
  check(exit_code == 22, "first run must reach the failed PR call")
  branch_sha = git(env, "--git-dir", remote, "rev-parse", "refs/heads/bump-chord-0.2.0")
  retry_env = env.merge(
    "TAP_PATH" => "#{dir}/retry",
    "GIT_AUTHOR_DATE" => "2026-01-02T00:00:00Z",
    "GIT_COMMITTER_DATE" => "2026-01-02T00:00:00Z",
  )
  git(env, "clone", remote, retry_env.fetch("TAP_PATH"))
  File.write(env.fetch("CALLS"), "")
  run_step(retry_env, UPDATE)
  check(git(env, "--git-dir", remote, "rev-parse", "refs/heads/bump-chord-0.2.0") == branch_sha,
        "retry must preserve the existing remote commit")
  calls = File.read(env.fetch("CALLS"))
  check(calls.include?("bump-formula-pr --write-only"), "retry must regenerate the expected formula")
  check(calls.include?("pr create --repo sample/homebrew-tap --head bump-chord-0.2.0 --base main"),
        "retry must create a PR from the existing branch")
  check(!calls.include?("--no-fork"), "retry must not push another commit")
  File.write(env.fetch("CALLS"), "")
  run_step(retry_env, UPDATE)
  check(File.read(env.fetch("CALLS")).empty?, "retry after success must be idempotent")
end
puts "PASS interrupted update recovery and idempotent retry"

["formula content", "unexpected file", "deleted formula", "file mode", "remote failure", "generation failure"].each do |name|
  fixture do |dir, env|
    remote = repository(dir, env)
    tap = env.fetch("TAP_PATH")
    git(env, "-C", tap, "checkout", "-b", "bump-chord-0.2.0")
    File.write("#{tap}/Formula/chord.rb", File.read(env.fetch("EXPECTED_FORMULA")))
    case name
    when "formula content"
      File.write("#{tap}/Formula/chord.rb", "raise 'unexpected formula'\n")
    when "unexpected file"
      File.write("#{tap}/extra.txt", "unexpected\n")
    when "deleted formula"
      File.delete("#{tap}/Formula/chord.rb")
    when "file mode"
      File.chmod(0o755, "#{tap}/Formula/chord.rb")
    end
    git(env, "-C", tap, "add", "--all")
    git(env, "-C", tap, "commit", "-m", "Pending update")
    git(env, "-C", tap, "push", "origin", "bump-chord-0.2.0")
    git(env, "-C", tap, "checkout", "main")
    branch_sha = git(env, "--git-dir", remote, "rev-parse", "refs/heads/bump-chord-0.2.0")
    git(env, "-C", tap, "remote", "set-url", "origin", "#{dir}/missing.git") if name == "remote failure"
    env = env.merge("FAIL_BUMP" => "true") if name == "generation failure"
    run_step(env, UPDATE, success: false)
    check(File.read(env.fetch("PR_STATE")).empty?, "#{name}: unexpected PR creation")
    check(git(env, "--git-dir", remote, "rev-parse", "refs/heads/bump-chord-0.2.0") == branch_sha,
          "#{name}: changed remote branch")
  end
  puts "PASS reject #{name}"
end
