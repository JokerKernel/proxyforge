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
  [[ "${output}" != *$'\033['* ]] || fail "redirected output contains ANSI controls"
  [[ "${output}" != *"SHA256SUMS"* ]] || fail "downloaded release files for current version"
)

test_color_controls() (
  PROXYFORGE_COLOR=always
  unset NO_COLOR
  source "${script_dir}/install.sh"

  local output
  output=$(info "彩色信息")
  [[ "${output}" == *$'\033[34m[信息]\033[0m'* ]] || fail "blue information color was not applied"
  output=$(print_header)
  [[ "${output}" == *$'\033[1;38;5;208m╭─ ProxyForge\033[0m'* ]] || fail "orange title color was not applied"
  output=$(step "彩色步骤")
  [[ "${output}" == *$'\033[1;38;5;208m[步骤]\033[0m'* ]] || fail "orange step color was not applied"
)

test_no_color_is_respected() (
  unset PROXYFORGE_COLOR
  NO_COLOR=""
  source "${script_dir}/install.sh"

  local output
  output=$(result "纯文本结果")
  [[ "${output}" == "[结果] 纯文本结果" ]] || fail "NO_COLOR output was decorated: ${output}"
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

test_uninstall_removes_only_proxyforge_binary() (
  source "${script_dir}/install.sh"
  require_root() { return; }
  local installed=true
  proxyforge_binary_exists() { [[ "${installed}" == true ]]; }
  remove_proxyforge_binary() { installed=false; }

  local output
  output=$(main uninstall)
  [[ "${output}" == *"自身卸载"* ]] || fail "missing uninstall header"
  [[ "${output}" == *"ProxyForge 自身已卸载"* ]] || fail "missing uninstall result"
  [[ "${output}" == *"sing-box、Xray、节点配置和 ProxyForge 数据均已保留"* ]] || fail "missing retained data notice"
)

test_uninstall_is_idempotent() (
  source "${script_dir}/install.sh"
  require_root() { return; }
  proxyforge_binary_exists() { return 1; }
  remove_proxyforge_binary() { fail "remove called for absent binary"; }

  local output
  output=$(main --uninstall --yes)
  [[ "${output}" == *"当前未安装，无需卸载"* ]] || fail "missing already-uninstalled result"
)

test_uninstall_aliases_are_equivalent() (
  source "${script_dir}/install.sh"
  require_root() { return; }
  proxyforge_binary_exists() { return 1; }

  local positional_output flag_output
  positional_output=$(main uninstall)
  flag_output=$(main --uninstall)
  [[ "${positional_output}" == "${flag_output}" ]] || fail "uninstall aliases produced different output"
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
  download_direct() { return 1; }

  local output
  output=$(main --yes)
  [[ "${output}" == *"[警告] GitHub Releases API 不可用，改用 Release version 文件"* ]] || fail "missing API fallback status"
  [[ "${output}" == *"最新正式版本：v1.2.3（Release version 文件）"* ]] || fail "version fallback failed"
)

test_api_failure_retries_without_proxy() (
  source "${script_dir}/install.sh"
  require_root() { return; }
  detect_current_version() { current_version=v1.2.3; }
  download() {
    [[ "$1" == "${latest_release_api}" ]] || fail "unexpected proxied download: $1"
    return 1
  }
  download_direct() {
    local url=$1 output=$2
    [[ "${url}" == "${latest_release_api}" ]] || fail "unexpected direct download: ${url}"
    printf '{"tag_name":"v1.2.3"}\n' > "${output}"
  }

  local output
  output=$(main)
  [[ "${output}" == *"尝试忽略代理直连"* ]] || fail "missing direct retry notice"
  [[ "${output}" == *"最新正式版本：v1.2.3（GitHub Releases API 直连）"* ]] || fail "direct API retry failed"
  [[ "${output}" != *"改用 Release version 文件"* ]] || fail "version fallback ran after successful direct retry"
)

test_curl_direct_download_bypasses_all_proxies() (
  source "${script_dir}/install.sh"
  local captured=()
  curl() { captured=("$@"); }

  download_direct "${latest_release_api}" /tmp/proxyforge-install-test-api
  local index found=false
  for ((index = 0; index + 1 < ${#captured[@]}; index++)); do
    if [[ "${captured[index]}" == "--noproxy" && "${captured[index + 1]}" == "*" ]]; then
      found=true
      break
    fi
  done
  [[ "${found}" == true ]] || fail "curl direct retry did not use --noproxy '*'"
)

test_automatic_mode_skips_when_already_current
test_color_controls
test_no_color_is_respected
test_version_comparison_and_classification
test_update_argument_is_removed
test_uninstall_removes_only_proxyforge_binary
test_uninstall_is_idempotent
test_uninstall_aliases_are_equivalent
test_api_failure_retries_without_proxy
test_curl_direct_download_bypasses_all_proxies
test_version_file_fallback
printf 'install_test: ok\n'
