#!/usr/bin/env bash
# Regression test for scripts/install.sh.
#
# The test drives the installer against a local fixture served over file:// so
# it never touches the network: it builds a fake release archive plus its
# SHA256SUMS, then checks the success path and every failure path that must
# stop the install.
set -euo pipefail

repo_root=$(cd "$(dirname "$0")/.." && pwd)
installer="$repo_root/scripts/install.sh"
fixture=$(mktemp -d "${TMPDIR:-/tmp}/chord-install-test.XXXXXX")
trap 'rm -rf "$fixture"' EXIT

fail() {
	echo "install script test failed: $*" >&2
	exit 1
}

tag="v1.2.3"
os="linux"
arch="amd64"
no_sudo="0"
base_url="file://${fixture}/releases/download"
asset="chord-${tag}-${os}-${arch}.tar.gz"
release_dir="${fixture}/releases/download/${tag}"

bash -n "$installer" || fail "installer does not parse"

mkdir -p "${release_dir}" "${fixture}/stage/chord-${tag}-${os}-${arch}" "${fixture}/releases/tag"

cat > "${fixture}/stage/chord-${tag}-${os}-${arch}/chord" <<'SH'
#!/usr/bin/env bash
echo "chord test-installer 1.2.3"
SH
chmod +x "${fixture}/stage/chord-${tag}-${os}-${arch}/chord"
echo "license text" > "${fixture}/stage/chord-${tag}-${os}-${arch}/LICENSE"
tar -C "${fixture}/stage" -czf "${release_dir}/${asset}" "chord-${tag}-${os}-${arch}"

if command -v sha256sum >/dev/null 2>&1; then
	(cd "$release_dir" && sha256sum "$asset" > SHA256SUMS)
else
	(cd "$release_dir" && shasum -a 256 "$asset" > SHA256SUMS)
fi

# The installer resolves the latest release by following the redirect and
# reading the tag from the last path segment, so the fixture only needs a file
# at that path.
: > "${fixture}/releases/tag/${tag}"

install_with() {
	local dir="$1"
	shift
	env \
		CHORD_INSTALL_BASE_URL="$base_url" \
		CHORD_INSTALL_LATEST_URL="file://${fixture}/releases/tag/${tag}" \
		CHORD_INSTALL_OS="$os" \
		CHORD_INSTALL_ARCH="$arch" \
		CHORD_INSTALL_DIR="$dir" \
		CHORD_INSTALL_NO_SUDO="$no_sudo" \
		bash "$installer" "$@"
}

# 1. The default path resolves the latest release and installs the binary.
install_dir="${fixture}/bin"
if ! install_with "$install_dir" > "${fixture}/latest.log" 2>&1; then
	fail "installer failed: $(cat "${fixture}/latest.log")"
fi
[ -x "${install_dir}/chord" ] || fail "chord was not installed"
"${install_dir}/chord" | grep -q "test-installer" || fail "the installed binary is not the fixture binary"
grep -q "Checksum verified" "${fixture}/latest.log" || fail "the installer did not report checksum verification"

# 2. An explicit version installs into a fresh directory.
pinned_dir="${fixture}/bin-pinned"
if ! install_with "$pinned_dir" --version "$tag" > "${fixture}/pinned.log" 2>&1; then
	fail "installer failed with an explicit version: $(cat "${fixture}/pinned.log")"
fi
[ -x "${pinned_dir}/chord" ] || fail "chord was not installed with an explicit version"

# 3. A checksum mismatch stops the install.
bad_release="${fixture}/bad/download/${tag}"
mkdir -p "$bad_release"
cp "${release_dir}/${asset}" "${bad_release}/${asset}"
printf '%s  %s\n' \
	"0000000000000000000000000000000000000000000000000000000000000000" "$asset" \
	> "${bad_release}/SHA256SUMS"
base_url="file://${fixture}/bad/download"
bad_dir="${fixture}/bin-bad"
if install_with "$bad_dir" > "${fixture}/bad.log" 2>&1; then
	fail "the installer accepted an archive with a mismatched checksum"
