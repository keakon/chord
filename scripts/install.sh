#!/usr/bin/env bash
# Install a published Chord release binary.
#
#   curl -fsSL https://github.com/keakon/chord/releases/latest/download/install.sh | bash
#
# The script downloads the release archive for this machine, verifies it against
# the release's SHA256SUMS, and installs the binary into a directory that is
# already on PATH (/usr/local/bin by default). It never edits your shell
# profile: when the install directory is not on PATH, it prints the export line
# instead.
#
# Options:
#   --version <tag>     Install a specific release (for example v0.8.3).
#                       Defaults to the latest published release.
#   --install-dir <dir> Install into <dir> instead of /usr/local/bin.
#   --help              Show this help.
#
# Environment overrides:
#   CHORD_INSTALL_DIR        Same as --install-dir.
#   CHORD_INSTALL_VERSION    Same as --version.
#   CHORD_INSTALL_BASE_URL   Release asset base URL.
#   CHORD_INSTALL_LATEST_URL URL that redirects to the latest release.
#   CHORD_INSTALL_OS         Override the detected OS (linux, darwin).
#   CHORD_INSTALL_ARCH       Override the detected architecture (amd64, arm64).
#   CHORD_INSTALL_NO_SUDO    Set to 1 to never escalate with sudo.
#
# CHORD_INSTALL_DIR and CHORD_INSTALL_VERSION mirror the flags above. The
# remaining overrides exist so scripts/test_install.sh can drive the script
# against a local fixture without touching the network.

set -euo pipefail

readonly REPO_URL="https://github.com/keakon/chord"
readonly DEFAULT_INSTALL_DIR="/usr/local/bin"

base_url="${CHORD_INSTALL_BASE_URL:-${REPO_URL}/releases/download}"
latest_url="${CHORD_INSTALL_LATEST_URL:-${REPO_URL}/releases/latest}"
install_dir="${CHORD_INSTALL_DIR:-$DEFAULT_INSTALL_DIR}"
release_tag="${CHORD_INSTALL_VERSION:-}"

log() {
	printf '%s\n' "$*"
}

die() {
	printf 'install.sh: %s\n' "$*" >&2
	exit 1
}

usage() {
	cat <<'EOF'
Usage: install.sh [--version <tag>] [--install-dir <dir>] [--help]

Options:
  --version <tag>     Install a specific release (for example v0.8.3).
                      Defaults to the latest published release.
  --install-dir <dir> Install into <dir> instead of /usr/local/bin.
  --help              Show this help.
EOF
}

while [ "$#" -gt 0 ]; do
	case "$1" in
		--version)
			[ "$#" -ge 2 ] || die "--version needs a release tag"
			release_tag="$2"
			shift 2
			;;
		--version=*)
			release_tag="${1#*=}"
			shift
			;;
		--install-dir)
			[ "$#" -ge 2 ] || die "--install-dir needs a directory"
			install_dir="$2"
			shift 2
			;;
		--install-dir=*)
			install_dir="${1#*=}"
			shift
			;;
		-h | --help)
			usage
			exit 0
			;;
		*)
			usage >&2
			die "unknown option: $1"
			;;
	esac
done

command -v curl >/dev/null 2>&1 || die "curl is required"
command -v tar >/dev/null 2>&1 || die "tar is required"

detect_os() {
	case "$(uname -s)" in
		Linux) printf 'linux\n' ;;
		Darwin) printf 'darwin\n' ;;
		*) printf 'unknown\n' ;;
	esac
}

detect_arch() {
	case "$(uname -m)" in
		x86_64 | amd64) printf 'amd64\n' ;;
		arm64 | aarch64) printf 'arm64\n' ;;
		*) printf 'unknown\n' ;;
	esac
}

target_os="${CHORD_INSTALL_OS:-$(detect_os)}"
target_arch="${CHORD_INSTALL_ARCH:-$(detect_arch)}"

