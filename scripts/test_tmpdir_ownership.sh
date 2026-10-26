#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/bin" "$tmp/home"

cat >"$tmp/bin/diskutil" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
case "$1" in
	info)
		printf 'mock plist\n'
		;;
	erasevolume)
		mkdir -p "$MOCK_MOUNT"
		printf '%s\n%s\n%s\n' "$MOCK_DEVICE" "$MOCK_UUID" "$MOCK_PARENT" >"$MOCK_DEVICE_FILE"
		;;
	eject)
		printf '%s\n' "$2" >>"$MOCK_EJECT_LOG"
		[[ "${MOCK_EJECT_FAIL:-0}" != 1 ]] || exit 1
		rmdir "$MOCK_MOUNT"
		;;
	*)
		exit 2
		;;
esac
EOF
cat >"$tmp/bin/plutil" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
case "$2" in
	DeviceNode) sed -n '1p' "$MOCK_DEVICE_FILE" ;;
	VolumeUUID) sed -n '2p' "$MOCK_DEVICE_FILE" ;;
	ParentWholeDisk) sed -n '3p' "$MOCK_DEVICE_FILE" ;;
	APFSPhysicalStores) printf '[{"APFSPhysicalStore":"%s"}]\n' "$MOCK_PHYSICAL_STORE" ;;
	*) exit 2 ;;
esac
EOF
cat >"$tmp/bin/hdiutil" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf 'attached\n' >>"$MOCK_HDIUTIL_LOG"
printf '/dev/disk42\n'
EOF
chmod +x "$tmp/bin/"*

export PATH="$tmp/bin:$PATH"
export MOCK_MOUNT="$tmp/mount"
export MOCK_DEVICE_FILE="$tmp/device"
export MOCK_EJECT_LOG="$tmp/eject.log"
export MOCK_HDIUTIL_LOG="$tmp/hdiutil.log"
export MOCK_DEVICE="/dev/disk43s1"
export MOCK_UUID="00000000-0000-4000-8000-000000000042"
export MOCK_PARENT="disk43"
export MOCK_PHYSICAL_STORE="disk42"
export HOME="$tmp/home"
: >"$MOCK_EJECT_LOG"
: >"$MOCK_HDIUTIL_LOG"

source "$repo_root/scripts/test_tmpdir.sh"
mount_point="$MOCK_MOUNT"
[[ "$owner_file" == "$HOME/Library/Caches/chord-test-tmpdir/test-tmpdir-owner" ]] || {
	echo "Chord does not use the shared owner record path: $owner_file" >&2
	exit 1
}

mkdir -p "$mount_point"
printf '%s\n%s\n%s\n' "$MOCK_DEVICE" "$MOCK_UUID" "$MOCK_PARENT" >"$MOCK_DEVICE_FILE"
if ! output="$(resolve_darwin_tmpdir 2>"$tmp/unowned.err")" || [[ "$output" != "$mount_point" ]]; then
	echo "resolver did not reuse an existing usable mount without the shared ownership record: $output" >&2
	exit 1
fi
[[ ! -s "$tmp/unowned.err" ]] || { echo "resolver warned while reusing an unowned mount" >&2; cat "$tmp/unowned.err" >&2; exit 1; }
[[ "$(wc -l <"$MOCK_HDIUTIL_LOG" | tr -d ' ')" == 0 ]] || { echo "resolver created a second disk over an unowned mount" >&2; exit 1; }
if destroy_owned_ram_disk 2>"$tmp/unowned-destroy.err"; then
	echo "destroy accepted a mount without the shared ownership record" >&2
	exit 1
fi
[[ "$(wc -l <"$MOCK_EJECT_LOG" | tr -d ' ')" == 0 ]] || { echo "unowned mount was ejected" >&2; exit 1; }

rmdir "$mount_point"
if ! output="$(resolve_darwin_tmpdir)" || [[ "$output" != "$mount_point" ]]; then
	echo "RAM disk creation did not return the owned mount: $output" >&2
	exit 1
fi
[[ -f "$owner_file" ]] || { echo "Chord did not write the shared owner record" >&2; exit 1; }
grep -qx "$MOCK_PARENT" <(sed -n '3p' "$owner_file")
grep -qx "$MOCK_PHYSICAL_STORE" <(sed -n '4p' "$owner_file")
[[ "$(wc -l <"$MOCK_HDIUTIL_LOG" | tr -d ' ')" == 1 ]]

