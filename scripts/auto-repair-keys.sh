#!/usr/bin/env bash
#
# auto-repair-keys.sh - Fix structural defects in codex-api-key entries.
#
# What it does (idempotent, safe to re-run):
#   1. Adds missing `excluded-models: ['*']` to any entry that lacks it.
#      Without it, the runtime treats the key as having zero models and
#      silently skips it — this is the most common "key exists but does
#      nothing" bug seen after hand-edits.
#   2. Re-normalizes indentation of any `- api-key:` line whose leading
#      whitespace is not exactly 2 spaces.
#   3. Reports (but does not touch) entries flagged by detect-key-health.sh
#      as payment-required / auth-fail / unreachable — those need human
#      attention (recharge, revoke, network fix), not auto-edit.
#
# Usage: ./scripts/auto-repair-keys.sh [path/to/config.yaml] [--apply]
#        Without --apply the script prints the diff and exits.
#
# Exit code:
#   0  nothing to repair (or diff preview shown)
#   1  repairs applied (when --apply is set)
#   2  unrecoverable error

set -euo pipefail

CONFIG="${1:-./config.yaml}"
APPLY="false"
[[ "${2:-}" == "--apply" ]] && APPLY="true"

if [[ ! -f "${CONFIG}" ]]; then
  echo "ERROR: config not found: ${CONFIG}" >&2
  exit 2
fi

ORIG=$(cat "$CONFIG")

# Run Python: diagnostics go to stderr, repaired text on stdout.
NEW=$(python3 - "$CONFIG" <<'PY' 2> /tmp/auto-repair.diag
import sys, re

path = sys.argv[1]
with open(path) as f:
    text = f.read()

lines = text.split('\n')
out = []
fixes = []
i = 0
n = len(lines)
in_codex = False
expected_indent = 2

# IMPORTANT: every diagnostic must go to stderr; stdout is reserved for
# the repaired file body that the bash wrapper will write back.

while i < n:
    line = lines[i]
    stripped = line.lstrip(' ')

    if line.startswith('codex-api-key:'):
        in_codex = True
        out.append(line); i += 1; continue
    if in_codex and line and not line.startswith(' ') and not line.startswith('-'):
        in_codex = False

    if in_codex and stripped.startswith('- api-key:'):
        leading = len(line) - len(stripped)
        if leading != expected_indent:
            fixes.append(f"indent-fix: line {i+1} had {leading} spaces, set to {expected_indent}")
            line = (' ' * expected_indent) + stripped

        out.append(line)
        i += 1
        entry_keys = {}
        while i < n:
            cur = lines[i]
            cur_stripped = cur.lstrip(' ')
            if cur_stripped.startswith('- api-key:'):
                break
            if cur and not cur.startswith(' '):
                break
            m = re.match(r'^( +)([a-zA-Z][\w-]*):', cur)
            if m:
                key = m.group(2)
                entry_keys[key] = True
            out.append(cur)
            i += 1

        if 'excluded-models' not in entry_keys:
            sib_indent = '    '
            content_indent = '      '
            out.append(f"{sib_indent}excluded-models:")
            out.append(f"{content_indent}- '*'")
            fixes.append(
                f"missing excluded-models: appended to entry near line {i+1}"
            )
        continue

    out.append(line)
    i += 1

new_text = '\n'.join(out)

# YAML re-validate (stderr only)
try:
    import yaml
    yaml.safe_load(new_text)
    ok = True
except Exception as e:
    ok = False
    print(f"WARN: produced YAML does not parse: {e}", file=sys.stderr)

print(f"REPAIRS={len(fixes)}", file=sys.stderr)
for f in fixes:
    print(f"  - {f}", file=sys.stderr)
print(f"YAML_OK={'yes' if ok else 'no'}", file=sys.stderr)

sys.stdout.write(new_text)
PY
)
DIAG=$(cat /tmp/auto-repair.diag 2>/dev/null || true)
rm -f /tmp/auto-repair.diag

# Echo diagnostics to stderr for the user
[[ -n "$DIAG" ]] && echo "$DIAG" >&2

if [[ "$ORIG" == "$NEW" ]]; then
  echo "No repairs needed." >&2
  exit 0
fi

if [[ "$APPLY" != "true" ]]; then
  echo "The following repairs would be made (re-run with --apply to commit):" >&2
  diff -u <(printf '%s' "$ORIG") <(printf '%s' "$NEW") | sed 's/^/  /' >&2 || true
  exit 0
fi

printf '%s' "$NEW" > "$CONFIG"
echo "Applied repairs to $CONFIG" >&2
exit 1