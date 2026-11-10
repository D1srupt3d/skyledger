#!/usr/bin/env bash
# Per-package test coverage, counting every test that exercises a package (the integration
# tests cover most of the database code). Fails if any package is below MIN percent.
# Needs SKYLEDGER_TEST_DSN for the integration tests; without it those packages fall short.
set -euo pipefail
cd "$(dirname "$0")/.."
MIN=${MIN:-80}
profile=$(mktemp)
trap 'rm -f "$profile"' EXIT

go test -count=1 -coverpkg=./... -coverprofile="$profile" ./...

# Each test binary reports every block; a block is covered if any binary ran it.
awk -v min="$MIN" '
    NR == 1 { next }
    {
        split($1, loc, ":"); file = loc[1]
        stmts[$1] = $2
        if ($3 > 0) hit[$1] = 1
        pkg = file; sub(/\/[^\/]*$/, "", pkg); owner[$1] = pkg
    }
    END {
        for (b in stmts) { total[owner[b]] += stmts[b]; if (b in hit) covered[owner[b]] += stmts[b] }
        fail = 0
        for (p in total) {
            pct = 100 * covered[p] / total[p]
            printf "%-60s %5.1f%%\n", p, pct
            if (pct < min) fail = 1
        }
        if (fail) { printf "below %d%% coverage\n", min; exit 1 }
    }' "$profile" | sort
