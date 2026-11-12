#!/usr/bin/env bash
set -euo pipefail

# Exercises scripts/check_deadcode.sh against canned findings and temporary
# baselines, so it needs neither the deadcode analyzer nor a writable checkout.

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
check="${repo_root}/scripts/check_deadcode.sh"
source_baseline="${repo_root}/scripts/deadcode-baseline/linux-amd64.txt"
marker="# --- unclassified: needs classification and reason ---"
new_symbol="github.com/keakon/chord/internal/example.unusedHelper"

test_root="$(mktemp -d "${TMPDIR:-/tmp}/chord-deadcode-check.XXXXXX")"
cleanup() {
  rm -rf "${test_root}"
}
trap cleanup EXIT

baseline="${test_root}/baseline.txt"
findings="${test_root}/findings.json"
out="${test_root}/out.txt"

fail() {
  printf 'test_check_deadcode: %s\n' "$1" >&2
  if [[ -f "${out}" ]]; then
    printf -- '--- output ---\n' >&2
    cat "${out}" >&2
  fi
  exit 1
}

# symbols_of FILE prints the symbol lines of a baseline.
symbols_of() {
  grep -v -e '^#' -e '^[[:space:]]*$' "$1" || true
}

# write_findings SYMBOL... writes deadcode -json output reporting those symbols.
write_findings() {
  python3 - "${findings}" "$@" <<'PY'
import json
import sys

path, symbols = sys.argv[1], sys.argv[2:]
packages = {}
for line, symbol in enumerate(symbols, start=1):
    slash = symbol.rfind("/")
    dot = symbol.index(".", slash + 1)
    pkg, name = symbol[:dot], symbol[dot + 1:]
    packages.setdefault(pkg, []).append(
        {"Name": name, "Position": {"File": "/src/example.go", "Line": line, "Col": 1}})
with open(path, "w", encoding="utf-8") as f:
    json.dump([{"Path": p, "Funcs": fns} for p, fns in packages.items()], f)
PY
}

run_update() {
  CHORD_DEADCODE_UPDATE=1 CHORD_DEADCODE_BASELINE="${baseline}" CHORD_DEADCODE_FINDINGS="${findings}" \
    GOOS=linux GOARCH=amd64 bash "${check}" >"${out}" 2>&1
}

run_check() {
  CHORD_DEADCODE_BASELINE="${baseline}" CHORD_DEADCODE_FINDINGS="${findings}" \
    GOOS=linux GOARCH=amd64 bash "${check}" >"${out}" 2>&1
}

all_symbols=()
while IFS= read -r symbol; do
  all_symbols+=("${symbol}")
done < <(symbols_of "${source_baseline}")
[[ "${#all_symbols[@]}" -ge 3 ]] || fail "expected at least three symbols in ${source_baseline}"

# Unchanged symbol set: the update keeps the file byte-identical and the check passes.
cp "${source_baseline}" "${baseline}"
write_findings "${all_symbols[@]}"
run_update || fail "update with an unchanged symbol set failed"
cmp -s "${source_baseline}" "${baseline}" || fail "update with an unchanged symbol set rewrote the baseline"
[[ "$(cat "${out}")" == *"(+0 / -0)"* ]] || fail "unchanged update did not report +0 / -0"
run_check || fail "check with an unchanged symbol set failed"

# One symbol gone: the update removes exactly that line.
dropped="${all_symbols[1]}"
remaining=()
for symbol in "${all_symbols[@]}"; do
  [[ "${symbol}" == "${dropped}" ]] || remaining+=("${symbol}")
done
write_findings "${remaining[@]}"
run_check || fail "check with a stale entry failed"
[[ "$(cat "${out}")" == *"no longer reported"* ]] || fail "check did not note the stale entry"
run_update || fail "update after removing one symbol failed"
[[ "$(cat "${out}")" == *"(+0 / -1)"* ]] || fail "removal update did not report +0 / -1"
diff_out="$(diff "${source_baseline}" "${baseline}" || true)"
[[ "$(printf '%s\n' "${diff_out}" | grep -c '^[<>]')" == 1 ]] || fail "removal changed more than one line: ${diff_out}"
[[ "${diff_out}" == *"< ${dropped}"* ]] || fail "removal did not drop ${dropped}: ${diff_out}"