if ! output="$(resolve_darwin_tmpdir)" || [[ "$output" != "$mount_point" ]]; then
	echo "owned RAM disk was not reused: $output" >&2
	exit 1
fi
[[ "$(wc -l <"$MOCK_HDIUTIL_LOG" | tr -d ' ')" == 1 ]]

MOCK_UUID="00000000-0000-4000-8000-000000000099"
export MOCK_UUID
printf '%s\n%s\n%s\n' "$MOCK_DEVICE" "$MOCK_UUID" "$MOCK_PARENT" >"$MOCK_DEVICE_FILE"
if destroy_owned_ram_disk 2>"$tmp/mismatch.err"; then
	echo "destroy accepted a mismatched volume UUID" >&2
	exit 1
fi
[[ "$(wc -l <"$MOCK_EJECT_LOG" | tr -d ' ')" == 0 ]] || { echo "mismatched volume was ejected" >&2; exit 1; }
MOCK_UUID="00000000-0000-4000-8000-000000000042"
export MOCK_UUID
printf '%s\n%s\n%s\n' "$MOCK_DEVICE" "$MOCK_UUID" "$MOCK_PARENT" >"$MOCK_DEVICE_FILE"

eject_count="$(wc -l <"$MOCK_EJECT_LOG" | tr -d ' ')"
MOCK_PHYSICAL_STORE="disk99"
export MOCK_PHYSICAL_STORE
if destroy_owned_ram_disk 2>"$tmp/store-mismatch.err"; then
	echo "destroy accepted a physical store that no longer backs the container" >&2
	exit 1
fi
[[ "$(wc -l <"$MOCK_EJECT_LOG" | tr -d ' ')" == "$eject_count" ]] || { echo "physical store mismatch was ejected" >&2; exit 1; }
[[ -f "$owner_file" ]] || { echo "physical store mismatch discarded the shared owner record" >&2; exit 1; }
MOCK_PHYSICAL_STORE="disk42"
export MOCK_PHYSICAL_STORE

head -n 3 "$owner_file" >"$tmp/legacy-owner"
cat "$tmp/legacy-owner" >"$owner_file"
if destroy_owned_ram_disk 2>"$tmp/legacy.err"; then
	echo "destroy accepted a three-line owner record" >&2
	exit 1
fi
[[ "$(wc -l <"$MOCK_EJECT_LOG" | tr -d ' ')" == "$eject_count" ]] || { echo "three-line owner record was ejected" >&2; exit 1; }
printf '%s\n%s\n%s\n%s\n' "$MOCK_DEVICE" "$MOCK_UUID" "$MOCK_PARENT" "$MOCK_PHYSICAL_STORE" >"$owner_file"

MOCK_EJECT_FAIL=1
export MOCK_EJECT_FAIL
if destroy_owned_ram_disk 2>"$tmp/eject.err"; then
	echo "destroy reported success after diskutil eject failed" >&2
	exit 1
fi
[[ -f "$owner_file" ]] || { echo "failed eject discarded the shared owner record" >&2; exit 1; }
MOCK_EJECT_FAIL=0
export MOCK_EJECT_FAIL
destroy_owned_ram_disk
[[ ! -e "$owner_file" ]] || { echo "successful eject retained the shared owner record" >&2; exit 1; }
[[ "$(tail -n 1 "$MOCK_EJECT_LOG")" == "/dev/$MOCK_PHYSICAL_STORE" ]] || {
	echo "destroy did not eject the recorded physical store" >&2
	exit 1
}

before_cleanup="$(wc -l <"$MOCK_EJECT_LOG" | tr -d ' ')"
MOCK_PHYSICAL_STORE="disk99"
export MOCK_PHYSICAL_STORE
output="$(resolve_darwin_tmpdir)"
if [[ -n "$output" ]]; then
	echo "creation returned a mount whose physical store did not match the attached disk: $output" >&2
	exit 1
fi
[[ "$(sed -n "$((before_cleanup + 1))p" "$MOCK_EJECT_LOG")" == "/dev/disk42" ]] || {
	echo "failed ownership verification did not eject only its newly attached device" >&2
	exit 1
}
[[ ! -e "$owner_file" ]] || { echo "failed ownership verification wrote a shared owner record" >&2; exit 1; }

echo "Chord tempdir reuse and ownership tests passed"
