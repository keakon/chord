require "fileutils"
require "json"
require "open3"
require "tmpdir"
require "yaml"
require_relative "homebrew"

ROOT = File.expand_path("..", __dir__)
SCRIPT = File.join(ROOT, "scripts/homebrew.rb")
TAG = "v0.2.0"
MANIFEST = HomebrewRelease::PLATFORMS.each_with_index.map do |platform, index|
  "#{(index + 1).to_s * 64}  chord-#{TAG}-#{platform}.tar.gz\n"
end.join.freeze
RELEASE = {
  tagName: TAG, isDraft: false, isPrerelease: false, publishedAt: "2026-01-01T00:00:00Z",
  assets: (HomebrewRelease::PLATFORMS.map { |platform| "chord-#{TAG}-#{platform}.tar.gz" } +
           ["SHA256SUMS"]).map { |name| { name: } },
}.freeze

def check(condition, message)
  raise message unless condition
end

def git(env, *args)
  stdout, stderr, status = Open3.capture3(env, "git", *args)
  raise "git #{args.join(' ')} failed:\n#{stderr}" unless status.success?
  stdout.strip
end

def run(env, *args, success: true)
  stdout, stderr, status = Open3.capture3(env, "ruby", SCRIPT, *args)
  check(status.success? == success, "unexpected exit #{status.exitstatus}:\n#{stdout}\n#{stderr}")
  [stdout, stderr]
end

def fixture(previous: "0.1.0", source_formula: false)
  Dir.mktmpdir("chord-homebrew-") do |dir|
    bin = "#{dir}/bin"
    FileUtils.mkdir_p(bin)
    File.write("#{bin}/gh", <<~'RUBY')
      #!/usr/bin/env ruby
      require "fileutils"
      exit 22 if ENV["FAIL_API"] == "true"
      case ARGV[0, 2]
      when ["release", "view"]
        puts ARGV[2] == "--repo" ? ENV.fetch("LATEST") : ENV.fetch("RELEASE")
      when ["release", "download"]
        exit 23 if ENV["FAIL_DOWNLOAD"] == "true"
        dir = ARGV.fetch(ARGV.index("--dir") + 1)
        File.write(File.join(dir, "SHA256SUMS"), ENV.fetch("MANIFEST"))
      else
        abort "unexpected gh command: #{ARGV.inspect}"
      end
    RUBY
    File.chmod(0o755, "#{bin}/gh")
    env = {
      "PATH" => "#{bin}:#{ENV.fetch('PATH')}",
      "GIT_CONFIG_GLOBAL" => File::NULL, "GIT_CONFIG_NOSYSTEM" => "1",
      "GIT_AUTHOR_NAME" => "Test", "GIT_AUTHOR_EMAIL" => "test@example.invalid",
      "GIT_COMMITTER_NAME" => "Test", "GIT_COMMITTER_EMAIL" => "test@example.invalid",
      "GITHUB_OUTPUT" => "#{dir}/output",
      "LATEST" => TAG, "RELEASE" => RELEASE.to_json, "MANIFEST" => MANIFEST,
    }
    remote = "#{dir}/remote.git"
    tap = "#{dir}/tap"
    git(env, "init", "--bare", "--initial-branch=main", remote)
    git(env, "clone", remote, tap)
    File.write("#{tap}/README.md", "tap\n")
    if previous
      FileUtils.mkdir_p("#{tap}/Formula")
      contents = if source_formula
        "class Chord < Formula\n  url \"https://github.com/keakon/chord/archive/refs/tags/v#{previous}.tar.gz\"\nend\n"
      else
        HomebrewRelease.render("v#{previous}", MANIFEST.gsub(TAG, "v#{previous}"))
      end
      File.write("#{tap}/Formula/chord.rb", contents)
    end
    git(env, "-C", tap, "add", ".")
    git(env, "-C", tap, "commit", "-m", "initial")
    git(env, "-C", tap, "push", "origin", "main")
    yield dir, env, tap, remote
  end
end

def candidate(dir, env, tap)
  run(env, "prepare", TAG, tap)
  path = "#{dir}/candidate.rb"
  FileUtils.cp("#{tap}/Formula/chord.rb", path)
  git(env, "-C", tap, "restore", "--", "Formula/chord.rb")
  path
end

[
  ["non-stable tag", "v0.2.0-rc.1", {}],
  ["invalid tag", '$(exit 9)', {}],
  ["older release", TAG, { "LATEST" => "v0.3.0" }],
  ["draft", TAG, { "RELEASE" => RELEASE.merge(isDraft: true).to_json }],
  ["prerelease", TAG, { "RELEASE" => RELEASE.merge(isPrerelease: true).to_json }],
  ["unpublished release", TAG, { "RELEASE" => RELEASE.merge(publishedAt: nil).to_json }],
].each do |name, tag, overrides|
  fixture do |_dir, env, tap, _remote|
    before = File.read("#{tap}/Formula/chord.rb")
    run(env.merge(overrides), "prepare", tag, tap)
    check(File.read("#{tap}/Formula/chord.rb") == before, "#{name}: changed formula")
    check(File.read(env.fetch("GITHUB_OUTPUT")) == "updated=false\n", "#{name}: output")
  end
  puts "PASS #{name}"
end

["0.2.0", "0.3.0"].each do |previous|
  fixture(previous:) do |_dir, env, tap, _remote|
    run(env, "prepare", TAG, tap)
    check(git(env, "-C", tap, "status", "--porcelain").empty?, "same/older version changed tap")
  end
