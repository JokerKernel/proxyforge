#!/usr/bin/env bash
set -euo pipefail

readonly script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)

fail() {
  printf 'install_test: %s\n' "$*" >&2
  exit 1
}

test_update_skips_when_already_current() (
  source "${script_dir}/install.sh"
  require_root() { return; }
  detect_current_version() { current_version=v1.2.3; }
  download() {
    local url=$1 output=$2
    [[ "${url}" == "${latest_release_api}" ]] || fail "unexpected download: ${url}"
    printf '{"tag_name":"v1.2.3"}\n' > "${output}"
  }

  local output
  output=$(main --update --yes)
  [[ "${output}" == *"当前版本 v1.2.3 已是最新正式版本"* ]] || fail "missing up-to-date result"
  [[ "${output}" != *"SHA256SUMS"* ]] || fail "downloaded release files for current version"
)

test_update_requires_confirmation() (
  source "${script_dir}/install.sh"
  require_root() { return; }
  detect_current_version() { current_version=v1.2.3; }
  download() {
    local url=$1 output=$2
    [[ "${url}" == "${latest_release_api}" ]] || fail "unexpected download before confirmation: ${url}"
    printf '{"tag_name":"v1.2.4"}\n' > "${output}"
  }

  local output status
  set +e
  output=$(main --update </dev/null 2>&1)
  status=$?
  set -e
  [[ ${status} -ne 0 ]] || fail "update continued without confirmation"
  [[ "${output}" == *"自动化时请显式提供 --yes"* ]] || fail "missing confirmation error"
)

test_update_forwards_yes_to_confirmation() (
  source "${script_dir}/install.sh"
  resolved_version=v1.2.4
  current_version=v1.2.3
  assume_yes=true
  confirm_update </dev/null
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
  output=$(main --update --yes)
  [[ "${output}" == *"API 不可用，尝试读取 Release version 文件"* ]] || fail "missing API fallback status"
  [[ "${output}" == *"通过 version 文件获取最新正式版本：v1.2.3"* ]] || fail "version fallback failed"
)

test_update_skips_when_already_current
test_update_requires_confirmation
test_update_forwards_yes_to_confirmation
test_version_file_fallback
printf 'install_test: ok\n'
