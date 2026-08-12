#!/usr/bin/env bash
set -euo pipefail

readonly script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)

fail() {
  printf 'install_test: %s\n' "$*" >&2
  exit 1
}

test_automatic_mode_skips_when_already_current() (
  source "${script_dir}/install.sh"
  require_root() { return; }
  detect_current_version() { current_version=v1.2.3; }
  download() {
    local url=$1 output=$2
    [[ "${url}" == "${latest_release_api}" ]] || fail "unexpected download: ${url}"
    printf '{"tag_name":"v1.2.3"}\n' > "${output}"
  }

  local output
  output=$(main)
  [[ "${output}" == *"╭─ ProxyForge"* ]] || fail "missing installer header"
  [[ "${output}" == *"当前版本：v1.2.3"* ]] || fail "missing current version summary"
  [[ "${output}" == *"目标版本：v1.2.3"* ]] || fail "missing target version summary"
  [[ "${output}" == *"[结果] 当前已是最新正式版本，无需更新"* ]] || fail "missing up-to-date result"
  [[ "${output}" != *"SHA256SUMS"* ]] || fail "downloaded release files for current version"
)

test_version_comparison_and_classification() (
  source "${script_dir}/install.sh"

  compare_versions v1.2.2 v1.2.3
  [[ ${version_order} -eq -1 ]] || fail "older patch version was not lower"
  compare_versions v1.2.3-dirty v1.2.3
  [[ ${version_order} -eq -1 ]] || fail "dirty build was not lower than release"
  compare_versions v2.0.0 v1.9.9
  [[ ${version_order} -eq 1 ]] || fail "newer major version was not higher"

  detect_current_version() { current_version=v1.2.2; }
  resolved_version=v1.2.3
  classify_installation
  [[ "${installation_action}" == "upgrade" ]] || fail "older installation was not classified as upgrade"
)

test_update_argument_is_removed() (
  source "${script_dir}/install.sh"
  require_root() { return; }

  local output status
  set +e
  output=$(main --update 2>&1)
  status=$?
  set -e
  [[ ${status} -ne 0 ]] || fail "removed --update argument was still accepted"
  [[ "${output}" == *"未知参数：--update"* ]] || fail "missing removed argument error"
)

test_version_file_fallback() (
  source "${script_dir}/install.sh"
  require_root() { return; }
  detect_current_version() { current_version=v1.2.3; }
  download() {
    local url=$1 output=$2
    case "${url}" in
      "${latest_release_api}")
        printf '{"tag_name":"latest"}\n' > "${output}"
        ;;
      */version)
        printf 'v1.2.3\n' > "${output}"
        ;;
      *)
        fail "unexpected fallback download: ${url}"
        ;;
    esac
  }

  local output
  output=$(main --yes)
  [[ "${output}" == *"[警告] GitHub Releases API 不可用，改用 Release version 文件"* ]] || fail "missing API fallback status"
  [[ "${output}" == *"最新正式版本：v1.2.3（Release version 文件）"* ]] || fail "version fallback failed"
)

test_automatic_mode_skips_when_already_current
test_version_comparison_and_classification
test_update_argument_is_removed
test_version_file_fallback
printf 'install_test: ok\n'
