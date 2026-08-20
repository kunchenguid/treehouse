#!/usr/bin/env bash
#
# Decides whether a pull request satisfies the "PR must be raised via no-mistakes"
# required check.
#
# A PR passes when any of these hold:
#   * its body carries the no-mistakes pipeline signature, or
#   * it was opened by github-actions[bot] or dependabot[bot], or
#   * it is structurally a release-please release PR (see below).
#
# The release-please exemption is deliberately STRUCTURAL, never author identity.
# Today release-please opens treehouse's release PRs as github-actions[bot], so
# the bot exemption already covers them, but the structural test keeps them
# covered if release-please is ever switched to a PAT and starts arriving as the
# human `kunchenguid`, who also opens ordinary human PRs. Exempting that login
# would exempt every human PR too.
#
# Every exemption lives here rather than in a job-level `if:` so the whole gate
# has one executable surface that tests can drive directly.
#
# Inputs (environment):
#   PR_BODY, PR_AUTHOR, PR_NUMBER, PR_HEAD_REF, PR_HEAD_REPO, PR_BASE_REPO
# Exit status: 0 = pass, 1 = fail.
set -eu

NO_MISTAKES_MARKER='Updates from [git push no-mistakes](https://github.com/kunchenguid/no-mistakes)'
RELEASE_PLEASE_MARKER='This PR was generated with [Release Please]'
RELEASE_PLEASE_BRANCH_PREFIX='release-please--'
RELEASE_PLEASE_LEGACY_BRANCH_PREFIX='release-please/'

pr_body="${PR_BODY:-}"
pr_author="${PR_AUTHOR:-unknown}"
pr_number="${PR_NUMBER:-unknown}"
pr_head_ref="${PR_HEAD_REF:-}"
pr_head_repo="${PR_HEAD_REPO:-}"
pr_base_repo="${PR_BASE_REPO:-}"

body_contains() {
    printf '%s' "$pr_body" | grep -qF -- "$1"
}

is_exempt_bot() {
    case "$pr_author" in
        'github-actions[bot]' | 'dependabot[bot]') return 0 ;;
        *) return 1 ;;
    esac
}

# Condition 1: a branch under release-please's reserved branch prefix. The
# legacy `release-please/` prefix is accepted for older release-please setups.
is_release_please_branch() {
    case "$pr_head_ref" in
        "$RELEASE_PLEASE_BRANCH_PREFIX"* | "$RELEASE_PLEASE_LEGACY_BRANCH_PREFIX"*) return 0 ;;
        *) return 1 ;;
    esac
}

# Condition 2: same-repo head. A fork can copy the branch name and the body but
# cannot make its head repository be this repository.
is_same_repo_head() {
    [ -n "$pr_head_repo" ] && [ -n "$pr_base_repo" ] && [ "$pr_head_repo" = "$pr_base_repo" ]
}

# Condition 3: release-please's generated body footer.
has_release_please_footer() {
    body_contains "$RELEASE_PLEASE_MARKER"
}

if body_contains "$NO_MISTAKES_MARKER"; then
    echo "Found no-mistakes signature in PR #${pr_number} body."
    exit 0
fi

if is_exempt_bot; then
    echo "PR #${pr_number} was opened by ${pr_author}; exempt from the no-mistakes signature."
    exit 0
fi

if is_release_please_branch && is_same_repo_head && has_release_please_footer; then
    echo "PR #${pr_number} is a release-please release PR (same-repo branch '${pr_head_ref}' with the Release Please footer); exempt from the no-mistakes signature."
    exit 0
fi

{
    echo "::error::This PR was not raised through no-mistakes."
    echo
    echo "Contributions to this repository must be submitted via 'git push no-mistakes'."
    echo "That pipeline runs the required review/test/lint/CI steps and writes a"
    echo "deterministic '## Pipeline' section into the PR body containing:"
    echo
    echo "    $NO_MISTAKES_MARKER"
    echo
    echo "The only other way to pass is release-please's own release PR, which must"
    echo "satisfy all three structural conditions: a '${RELEASE_PLEASE_BRANCH_PREFIX}'"
    echo "(or legacy '${RELEASE_PLEASE_LEGACY_BRANCH_PREFIX}') head branch, a"
    echo "same-repository (non-fork) head, and the Release Please body footer."
    echo
    echo "PR author: ${pr_author}"
    echo "Head branch: ${pr_head_ref:-unknown}"
    echo "Head repository: ${pr_head_repo:-unknown} (base ${pr_base_repo:-unknown})"
} >&2
exit 1