fi
grep -q "checksum mismatch" "${fixture}/bad.log" || fail "the installer did not report the checksum mismatch"
[ ! -e "${bad_dir}/chord" ] || fail "the installer installed a binary with a mismatched checksum"
base_url="file://${fixture}/releases/download"

# 4. A missing asset stops the install.
empty_release="${fixture}/empty/download/${tag}"
mkdir -p "$empty_release"
base_url="file://${fixture}/empty/download"
if install_with "${fixture}/bin-empty" > "${fixture}/empty.log" 2>&1; then
	fail "the installer accepted a release without the requested asset"
fi
grep -q "failed to download" "${fixture}/empty.log" || fail "the installer did not report the missing asset"
base_url="file://${fixture}/releases/download"

# 5. An unsupported architecture stops the install.
arch="riscv64"
if install_with "${fixture}/bin-arch" > "${fixture}/arch.log" 2>&1; then
	fail "the installer accepted an unsupported architecture"
fi
grep -q "unsupported architecture" "${fixture}/arch.log" || fail "the installer did not report the unsupported architecture"
arch="amd64"

# 6. An unwritable install directory without sudo stops the install and points
#    at the alternative flag.
if [ "$(id -u)" -ne 0 ]; then
	readonly_dir="${fixture}/readonly"
	mkdir -p "$readonly_dir"
	chmod 555 "$readonly_dir"
	no_sudo="1"
	if install_with "$readonly_dir" > "${fixture}/readonly.log" 2>&1; then
		fail "the installer wrote to an unwritable directory"
	fi
	grep -q -- "--install-dir" "${fixture}/readonly.log" ||
		fail "the installer did not point at the --install-dir alternative"
	no_sudo="0"
fi

# 7. A release tag that is not a version stops the install.
if install_with "${fixture}/bin-tag" --version "not-a-tag" > "${fixture}/tag.log" 2>&1; then
	fail "the installer accepted an invalid release tag"
fi
grep -q "unexpected release tag" "${fixture}/tag.log" || fail "the installer did not report the invalid release tag"

# 8. --help succeeds and an unknown option fails.
bash "$installer" --help > "${fixture}/help.log" 2>&1 || fail "--help failed"
grep -q -- "--install-dir" "${fixture}/help.log" || fail "--help did not print the options"
if bash "$installer" --nope > "${fixture}/nope.log" 2>&1; then
	fail "the installer accepted an unknown option"
fi

# Installation failures must propagate even when a previous binary still runs.
mkdir -p "${fixture}/stubs"
cat > "${fixture}/stubs/install" <<'SH'
#!/usr/bin/env bash
exit 73
SH
cat > "${fixture}/stubs/sudo" <<'SH'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$SUDO_CALLS"
if [ "$2" = "-d" ] && [ "${SUDO_FAIL_CREATE:-0}" != "1" ]; then
	exit 0
fi
exit 74
SH
chmod +x "${fixture}/stubs/install" "${fixture}/stubs/sudo"
PATH="${fixture}/stubs:${PATH}"
no_sudo="1"
if install_with "$install_dir" > "${fixture}/write-failed.log" 2>&1; then
	fail "installer accepted a failed binary replacement"
fi
if grep -q "Installed chord" "${fixture}/write-failed.log"; then
	fail "installer reported success after a failed write"
fi

# A file as parent forces the sudo branch without relying on UID or permissions.
touch "${fixture}/parent-file"
no_sudo="0"
export SUDO_CALLS="${fixture}/sudo-calls"
for create_failure in 0 1; do
	export SUDO_FAIL_CREATE="$create_failure"
	: > "$SUDO_CALLS"
	if install_with "${fixture}/parent-file/bin" > "${fixture}/sudo-failed.log" 2>&1; then
		fail "installer accepted a failed sudo install"
	fi
	expected_calls=$((2 - create_failure))
	[ "$(wc -l < "$SUDO_CALLS" | tr -d ' ')" = "$expected_calls" ] ||
		fail "installer continued after sudo directory creation failed"
	if grep -q "Installed chord" "${fixture}/sudo-failed.log"; then
		fail "installer reported success after sudo failed"
	fi
done

echo "install script test passed"