case "$target_os" in
	linux | darwin) ;;
	*)
		die "unsupported operating system: ${target_os}. This script installs on Linux and macOS; Windows and other systems should download the archive from ${REPO_URL}/releases"
		;;
esac

case "$target_arch" in
	amd64 | arm64) ;;
	*)
		die "unsupported architecture: ${target_arch}. Published binaries cover amd64 and arm64"
		;;
esac

if [ -z "$release_tag" ]; then
	log "Resolving the latest release..."
	# GitHub redirects /releases/latest to /releases/tag/<tag>; the last path
	# segment of the effective URL is the release tag.
	effective_url="$(curl -fsSL -o /dev/null -w '%{url_effective}' "$latest_url")" ||
		die "could not reach ${latest_url}. Pass --version <tag> to install a specific release"
	release_tag="${effective_url##*/}"
fi

if ! printf '%s' "$release_tag" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+$'; then
	die "unexpected release tag: ${release_tag}"
fi

readonly asset="chord-${release_tag}-${target_os}-${target_arch}.tar.gz"

tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/chord-install.XXXXXX")"
trap 'rm -rf "$tmp_dir"' EXIT

log "Downloading ${asset}..."
curl -fsSL -o "${tmp_dir}/${asset}" "${base_url}/${release_tag}/${asset}" ||
	die "failed to download ${asset} from ${base_url}/${release_tag}"

curl -fsSL -o "${tmp_dir}/SHA256SUMS" "${base_url}/${release_tag}/SHA256SUMS" ||
	die "failed to download SHA256SUMS from ${base_url}/${release_tag}"

sha256_of() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | awk '{print $1}'
	elif command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$1" | awk '{print $1}'
	else
		die "neither sha256sum nor shasum is available to verify the download"
	fi
}

# The release writes "sha256sum *", so the digest is the first field and the
# file name is the second one, optionally prefixed with "*" for binary mode.
expected_digest="$(awk -v name="$asset" '{ n = $2; sub(/^\*/, "", n); if (n == name) print $1 }' "${tmp_dir}/SHA256SUMS")"
[ -n "$expected_digest" ] || die "SHA256SUMS does not list ${asset}"

actual_digest="$(sha256_of "${tmp_dir}/${asset}")"
if [ "$expected_digest" != "$actual_digest" ]; then
	die "checksum mismatch for ${asset}: expected ${expected_digest}, got ${actual_digest}"
fi
log "Checksum verified."

tar -xzf "${tmp_dir}/${asset}" -C "$tmp_dir"
readonly binary="${tmp_dir}/chord-${release_tag}-${target_os}-${target_arch}/chord"
[ -f "$binary" ] || die "the archive does not contain the expected chord binary"

install_binary() {
	local source="$1" dir="$2" target="${2}/chord"
	if [ ! -d "$dir" ]; then
		mkdir -p "$dir" 2>/dev/null || true
	fi
	if [ -d "$dir" ] && [ -w "$dir" ]; then
		install -m 0755 "$source" "$target" || return 1
		return 0
	fi
	if [ "${CHORD_INSTALL_NO_SUDO:-0}" = "1" ]; then
		return 1
	fi
	if command -v sudo >/dev/null 2>&1; then
		sudo install -d -m 0755 "$dir" || return 1
		sudo install -m 0755 "$source" "$target" || return 1
		return 0
	fi
	return 1
}

if ! install_binary "$binary" "$install_dir"; then
	die "cannot write to ${install_dir}. Re-run with --install-dir \"\$HOME/.local/bin\" and add that directory to your PATH"
fi

log ""
log "Installed chord to ${install_dir}/chord"

case ":${PATH:-}:" in
	*":${install_dir}:"*) ;;
	*)
		log ""
		log "${install_dir} is not on your PATH. Add it with:"
		log "  export PATH=\"${install_dir}:\$PATH\""
		;;
esac

if ! "${install_dir}/chord" --version; then
	log ""
	log "The binary is installed but 'chord --version' failed. Report it at ${REPO_URL}/issues"
fi