end
puts "PASS duplicate versions and downgrades"

[
  ["API failure", { "FAIL_API" => "true" }],
  ["download failure", { "FAIL_DOWNLOAD" => "true" }],
  ["missing archives", { "RELEASE" => RELEASE.merge(assets: []).to_json }],
  ["missing checksum", { "MANIFEST" => MANIFEST.lines.drop(1).join }],
  ["invalid checksum", { "MANIFEST" => MANIFEST.sub("1" * 64, "invalid") }],
  ["duplicate file", { "MANIFEST" => MANIFEST + MANIFEST.lines.first }],
].each do |name, overrides|
  fixture do |_dir, env, tap, remote|
    before = git(env, "--git-dir", remote, "rev-parse", "main")
    run(env.merge(overrides), "prepare", TAG, tap, success: false)
    check(git(env, "-C", tap, "status", "--porcelain").empty?, "#{name}: changed working tree")
    check(git(env, "--git-dir", remote, "rev-parse", "main") == before, "#{name}: pushed")
  end
  puts "PASS #{name}"
end

[false, true].each do |source_formula|
  fixture(source_formula:) do |dir, env, tap, remote|
    path = candidate(dir, env, tap)
    generated = File.read(path)
    check(generated.scan(/sha256 "/).length == 4, "missing platform checksums")
    check(generated.include?('bin.install "chord"'), "not a binary formula")
    run(env, "publish", TAG, tap, path)
    check(git(env, "--git-dir", remote, "show", "main:Formula/chord.rb") == generated.strip,
          "published formula differs from tested candidate")
    check(git(env, "--git-dir", remote, "for-each-ref", "--format=%(refname)", "refs/heads") ==
          "refs/heads/main", "unexpected release branch")
    head = git(env, "--git-dir", remote, "rev-parse", "main")
    run(env, "publish", TAG, tap, path)
    check(git(env, "--git-dir", remote, "rev-parse", "main") == head, "retry changed main")
  end
end
puts "PASS binary/source formula upgrades, direct main commits, and idempotent retries"

fixture(previous: nil) do |dir, env, tap, _remote|
  run(env, "prepare", TAG, tap)
  path = "#{dir}/candidate.rb"
  FileUtils.mv("#{tap}/Formula/chord.rb", path)
  run(env, "publish", TAG, tap, path)
end
puts "PASS initial release"

fixture(previous: "0.9.9") do |_dir, env, tap, _remote|
  check(HomebrewRelease.newer?("v0.10.0", tap), "version comparison must be numeric")
end
puts "PASS numeric version ordering"

["modified candidate", "changed release assets", "newer release during publishing", "publish API failure", "dirty working tree"].each do |name|
  fixture do |dir, env, tap, remote|
    path = candidate(dir, env, tap)
    before = git(env, "--git-dir", remote, "rev-parse", "main")
    case name
    when "modified candidate" then File.write(path, "raise 'modified'\n")
    when "changed release assets" then env = env.merge("MANIFEST" => MANIFEST.sub("1" * 64, "a" * 64))
    when "newer release during publishing" then env = env.merge("LATEST" => "v0.3.0")
    when "publish API failure" then env = env.merge("FAIL_API" => "true")
    when "dirty working tree" then File.write("#{tap}/README.md", "unrelated work\n")
    end
    run(env, "publish", TAG, tap, path, success: name == "newer release during publishing")
    check(git(env, "--git-dir", remote, "rev-parse", "main") == before, "#{name}: changed remote")
  end
  puts "PASS #{name}"
end

fixture do |dir, env, tap, remote|
  path = candidate(dir, env, tap)
  other = "#{dir}/other"
  git(env, "clone", remote, other)
  File.write("#{other}/README.md", "concurrent change\n")
  git(env, "-C", other, "add", "README.md")
  git(env, "-C", other, "commit", "-m", "concurrent")
  git(env, "-C", other, "push", "origin", "main")
  before = git(env, "--git-dir", remote, "rev-parse", "main")
  run(env, "publish", TAG, tap, path, success: false)
  check(git(env, "--git-dir", remote, "rev-parse", "main") == before, "overwrote concurrent push")
  retry_tap = "#{dir}/retry"
  git(env, "clone", remote, retry_tap)
  retry_path = candidate(dir, env, retry_tap)
  run(env, "publish", TAG, retry_tap, retry_path)
  check(git(env, "--git-dir", remote, "show", "main:README.md") == "concurrent change",
        "retry lost concurrent work")
end
puts "PASS concurrent push rejected and full retry preserves other changes"

workflow = YAML.load_file(File.join(ROOT, ".github/workflows/homebrew.yml"))
jobs = workflow.fetch("jobs")
check(jobs.fetch("publish").fetch("needs").sort == %w[prepare test], "publish must wait for tests")
check(jobs.fetch("test").fetch("strategy").fetch("matrix").fetch("os").length == 4, "four platforms required")
%w[prepare test].each do |name|
  check(!jobs.fetch(name).to_s.include?("HOMEBREW_TAP_TOKEN"), "write token exposed before publishing")
end
jobs.each_value do |job|
  job.fetch("steps").each do |step|
    next unless step["run"]
    _, error, status = Open3.capture3("bash", "-n", stdin_data: step.fetch("run"))
    check(status.success?, error)
  end
end
puts "PASS workflow dependencies, token isolation, and Bash syntax"
