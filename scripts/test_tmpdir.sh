#!/usr/bin/env bash
# Resolve a memory-backed temp directory for local test runs.
#
# The Go test suite is dominated by durable writes: every persistence write does
# an fsync, which on a real disk costs milliseconds and cannot be hidden by
# parallelism. Pointing TMPDIR at a memory-backed filesystem keeps the same code
# paths and durability semantics while removing that latency.
#
# Resolution order (first writable directory wins):
#   1. $CHORD_TEST_TMPDIR
#   2. .chord/test-tmpdir (repo-local, untracked) — first non-empty, non-comment line
#   3. platform default: /dev/shm on Linux; a reused-or-created APFS RAM disk on macOS
#
# Prints the chosen directory, or nothing when no memory-backed filesystem is
# available, so callers fall back to the default temp dir. Windows has no
# built-in memory filesystem and Go ignores TMPDIR there; point TMP or TEMP at a
# RAM disk or fast SSD instead (see CONTRIBUTING.md).
#
# Usage:
#   scripts/test_tmpdir.sh            # print the directory to use
#   scripts/test_tmpdir.sh --destroy  # tear down the macOS RAM disk this may create
#   scripts/test_tmpdir.sh --release DIR  # eject DIR if it is a private RAM disk

set -euo pipefail

volume_name="ChordTestRam"
# 4 GiB expressed in 512-byte sectors, the unit `ram://` expects. Sized for a
# cold Go build cache, where the build work dir holds every compiled package
# archive and can exceed 1 GiB. The disk is sparse, so only written data is
# backed by memory.
size_sectors=$((4 * 1024 * 1024 * 1024 / 512))
mount_point="/Volumes/${volume_name}"
owner_dir="${HOME:-}/Library/Caches/chord-test-tmpdir"
owner_file="${owner_dir}/test-tmpdir-owner"

# usable reports whether dir can back TMPDIR for go test: it must exist, be
# writable, and allow executing files, because Go builds the test binary inside
# TMPDIR and execs it. A noexec mount (the default inside containers, and often
# for /dev/shm) is writable but fails there with "fork/exec ...: permission
# denied".
usable() {
	local dir="${1:-}"
	[[ -n "$dir" && -d "$dir" && -w "$dir" ]] || return 1
	local probe ok=1
	probe="$(mktemp "${dir%/}/chord-exec-probe.XXXXXX" 2>/dev/null)" || return 1
	if (set +e; printf '#!/bin/sh\nexit 0\n' >"${probe}" && chmod +x "${probe}" && "${probe}") >/dev/null 2>&1; then
		ok=0
	fi
	rm -f "${probe}"
	return "${ok}"
}