# A group whose symbols all disappear stays in place and triggers a warning.
cat >"${baseline}" <<EOF
# header
# alpha: test-support; first group.
github.com/keakon/chord/internal/alpha.One
github.com/keakon/chord/internal/alpha.Two
# beta: test-support; second group.
github.com/keakon/chord/internal/beta.One
EOF
cp "${baseline}" "${test_root}/groups.txt"
write_findings "github.com/keakon/chord/internal/beta.One"
run_update || fail "update that empties a group failed"
[[ "$(cat "${out}")" == *"warning: group comment left without symbols"*"# alpha: test-support; first group."* ]] ||
  fail "emptied group was not reported"
[[ "$(cat "${out}")" != *"# beta:"* ]] || fail "non-empty group was reported as empty"
grep -qxF "# alpha: test-support; first group." "${baseline}" || fail "emptied group comment was deleted"

# A new symbol lands under the unclassified marker and the check fails until it is classified.
cp "${source_baseline}" "${baseline}"
write_findings "${all_symbols[@]}" "${new_symbol}"
run_check && fail "check passed with an unlisted finding"
[[ "$(cat "${out}")" == *"missing from"*"${new_symbol}"* ]] || fail "check did not name the unlisted finding"
run_update || fail "update with a new symbol failed"
[[ "$(cat "${out}")" == *"(+1 / -0)"* ]] || fail "addition update did not report +1 / -0"
[[ "$(cat "${out}")" == *"classified group"* ]] || fail "addition update did not ask for a classification"
[[ "$(tail -n 2 "${baseline}")" == "${marker}"$'\n'"${new_symbol}" ]] ||
  fail "new symbol is not under the unclassified marker: $(tail -n 2 "${baseline}")"
source_lines="$(wc -l <"${source_baseline}")"
[[ "$(head -n "$((source_lines))" "${baseline}")" == "$(cat "${source_baseline}")" ]] ||
  fail "addition update changed existing lines"
run_check && fail "check passed with an unclassified entry"
[[ "$(cat "${out}")" == *"not classified yet"*"${new_symbol}"* ]] || fail "check did not name the unclassified entry"

# Another new symbol joins the existing unclassified entries without reordering them.
second_symbol="github.com/keakon/chord/internal/example.anotherHelper"
write_findings "${all_symbols[@]}" "${new_symbol}" "${second_symbol}"
run_update || fail "second addition update failed"
[[ "$(tail -n 3 "${baseline}")" == "${marker}"$'\n'"${new_symbol}"$'\n'"${second_symbol}" ]] ||
  fail "second addition did not keep the existing unclassified entry: $(tail -n 3 "${baseline}")"

# Moving the entries into a classified group makes the check pass, and a later update keeps the file.
{
  grep -vxF -e "${new_symbol}" -e "${second_symbol}" "${baseline}" | grep -vxF "${marker}"
  printf '# example helpers: test-support; sample reason.\n%s\n%s\n%s\n' \
    "${new_symbol}" "${second_symbol}" "${marker}"
} >"${test_root}/classified.txt"
mv "${test_root}/classified.txt" "${baseline}"
cp "${baseline}" "${test_root}/classified-copy.txt"
run_check || fail "check failed after the entries were classified"
run_update || fail "update after classification failed"
cmp -s "${test_root}/classified-copy.txt" "${baseline}" || fail "update rewrote a classified baseline"

# Without a baseline, the update creates one with every symbol unclassified.
rm -f "${baseline}"
write_findings "${all_symbols[@]}"
run_check && fail "check passed without a baseline"
run_update || fail "update without a baseline failed"
[[ "$(head -n 1 "${baseline}")" == "# Unreachable symbols reported by deadcode for linux/amd64." ]] ||
  fail "created baseline is missing the header"
[[ "$(sed -n 5p "${baseline}")" == "${marker}" ]] || fail "created baseline is missing the unclassified marker"
[[ "$(symbols_of "${baseline}")" == "$(printf '%s\n' "${all_symbols[@]}" | LC_ALL=C sort)" ]] ||
  fail "created baseline does not list every symbol"
run_check && fail "check passed with a freshly created, unclassified baseline"

echo "test_check_deadcode: ok"
