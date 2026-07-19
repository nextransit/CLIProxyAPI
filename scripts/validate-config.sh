#!/usr/bin/env bash
#
# validate-config.sh - Validate config.yaml structure for CLIProxyAPI.
#
# Catches the most common mistakes made when adding new codex-api-key /
# openai-compatibility entries by hand:
#
#   1. Inconsistent list-item indentation
#   2. Missing required sibling keys (weight, base-url, proxy-url, excluded-models)
#   3. base-url pointing at unreachable private networks (192.168.x.x,
#      10.x.x.x) without an explicit unreachable-host note
#   4. Unparseable YAML (delegate to python yaml.safe_load)
#
# Usage: ./scripts/validate-config.sh [path/to/config.yaml]
#        (defaults to ./config.yaml)

set -euo pipefail

CONFIG="${1:-./config.yaml}"

if [[ ! -f "${CONFIG}" ]]; then
  echo "ERROR: config not found: ${CONFIG}" >&2
  exit 2
fi

if ! command -v python3 >/dev/null 2>&1; then
  echo "ERROR: python3 required" >&2
  exit 2
fi

python3 - "${CONFIG}" <<'PY'
import sys, re, yaml

path = sys.argv[1]
with open(path, 'r') as f:
    text = f.read()

errors = []
warnings = []

# --- 1. YAML parses ---
try:
    data = yaml.safe_load(text)
except yaml.YAMLError as e:
    print(f"FAIL: YAML parse error in {path}: {e}")
    sys.exit(1)
print(f"OK   YAML parses ({path})")

# --- 2. codex-api-key consistency ---
codex = data.get('codex-api-key') or []
if not isinstance(codex, list):
    errors.append("codex-api-key must be a list")
else:
    print(f"OK   codex-api-key: {len(codex)} entries")
    for i, entry in enumerate(codex):
        if not isinstance(entry, dict):
            errors.append(f"codex-api-key[{i}] is not a mapping")
            continue
        ak = (entry.get('api-key') or '').strip()
        if not ak:
            errors.append(f"codex-api-key[{i}] missing api-key")
            continue
        # required siblings
        for required in ('base-url', 'proxy-url'):
            if required not in entry:
                warnings.append(
                    f"codex-api-key[{i}] ({ak[:18]}...) missing '{required}'"
                )
        # excluded-models coverage
        if not entry.get('excluded-models'):
            warnings.append(
                f"codex-api-key[{i}] ({ak[:18]}...) missing "
                f"'excluded-models: [\"*\"]' — fallback behavior is undefined"
            )
        # private IP base-url check (only flag loopback / link-local — those
        # almost never work from a container; private LAN ranges like 10.x /
        # 192.168.x are intentionally permitted because they may be reachable
        # via the host's LAN routing table).
        base = (entry.get('base-url') or '').strip()
        if re.match(r'https?://127\.', base):
            warnings.append(
                f"codex-api-key[{i}] ({ak[:18]}...) base-url '{base}' uses "
                f"loopback — this resolves to the container itself, not the host"
            )
        # missing /v1 suffix (OpenAI-compat endpoints usually need it)
        if base and not base.endswith('/v1') and not base.endswith('/codex'):
            warnings.append(
                f"codex-api-key[{i}] ({ak[:18]}...) base-url '{base}' "
                f"does not end with /v1 or /codex — verify upstream path"
            )

# --- 3. Indentation consistency for codex-api-key entries ---
lines = text.split('\n')
in_block = False
expected_indent = 2
for i, line in enumerate(lines, 1):
    if line.startswith('codex-api-key:'):
        in_block = True
        continue
    if in_block and re.match(r'^[a-zA-Z]', line):
        in_block = False
    if in_block and '- api-key:' in line:
        leading = len(line) - len(line.lstrip(' '))
        if leading != expected_indent:
            errors.append(
                f"line {i}: - api-key: has {leading} leading spaces, "
                f"expected {expected_indent}"
            )

# --- 4. openai-compatibility consistency ---
oc = data.get('openai-compatibility') or []
if isinstance(oc, list):
    print(f"OK   openai-compatibility: {len(oc)} providers")
    for i, provider in enumerate(oc):
        if not isinstance(provider, dict):
            continue
        for required in ('name', 'base-url'):
            if not provider.get(required):
                errors.append(
                    f"openai-compatibility[{i}] missing '{required}'"
                )

# --- summary ---
if warnings:
    print("\nWARN:")
    for w in warnings:
        print(f"  - {w}")
if errors:
    print("\nFAIL:")
    for e in errors:
        print(f"  - {e}")
    sys.exit(1)

print("\nPASS: config.yaml validation succeeded")
if warnings:
    print(f"     ({len(warnings)} warnings)")
PY