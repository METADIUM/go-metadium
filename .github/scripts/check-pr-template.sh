#!/usr/bin/env bash
#
# Check a pull request body against the required fields in
# .github/PULL_REQUEST_TEMPLATE.md (issue #113).
#
# The template makes the compatibility tier and the rollout gate required, and
# nothing enforced that: CI never read the PR body, so a PR that omitted either
# merged on reviewer diligence alone.
#
# What this checks is only whether the fields are *there*. Whether the ticked
# tier is the right one, and whether the rollout plan is the right plan, stays a
# reviewer's judgement -- no script can do that part.
#
#   usage: check-pr-template.sh <file containing the PR body>
#
# Run it on a body you are about to submit:
#
#   gh pr view <n> --json body --jq .body > /tmp/body.md
#   .github/scripts/check-pr-template.sh /tmp/body.md

set -uo pipefail

if [[ $# -ne 1 ]]; then
	echo "usage: $0 <pr-body-file>" >&2
	exit 2
fi

body_file=$1
if [[ ! -r $body_file ]]; then
	echo "$0: cannot read $body_file" >&2
	exit 2
fi

# Strip HTML comments before anything else: the template's own prompts live in
# them, and a body that only kept those prompts has said nothing.
body=$(perl -0777 -pe 's/<!--.*?-->//gs' "$body_file")

fail=0
problem() {
	fail=1
	echo "FAIL: $1"
}

# --- compatibility tier -----------------------------------------------------
#
# Exactly one "- [x] **C<n>" must be ticked. Zero means the field was skipped;
# more than one means the PR has not decided what a node on the current release
# sees, which is the whole point of picking a tier.

ticked_tiers=$(printf '%s\n' "$body" |
	grep -oiE '^[[:space:]]*-[[:space:]]*\[x\][[:space:]]*\*\*C[0-7]' |
	grep -oE 'C[0-7]')
tier_count=$(printf '%s' "$ticked_tiers" | grep -c . || true)

tier=""
case $tier_count in
0)
	problem "no compatibility tier is ticked. Tick exactly one '- [x] **C<n>' box under '## Compatibility tier'."
	;;
1)
	tier=$ticked_tiers
	echo "ok: compatibility tier $tier"
	;;
*)
	problem "more than one compatibility tier is ticked ($(printf '%s' "$ticked_tiers" | tr '\n' ' ')). Pick exactly one."
	;;
esac

# --- rollout ----------------------------------------------------------------
#
# The section is extracted up to the next level-2 heading. Lines that markdown
# folds into the item above them (the template wraps every checkbox over several
# indented lines) are joined back onto it, so "prose" below means a real
# paragraph rather than the tail of an untouched template checkbox.

rollout=$(printf '%s\n' "$body" | awk '
	/^##[[:space:]]+Rollout([[:space:]]|$)/ { inside = 1; next }
	/^##[[:space:]]/                        { inside = 0 }
	inside                                  { print }
')

if ! printf '%s\n' "$body" | grep -qiE '^##[[:space:]]+Rollout([[:space:]]|$)'; then
	problem "no '## Rollout' section. Say how this reaches the fleet, or that nothing deploys."
else
	unwrapped=$(printf '%s\n' "$rollout" | awk '
		/^[[:space:]]+[^[:space:]]/ && NR > 1 && buf != "" { sub(/^[[:space:]]+/, " "); buf = buf $0; next }
		{ if (buf != "") print buf; buf = $0 }
		END { if (buf != "") print buf }
	')

	has_ticked=$(printf '%s\n' "$unwrapped" | grep -ciE '^[[:space:]]*-[[:space:]]*\[x\]' || true)
	has_prose=$(printf '%s\n' "$unwrapped" |
		grep -vE '^[[:space:]]*-[[:space:]]*\[[[:space:]xX]\]' |
		grep -c '[^[:space:]]' || true)

	if [[ $has_ticked -eq 0 && $has_prose -eq 0 ]]; then
		problem "'## Rollout' is empty -- it carries only the template's unticked boxes. Tick what applies or write what happens."
	else
		echo "ok: rollout stated"

		# C0-C3 are the tiers a node on the current release can feel, so the
		# template requires the mixed-fleet gate for them: testnet BPs first,
		# rest of the testnet left on the old build. Prose satisfies this --
		# #136 said it in a sentence rather than a checkbox -- so the test is
		# that the gate is named at all, not how.
		case $tier in
		C0 | C1 | C2 | C3)
			if printf '%s' "$unwrapped" | grep -qi 'testnet' &&
				printf '%s' "$unwrapped" | grep -qiE '\bBPs?\b|block producers?'; then
				echo "ok: $tier rollout names the testnet-BP-first gate"
			else
				problem "tier $tier requires the testnet-BP-first gate in '## Rollout' (testnet block producers upgrade first, the rest of the testnet stays on the old build). It is not mentioned."
			fi
			;;
		esac
	fi
fi

if [[ $fail -ne 0 ]]; then
	echo
	echo "See .github/PULL_REQUEST_TEMPLATE.md. This check reads the PR body only;"
	echo "edit the description and it runs again."
	exit 1
fi

echo
echo "PR template fields present."
