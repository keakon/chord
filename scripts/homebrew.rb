require "erb"
require "fileutils"
require "json"
require "open3"
require "tmpdir"

module HomebrewRelease
  PLATFORMS = %w[darwin-arm64 darwin-amd64 linux-arm64 linux-amd64].freeze
  STABLE_TAG = /\Av(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)\z/
  TEMPLATE = File.join(__dir__, "homebrew/chord.rb.erb")

  def self.command(*args)
    stdout, stderr, status = Open3.capture3(*args)
    raise "#{args.first} failed: #{stderr}" unless status.success?
    stdout
  end

  def self.release(tag)
    unless STABLE_TAG.match?(tag)
      puts "Skipping non-stable tag: #{tag}"
      return
    end
    data = JSON.parse(command("gh", "release", "view", tag, "--repo", "keakon/chord",
                              "--json", "tagName,isDraft,isPrerelease,publishedAt,assets"))
    unless data.fetch("tagName") == tag && !data.fetch("isDraft") &&
           !data.fetch("isPrerelease") && data.fetch("publishedAt")
      puts "Skipping release that is not published and stable: #{tag}"
      return
    end
    latest = command("gh", "release", "view", "--repo", "keakon/chord",
                     "--json", "tagName", "--jq", ".tagName").strip
    if latest != tag
      puts "Skipping older release #{tag}; latest stable release is #{latest}."
      return
    end
    expected = PLATFORMS.map { |platform| "chord-#{tag}-#{platform}.tar.gz" } + ["SHA256SUMS"]
    names = data.fetch("assets").map { |asset| asset.fetch("name") }
    raise "Release is missing archives or SHA256SUMS." unless (expected - names).empty?
    data
  end

  def self.newer?(tag, tap)
    path = File.join(tap, "Formula/chord.rb")
    return true unless File.exist?(path)

    contents = File.read(path)
    versions = contents.scan(%r{^\s+url "https://github\.com/keakon/chord/(?:archive/refs/tags|releases/download)/v(\d+\.\d+\.\d+)(?:\.tar\.gz"|/)}).flatten.uniq
    raise "Cannot determine the current tap version." unless versions.length == 1
    previous = versions.first
    if (tag.delete_prefix("v").split(".").map(&:to_i) <=> previous.split(".").map(&:to_i)) <= 0
      puts "Tap is already at #{previous}; skipping #{tag}."
      return false
    end
    true
  end

  def self.render(tag, manifest)
    raise "Invalid stable release tag." unless STABLE_TAG.match?(tag)
    entries = {}
    manifest.each_line do |line|
      match = /\A([0-9a-f]{64}) [ *](\S+)\s*\z/.match(line)
      raise "Invalid SHA256SUMS format." unless match
      raise "Duplicate file in SHA256SUMS: #{match[2]}" if entries.key?(match[2])
      entries[match[2]] = match[1]
    end
    checksums = PLATFORMS.to_h do |platform|
      [platform, entries.fetch("chord-#{tag}-#{platform}.tar.gz")]
    end
    ERB.new(File.read(TEMPLATE)).result(binding)
  end

  def self.formula(tag)
    Dir.mktmpdir("chord-checksums-") do |dir|
      command("gh", "release", "download", tag, "--repo", "keakon/chord",
              "--pattern", "SHA256SUMS", "--dir", dir)
      render(tag, File.read(File.join(dir, "SHA256SUMS")))
    end
  end

  def self.prepare(tag, tap)
    return false unless release(tag) && newer?(tag, tap)
    contents = formula(tag)
    FileUtils.mkdir_p(File.join(tap, "Formula"))
    File.write(File.join(tap, "Formula/chord.rb"), contents)
    true
  end

  def self.publish(tag, tap, candidate)
    # Recheck after all platform jobs: another release may have completed meanwhile.
    return false unless release(tag) && newer?(tag, tap)
    contents = File.read(candidate)
    raise "Candidate formula does not match the current release; rerun the full validation." unless contents == formula(tag)
    raise "Tap working tree must be clean." unless command("git", "-C", tap, "status", "--porcelain").empty?
    FileUtils.mkdir_p(File.join(tap, "Formula"))
    File.write(File.join(tap, "Formula/chord.rb"), contents)
    command("git", "-C", tap, "add", "--", "Formula/chord.rb")
    command("git", "-C", tap, "-c", "user.name=keakon",
            "-c", "user.email=1155229+keakon@users.noreply.github.com",
            "commit", "-m", "chord #{tag.delete_prefix('v')}")
    # A non-fast-forward push fails without overwriting concurrent changes.
    command("git", "-C", tap, "push", "origin", "HEAD:main")
    true
  end
end

if $PROGRAM_NAME == __FILE__
  begin
    action, tag, tap, candidate = ARGV
    updated = case action
    when "prepare"
      raise "Usage: homebrew.rb prepare <tag> <tap directory>" unless ARGV.length == 3
      HomebrewRelease.prepare(tag, tap)
    when "publish"
      raise "Usage: homebrew.rb publish <tag> <tap directory> <validated formula>" unless ARGV.length == 4
      HomebrewRelease.publish(tag, tap, candidate)
    else
      raise "Action must be prepare or publish."
    end
    File.open(ENV.fetch("GITHUB_OUTPUT"), "a") { |file| file.puts "updated=#{updated}" } if ENV["GITHUB_OUTPUT"]
  rescue StandardError => error
    warn "Homebrew update failed: #{error.message}"
    exit 1
  end
end
