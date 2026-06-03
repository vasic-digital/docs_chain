#!/bin/bash
#
# install_upstreams.sh — Configure git remotes for all upstream repositories.
#
# Reads UPSTREAMABLE_REPOSITORY from each .sh file in the Upstreams/
# directory and adds them as git remotes. Existing remotes are updated.
#
# Usage: ./install_upstreams.sh [--push] [--dry-run]
#
# Options:
#   --push      Push current branch to all upstreams after configuration
#   --dry-run   Show what would be done without making changes
#
# Inheritance: §11.4.36 (CONST-056), §6.W (GitHub + GitLab only).
# Classification: project-specific (vasic-digital + HelixDevelopment infra).

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
UPSTREAMS_DIR="${SCRIPT_DIR}/Upstreams"

if [[ ! -d "${UPSTREAMS_DIR}" ]]; then
    echo "Error: Upstreams directory not found at ${UPSTREAMS_DIR}" >&2
    echo "Create Upstreams/ with GitHub.sh and GitLab.sh recipe files." >&2
    exit 1
fi

PUSH=false
DRY_RUN=false

while [[ $# -gt 0 ]]; do
    case $1 in
        --push)    PUSH=true;     shift ;;
        --dry-run) DRY_RUN=true;  shift ;;
        *) echo "Unknown option: $1" >&2; echo "Usage: $0 [--push] [--dry-run]" >&2; exit 1 ;;
    esac
done

echo "=== Configuring upstream git remotes ==="
echo "Upstreams directory: ${UPSTREAMS_DIR}"
echo

for script in "${UPSTREAMS_DIR}"/*.sh; do
    [[ -f "$script" ]] || continue
    upstream_name="$(basename "$script" .sh)"
    repo_url="$(bash -c "source \"$script\" && echo \"\$UPSTREAMABLE_REPOSITORY\"")"
    if [[ -z "$repo_url" ]]; then
        echo "Warning: $script does not export UPSTREAMABLE_REPOSITORY" >&2
        continue
    fi
    echo "Processing $upstream_name..."
    echo "  Repository URL: $repo_url"
    if [[ "$DRY_RUN" == "true" ]]; then
        echo "  [DRY RUN] Would configure remote: $upstream_name -> $repo_url"
        continue
    fi
    if git remote add "$upstream_name" "$repo_url" 2>/dev/null; then
        echo "  Added remote: $upstream_name"
    else
        git remote set-url "$upstream_name" "$repo_url"
        echo "  Updated remote: $upstream_name"
    fi
done

echo
echo "=== Current git remotes ==="
git remote -v

if [[ "$PUSH" == "true" ]]; then
    echo
    echo "=== Pushing to all upstreams ==="
    current_branch="$(git branch --show-current)"
    [[ -z "$current_branch" ]] && { echo "Error: Not on a branch" >&2; exit 1; }
    for remote in $(git remote); do
        echo "Pushing to $remote/$current_branch..."
        if [[ "$DRY_RUN" == "true" ]]; then
            echo "  [DRY RUN] Would run: git push $remote $current_branch"
        else
            git push "$remote" "$current_branch" || echo "  Warning: push to $remote failed"
        fi
    done
fi

echo
echo "=== Done ==="
