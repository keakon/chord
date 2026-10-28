#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/bin" "$tmp/home" "$tmp/volumes"

cat >"$tmp/bin/diskutil" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
case "$1" in
	info)
		printf 'mock plist\n'
		;;
	erasevolume)
		mkdir -p "$(dirname "$MOCK_MOUNT")/$3"
		printf '%s\n%s\n%s\n' "$MOCK_DEVICE" "$MOCK_UUID" "$MOCK_PARENT" >"$MOCK_DEVICE_FILE"
		;;
	eject)
		printf '%s\n' "$2" >>"$MOCK_EJECT_LOG"
		[[ "${MOCK_EJECT_FAIL:-0}" != 1 ]] || exit 1
		# Models ejecting a disk that does not back the shared mount: the path is
		# another disk's volume and stays mounted.
		[[ "${MOCK_EJECT_KEEPS_MOUNT:-0}" != 1 ]] || exit 0
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
[[ "${MOCK_FAIL_FIELD:-}" != "$2" ]] || exit 1
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
export MOCK_MOUNT="$tmp/volumes/ChordTestRam"
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

rmdir "$owner_dir"
: >"$owner_dir"
before_eject="$(wc -l <"$MOCK_EJECT_LOG" | tr -d ' ')"
if ! output="$(resolve_darwin_tmpdir 2>"$tmp/unwritable.err")" || [[ "$output" != "$mount_point" ]]; then
	echo "creation dropped a mount it created when the owner record could not be written: $output" >&2
	exit 1
fi
grep -qF 'ownership could not be recorded' "$tmp/unwritable.err" || {
	echo "unwritable owner record was kept silently" >&2
	cat "$tmp/unwritable.err" >&2
	exit 1
}
grep -qF "eject it manually with diskutil eject /dev/disk42" "$tmp/unwritable.err" || {
	echo "record-less mount warning does not say how to eject it" >&2
	cat "$tmp/unwritable.err" >&2
	exit 1
}
[[ "$(wc -l <"$MOCK_EJECT_LOG" | tr -d ' ')" == "$before_eject" ]] || { echo "record-less mount was ejected" >&2; exit 1; }
[[ ! -e "$owner_file" ]] || { echo "unwritable owner record path produced a record" >&2; exit 1; }
rm -f "$owner_dir"
mkdir -p "$owner_dir"

rmdir "$mount_point"
MOCK_PHYSICAL_STORE="disk99"
export MOCK_PHYSICAL_STORE
MOCK_EJECT_KEEPS_MOUNT=1
export MOCK_EJECT_KEEPS_MOUNT
before_eject="$(wc -l <"$MOCK_EJECT_LOG" | tr -d ' ')"
before_attach="$(wc -l <"$MOCK_HDIUTIL_LOG" | tr -d ' ')"
if ! output="$(resolve_darwin_tmpdir 2>"$tmp/impostor.err")"; then
	echo "creation failed instead of falling back to a private mount" >&2
	cat "$tmp/impostor.err" >&2
	exit 1
fi
[[ "$output" == "$tmp/volumes/${volume_name}-"* ]] || { echo "shared mount was not backed by the attached disk, yet creation kept using it: $output" >&2; exit 1; }
[[ -d "$output" ]] || { echo "private mount does not exist: $output" >&2; exit 1; }
grep -qF "leaving ${mount_point} untouched and using ${output} instead" "$tmp/impostor.err" || {
	echo "private mount warning does not name the untouched shared path" >&2
	cat "$tmp/impostor.err" >&2
	exit 1
}
grep -qF "scripts/test_tmpdir.sh --release ${output}" "$tmp/impostor.err" || {
	echo "private mount warning does not say how to release it" >&2
	cat "$tmp/impostor.err" >&2
	exit 1
}
[[ "$(wc -l <"$MOCK_EJECT_LOG" | tr -d ' ')" == "$((before_eject + 1))" ]] || { echo "the disk attached for the shared mount was not ejected" >&2; exit 1; }
[[ "$(tail -n 1 "$MOCK_EJECT_LOG")" == "/dev/disk42" ]] || { echo "the wrong disk was ejected while falling back to a private mount" >&2; exit 1; }
if grep -qF 'was attached for' "$tmp/impostor.err"; then
	echo "private mount reported a disk it had already ejected" >&2
	cat "$tmp/impostor.err" >&2
	exit 1