mounted_identity() {
	local dir="${1:-${mount_point}}"
	[[ -d "${dir}" ]] || return 1
	local info device uuid parent
	info="$(diskutil info -plist "${dir}" 2>/dev/null)" || return 1
	device="$(printf '%s' "${info}" | plutil -extract DeviceNode raw -o - - 2>/dev/null)" || return 1
	uuid="$(printf '%s' "${info}" | plutil -extract VolumeUUID raw -o - - 2>/dev/null)" || return 1
	parent="$(printf '%s' "${info}" | plutil -extract ParentWholeDisk raw -o - - 2>/dev/null)" || return 1
	[[ "${device}" =~ ^/dev/disk[0-9]+s?[0-9]*$ ]] || return 1
	[[ ${#uuid} -eq 36 && "${uuid}" =~ ^[[:xdigit:]-]+$ ]] || return 1
	[[ "${parent}" =~ ^disk[0-9]+$ ]] || return 1
	printf '%s\n%s\n%s\n' "${device}" "${uuid}" "${parent}"
}

# APFS puts a formatted RAM disk's volume in a synthesized container: the
# attached device (diskN) is the container's physical store, while the mounted
# volume reports diskN+1 as its parent. Verify that relationship instead of
# requiring the two identifiers to match.
parent_has_physical_store() {
	local parent="${1:-}" device="${2:-}" info stores
	[[ "${parent}" =~ ^disk[0-9]+$ && "${device}" =~ ^disk[0-9]+$ ]] || return 1
	info="$(diskutil info -plist "/dev/${parent}" 2>/dev/null)" || return 1
	stores="$(printf '%s' "${info}" | plutil -extract APFSPhysicalStores json -o - - 2>/dev/null)" || return 1
	printf '%s' "${stores}" | grep -Eq "\"APFSPhysicalStore\"[[:space:]]*:[[:space:]]*\"${device}\""
}

owned_mount_identity() {
	[[ -f "${owner_file}" && ! -L "${owner_file}" ]] || return 1
	local recorded_device recorded_uuid recorded_parent recorded_store current
	IFS= read -r recorded_device <"${owner_file}" || return 1
	IFS= read -r recorded_uuid < <(sed -n '2p' "${owner_file}") || return 1
	IFS= read -r recorded_parent < <(sed -n '3p' "${owner_file}") || return 1
	IFS= read -r recorded_store < <(sed -n '4p' "${owner_file}") || return 1
	[[ "${recorded_device}" =~ ^/dev/disk[0-9]+s?[0-9]*$ ]] || return 1
	[[ ${#recorded_uuid} -eq 36 && "${recorded_uuid}" =~ ^[[:xdigit:]-]+$ ]] || return 1
	[[ "${recorded_parent}" =~ ^disk[0-9]+$ ]] || return 1
	[[ "${recorded_store}" =~ ^disk[0-9]+$ && "${recorded_store}" != "${recorded_parent}" ]] || return 1
	current="$(mounted_identity)" || return 1
	[[ "${current}" == "${recorded_device}"$'\n'"${recorded_uuid}"$'\n'"${recorded_parent}" ]] || return 1
	parent_has_physical_store "${recorded_parent}" "${recorded_store}" || return 1
	printf '%s\n%s\n%s\n%s\n' "${recorded_device}" "${recorded_uuid}" "${recorded_parent}" "${recorded_store}"
}

write_owner_record() {
	local identity="$1" physical_store="$2" created_tmp
	[[ -n "${owner_dir}" && ! -L "${owner_dir}" ]] || return 1
	mkdir -p "${owner_dir}" || return 1
	chmod 700 "${owner_dir}" || return 1
	[[ ! -L "${owner_file}" ]] || return 1
	created_tmp="$(mktemp "${owner_file}.XXXXXX")" || return 1
	if ! printf '%s\n%s\n' "${identity}" "${physical_store}" >"${created_tmp}" || ! chmod 600 "${created_tmp}" ||
		! mv -f "${created_tmp}" "${owner_file}"; then
		rm -f "${created_tmp}"
		return 1
	fi
}

destroy_owned_ram_disk() {
	[[ -d "${mount_point}" ]] || return 0
	local identity recorded_identity store current
	identity="$(owned_mount_identity)" || {
		echo "error: refusing to eject ${mount_point}; its owner cannot be verified" >&2
		return 1
	}
	store="$(sed -n '4p' <<<"${identity}")"
	recorded_identity="$(sed -n '1,3p' <<<"${identity}")"
	current="$(mounted_identity)" || {
		echo "error: refusing to eject ${mount_point}; its device identity changed" >&2
		return 1
	}
	[[ "${current}" == "${recorded_identity}" ]] || {
		echo "error: refusing to eject ${mount_point}; its device identity changed" >&2
		return 1
	}
	if ! diskutil eject "/dev/${store}" >/dev/null; then
		echo "error: failed to eject the owned RAM disk at ${mount_point}" >&2
		return 1
	fi
	rm -f "${owner_file}"
}

eject_created_device() {
	local device="$1"
	[[ "${device}" =~ ^/dev/disk[0-9]+$ ]] || return 1
	diskutil eject "${device}" >/dev/null
}

# abandon_attached_disk reports the disk this run attached for the shared mount
# when that mount cannot use it. The caller passes eject=1 only when the mount
# provably does not sit on that disk; a disk left attached stays removable only
# by hand, so the warning must name it.
abandon_attached_disk() {
	local dev="$1" eject="$2"
	if [[ "${eject}" == 1 ]] && eject_created_device "${dev}"; then
		return 0
	fi
	echo "warning: ${dev} was attached for ${mount_point} and stays attached; eject it manually with diskutil eject ${dev}" >&2
}

# Creates a RAM disk at a path that did not exist before, for the case where the
# shared mount cannot be confirmed as the disk attached in this run. The shared
# mount is left untouched rather than ejected or renamed: another process may
# already use it, and its users would fail on a path that disappears mid-run.
use_unique_ram_disk() {
	local reason="$1" attempt name target dev volumes_root
	volumes_root="$(dirname "${mount_point}")"
	for attempt in 1 2 3 4 5; do
		name="${volume_name}-$$-${RANDOM}"
		target="${volumes_root}/${name}"
		if [[ -e "${target}" ]]; then
			continue
		fi
		dev="$(hdiutil attach -nomount "ram://${size_sectors}" 2>/dev/null | awk '{print $1; exit}' || true)"
		[[ "${dev}" =~ ^/dev/disk[0-9]+$ ]] || return 1
		if ! diskutil erasevolume APFS "${name}" "${dev}" >/dev/null 2>&1 || ! usable "${target}"; then
			eject_created_device "${dev}" >/dev/null 2>&1 || true
			return 1
		fi
		echo "warning: ${reason}; leaving ${mount_point} untouched and using ${target} instead (make test-fast and race-fast eject it when the run ends; otherwise run scripts/test_tmpdir.sh --release ${target})" >&2
		printf '%s\n' "${target}"
		return 0
	done
	return 1
}

# Ejects a private disk created by use_unique_ram_disk once its run is done.
# Only that naming pattern qualifies: such a path is created fresh for one run,
# while the shared mount may serve other processes and is left to --destroy.
release_private_ram_disk() {
	local dir="${1:-}" identity parent stores store
	[[ "$(dirname "${dir}")" == "$(dirname "${mount_point}")" && "$(basename "${dir}")" =~ ^${volume_name}-[0-9]+-[0-9]+$ ]] || return 0
	[[ -d "${dir}" ]] || return 0
	if identity="$(mounted_identity "${dir}")"; then
		parent="$(sed -n '3p' <<<"${identity}")"
		stores="$(diskutil info -plist "/dev/${parent}" 2>/dev/null | plutil -extract APFSPhysicalStores json -o - - 2>/dev/null)" || stores=""
		store="$(grep -oE '"APFSPhysicalStore"[[:space:]]*:[[:space:]]*"disk[0-9]+"' <<<"${stores}" | grep -oE 'disk[0-9]+' || true)"
		if [[ "${store}" =~ ^disk[0-9]+$ ]] && diskutil eject "/dev/${store}" >/dev/null; then
			return 0
		fi
	fi
	echo "warning: could not release the private RAM disk at ${dir}; eject it manually with diskutil eject ${dir}" >&2
	return 1
}

resolve_darwin_tmpdir() {
	local identity dev dev_name parent unique
	# A usable mount is reused whether or not it has an owner record. The
	# record only authorizes --destroy to eject a disk this helper created.
	if usable "${mount_point}"; then
		echo "${mount_point}"
		return 0
	fi
	dev="$(hdiutil attach -nomount "ram://${size_sectors}" 2>/dev/null | awk '{print $1; exit}' || true)"
	if [[ ! "${dev}" =~ ^/dev/disk[0-9]+$ ]]; then
		echo "warning: could not create an APFS RAM disk; falling back to the default temp dir" >&2
		return 0
	fi
	dev_name="${dev##*/}"
	if ! diskutil erasevolume APFS "${volume_name}" "${dev}" >/dev/null 2>&1 || ! usable "${mount_point}"; then
		eject_created_device "${dev}" >/dev/null 2>&1 || true
		echo "warning: could not create an APFS RAM disk; falling back to the default temp dir" >&2
		return 0
	fi
	# Whatever sits at the shared path is never ejected or renamed here: other
	# processes may already have taken it as their TMPDIR. When it cannot be
	# confirmed as this run's disk, this run gets a disk of its own instead.
	if ! identity="$(mounted_identity)"; then
		abandon_attached_disk "${dev}" 0
		if unique="$(use_unique_ram_disk "created RAM disk identity could not be verified")"; then
			printf '%s\n' "${unique}"
			return 0
		fi
		echo "warning: could not create a usable APFS RAM disk; falling back to the default temp dir" >&2
		return 0
	fi
	parent="$(sed -n '3p' <<<"${identity}")"
	if ! parent_has_physical_store "${parent}" "${dev_name}"; then
		# The mount is not on this run's disk, so ejecting that disk cannot take
		# the mount down.
		abandon_attached_disk "${dev}" 1
		if unique="$(use_unique_ram_disk "${mount_point} is not backed by the RAM disk attached in this run")"; then
			printf '%s\n' "${unique}"
			return 0
		fi
		echo "warning: could not create a usable APFS RAM disk; falling back to the default temp dir" >&2
		return 0
	fi
	# The mount is this run's disk, so keep using it even when the record cannot
	# be written; --destroy then refuses it and the warning says how to eject.
	if ! write_owner_record "${identity}" "${dev_name}"; then
		echo "warning: created RAM disk ownership could not be recorded; using ${mount_point} anyway (eject it manually with diskutil eject ${dev})" >&2
	fi
	echo "${mount_point}"
}

main() {
	case "${1:-}" in
		--destroy)
			destroy_owned_ram_disk
			return
			;;
		--release)
			release_private_ram_disk "${2:-}"
			return
			;;
		"") ;;
		*)
			echo "usage: $0 [--destroy | --release DIR]" >&2
			return 2
			;;
	esac

	if [[ -n "${CHORD_TEST_TMPDIR:-}" ]]; then
		if usable "${CHORD_TEST_TMPDIR}"; then
			echo "${CHORD_TEST_TMPDIR}"
			return 0
		fi
		echo "warning: CHORD_TEST_TMPDIR=${CHORD_TEST_TMPDIR} is not a writable directory; ignoring" >&2
	fi

	local repo_root conf configured
	repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
	conf="${repo_root}/.chord/test-tmpdir"
	if [[ -f "${conf}" ]]; then
		configured="$(grep -vE '^[[:space:]]*(#|$)' "${conf}" | head -n1 | tr -d '[:space:]' || true)"
		if [[ -n "${configured}" ]]; then
			if usable "${configured}"; then
				echo "${configured}"
				return 0
			fi
			echo "warning: ${conf} points at ${configured}, which is not a writable directory; ignoring" >&2
		fi
	fi

	case "$(uname -s)" in
		Linux)
			if usable /dev/shm; then
				echo /dev/shm
				return 0
			fi
			if [[ -e /dev/shm ]]; then
				echo "warning: /dev/shm is not usable as a test temp dir (must be writable and allow exec); falling back to the default temp dir" >&2
			fi
			;;
		Darwin)
			resolve_darwin_tmpdir
			;;
	esac
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
	main "$@"
fi
