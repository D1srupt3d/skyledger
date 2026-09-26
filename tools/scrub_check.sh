#!/usr/bin/env bash
# Publishing gate: fail if the working tree contains things that must never be public.
# Generic checks run everywhere (CI too). Personal patterns (your hostnames, email,
# coordinates) go in .scrub-patterns, one extended regex per line: that file is
# gitignored, because a committed list of secrets to hide would publish them.
set -euo pipefail
cd "$(dirname "$0")/.."

fail=0
files() { git ls-files --cached --others --exclude-standard | grep -v -e '^uv.lock$' -e '^tools/scrub_check.sh$'; }
check() {
    local label=$1 pattern=$2 hits
    hits=$(files | xargs -d '\n' grep -nIE -e "$pattern" -- 2>/dev/null || true)
    if [[ -n $hits ]]; then
        echo "scrub: $label"
        echo "  ${hits//$'\n'/$'\n'  }"
        fail=1
    fi
}

check "private key" '-----BEGIN [A-Z ]*PRIVATE KEY-----'
check "filled-in password in .env.example" '^(POSTGRES|GRAFANA_DB|GRAFANA_ADMIN)_PASSWORD=.+'
if files | grep -qx '.env'; then
    echo "scrub: .env is tracked"
    fail=1
fi

if [[ -f .scrub-patterns ]]; then
    while IFS= read -r pattern; do
        [[ -z $pattern || $pattern == \#* ]] && continue
        check "personal pattern (.scrub-patterns)" "$pattern"
    done < .scrub-patterns
else
    echo "scrub: no .scrub-patterns file; generic checks only"
fi

if (( fail )); then
    exit 1
fi
echo "scrub: clean"