fi
[[ "$(wc -l <"$MOCK_HDIUTIL_LOG" | tr -d ' ')" == "$((before_attach + 2))" ]] || { echo "private mount did not attach a second disk" >&2; exit 1; }
[[ -d "$mount_point" ]] || { echo "untouched shared mount disappeared" >&2; exit 1; }
[[ ! -e "$owner_file" ]] || { echo "private mount wrote the shared owner record" >&2; exit 1; }

before_eject="$(wc -l <"$MOCK_EJECT_LOG" | tr -d ' ')"
release_private_ram_disk "$mount_point" || { echo "releasing the shared mount failed" >&2; exit 1; }
release_private_ram_disk "$tmp/volumes/${volume_name}-manual" || { echo "releasing an unrelated path failed" >&2; exit 1; }
[[ "$(wc -l <"$MOCK_EJECT_LOG" | tr -d ' ')" == "$before_eject" ]] || { echo "release ejected a disk that is not a private RAM disk" >&2; exit 1; }
release_private_ram_disk "$output" || { echo "private RAM disk was not released" >&2; exit 1; }
[[ "$(wc -l <"$MOCK_EJECT_LOG" | tr -d ' ')" == "$((before_eject + 1))" ]] || { echo "release did not eject the private RAM disk" >&2; exit 1; }
[[ "$(tail -n 1 "$MOCK_EJECT_LOG")" == "/dev/${MOCK_PHYSICAL_STORE}" ]] || { echo "release ejected something other than the private disk's physical store" >&2; exit 1; }
[[ -d "$mount_point" ]] || { echo "releasing the private disk took down the shared mount" >&2; exit 1; }
if MOCK_EJECT_FAIL=1 release_private_ram_disk "$output" 2>"$tmp/release.err"; then
	echo "a failed release reported success" >&2
	exit 1
fi
grep -qF "eject it manually with diskutil eject ${output}" "$tmp/release.err" || {
	echo "failed release does not say how to eject the private disk" >&2
	cat "$tmp/release.err" >&2
	exit 1
}
unset MOCK_EJECT_KEEPS_MOUNT

MOCK_PHYSICAL_STORE="disk42"
export MOCK_PHYSICAL_STORE
MOCK_FAIL_FIELD="DeviceNode"
export MOCK_FAIL_FIELD
rmdir "$mount_point"
before_eject="$(wc -l <"$MOCK_EJECT_LOG" | tr -d ' ')"
if ! output="$(resolve_darwin_tmpdir 2>"$tmp/unidentifiable.err")"; then
	echo "creation failed instead of falling back to a private mount" >&2
	cat "$tmp/unidentifiable.err" >&2
	exit 1
fi
[[ "$output" == "$tmp/volumes/${volume_name}-"* ]] || { echo "unverifiable shared mount was used: $output" >&2; exit 1; }
grep -qF 'created RAM disk identity could not be verified' "$tmp/unidentifiable.err" || {
	echo "unverifiable shared mount was replaced without the expected warning" >&2
	cat "$tmp/unidentifiable.err" >&2
	exit 1
}
grep -qF 'stays attached; eject it manually with diskutil eject /dev/disk42' "$tmp/unidentifiable.err" || {
	echo "unverifiable shared mount did not say how to eject the disk it left attached" >&2
	cat "$tmp/unidentifiable.err" >&2
	exit 1
}
[[ "$(wc -l <"$MOCK_EJECT_LOG" | tr -d ' ')" == "$before_eject" ]] || { echo "a mount was ejected while verifying identity" >&2; exit 1; }
[[ -d "$mount_point" ]] || { echo "unverifiable shared mount disappeared" >&2; exit 1; }
unset MOCK_FAIL_FIELD

echo "Chord tempdir reuse and ownership tests passed"
