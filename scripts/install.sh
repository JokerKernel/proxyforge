#!/usr/bin/env bash
set -euo pipefail

readonly repository="JokerKernel/proxyforge"
readonly install_dir="/usr/local/sbin"
readonly install_path="${install_dir}/proxyforge"
readonly max_version_size=256
readonly max_release_api_size=$((1024 * 1024))
readonly latest_release_api="https://api.github.com/repos/${repository}/releases/latest"

color_enabled=false
proxyforge_color_mode=${PROXYFORGE_COLOR:-auto}
proxyforge_color_mode=${proxyforge_color_mode,,}
case "${proxyforge_color_mode}" in
  always | 1 | true | yes)
    color_enabled=true
    ;;
  never | 0 | false | no)
    ;;
  *)
    if [[ -z "${NO_COLOR+x}" && "${TERM:-}" != "dumb" && -t 1 ]]; then
      color_enabled=true
    fi
    ;;
esac
readonly color_enabled
readonly proxyforge_color_mode

if [[ "${color_enabled}" == true ]]; then
  readonly color_reset=$'\033[0m'
  readonly color_bold_orange=$'\033[1;38;5;208m'
  readonly color_orange=$'\033[38;5;208m'
  readonly color_blue=$'\033[34m'
  readonly color_bold_green=$'\033[1;32m'
  readonly color_bold_yellow=$'\033[1;33m'
  readonly color_bold_red=$'\033[1;31m'
else
  readonly color_reset=""
  readonly color_bold_orange=""
  readonly color_orange=""
  readonly color_blue=""
  readonly color_bold_green=""
  readonly color_bold_yellow=""
  readonly color_bold_red=""
fi

current_version=""
current_installed=false
requested_action="install"

selected_asset=""
selected_checksum=""
temporary_dir=""
staged_path=""

paint() {
  printf '%s%s%s' "$1" "$2" "${color_reset}"
}

info() {
  printf '%s %s\n' "$(paint "${color_blue}" '[信息]')" "$*"
}

step() {
  printf '\n%s %s\n' "$(paint "${color_bold_orange}" '[步骤]')" "$*"
}

result() {
  printf '%s %s\n' "$(paint "${color_bold_green}" '[结果]')" "$*"
}

warn() {
  printf '%s %s\n' "$(paint "${color_bold_yellow}" '[警告]')" "$*"
}

die() {
  printf '%s %s\n' "$(paint "${color_bold_red}" '[错误]')" "$*" >&2
  exit 1
}

print_header() {
  local subtitle=${1:-自身安装与更新}
  printf '%s\n' "$(paint "${color_bold_orange}" '╭─ ProxyForge')"
  printf '%s\n' "$(paint "${color_orange}" "│ ${subtitle}")"
  printf '%s\n' "$(paint "${color_bold_orange}" '╰──────────────────────────────────────────────')"
}

print_release_summary() {
  local current_display="未安装"
  local action_display="安装"
  if [[ "${current_installed}" == true ]]; then
    current_display=${current_version}
  fi
  case "${installation_action}" in
    upgrade) action_display="更新" ;;
    replace) action_display="重新安装" ;;
    current) action_display="无需更新" ;;
    newer) action_display="跳过，避免降级" ;;
  esac
  printf '\n%s\n' "$(paint "${color_bold_orange}" '发布信息')"
  printf '%s\n' "$(paint "${color_orange}" '----------------------------------------')"
  printf '  %s：Linux/%s\n' "$(paint "${color_orange}" '平台')" "$1"
  printf '  %s：%s\n' "$(paint "${color_orange}" '当前版本')" "${current_display}"
  printf '  %s：%s\n' "$(paint "${color_orange}" '目标版本')" "${resolved_version}"
  printf '  %s：%s\n' "$(paint "${color_orange}" '执行操作')" "${action_display}"
  printf '%s\n' "$(paint "${color_orange}" '----------------------------------------')"
}

print_completion() {
  local action_label="安装完成"
  if [[ "${installation_action}" == "upgrade" ]]; then
    action_label="更新完成"
  elif [[ "${installation_action}" == "replace" ]]; then
    action_label="重新安装完成"
  fi
  printf '\n%s\n' "$(paint "${color_bold_orange}" '╭─ ProxyForge')"
  printf '%s│ %s%s%s\n' "${color_orange}" "${color_bold_green}" "${action_label}" "${color_reset}"
  printf '%s\n' "$(paint "${color_orange}" "│ 版本：${resolved_version}")"
  printf '%s\n' "$(paint "${color_orange}" "│ 位置：${install_path}")"
  printf '%s\n' "$(paint "${color_orange}" "│ 运行命令：sudo ${install_path}")"
  printf '%s\n' "$(paint "${color_bold_orange}" '╰──────────────────────────────────────────────')"
}

cleanup() {
  if [[ -n "${staged_path}" && -e "${staged_path}" ]]; then
    rm -f -- "${staged_path}"
  fi
  if [[ -n "${temporary_dir}" && -d "${temporary_dir}" ]]; then
    rm -rf -- "${temporary_dir}"
  fi
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || die "缺少必需命令：$1"
}

require_root() {
  [[ ${EUID} -eq 0 ]] || die "请使用 root 运行，例如：curl ... | sudo bash"
}

proxyforge_binary_exists() {
  [[ -e "${install_path}" || -L "${install_path}" ]]
}

remove_proxyforge_binary() {
  [[ ! -d "${install_path}" ]] || die "卸载目标是目录，拒绝删除：${install_path}"
  rm -f -- "${install_path}"
}

uninstall_proxyforge() {
  print_header "自身卸载"
  step "检查 ProxyForge 安装状态"
  if ! proxyforge_binary_exists; then
    result "ProxyForge 当前未安装，无需卸载。"
    return
  fi

  info "卸载位置：${install_path}"
  step "删除 ProxyForge 二进制"
  remove_proxyforge_binary
  if proxyforge_binary_exists; then
    die "卸载后仍检测到文件：${install_path}"
  fi
  result "ProxyForge 自身已卸载。"
  info "sing-box、Xray、节点配置和 ProxyForge 数据均已保留。"
}

parse_arguments() {
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --yes | -y)
        shift
        ;;
      uninstall | --uninstall)
        requested_action="uninstall"
        shift
        ;;
      *)
        die "未知参数：$1"
        ;;
    esac
  done
}

detect_current_version() {
  if [[ -n "${current_version}" ]]; then
    return
  fi
  if [[ -x "${install_path}" ]]; then
    current_installed=true
    local version_output
    version_output=$("${install_path}" --version 2>/dev/null || true)
    current_version=${version_output%%$'\n'*}
    current_version=${current_version#proxyforge }
  fi
  if [[ -z "${current_version}" ]]; then
    current_version="unknown"
  fi
}

compare_versions() {
  local left=$1
  local right=$2
  local version_pattern='^v([0-9]+)\.([0-9]+)\.([0-9]+)([-+][0-9A-Za-z.-]+)?$'
  local left_major left_minor left_patch left_suffix
  local right_major right_minor right_patch right_suffix

  [[ "${left}" =~ ${version_pattern} ]] || return 1
  left_major=$((10#${BASH_REMATCH[1]}))
  left_minor=$((10#${BASH_REMATCH[2]}))
  left_patch=$((10#${BASH_REMATCH[3]}))
  left_suffix=${BASH_REMATCH[4]:-}
  [[ "${right}" =~ ${version_pattern} ]] || return 1
  right_major=$((10#${BASH_REMATCH[1]}))
  right_minor=$((10#${BASH_REMATCH[2]}))
  right_patch=$((10#${BASH_REMATCH[3]}))
  right_suffix=${BASH_REMATCH[4]:-}

  local left_parts=("${left_major}" "${left_minor}" "${left_patch}")
  local right_parts=("${right_major}" "${right_minor}" "${right_patch}")
  local index
  for index in 0 1 2; do
    if ((left_parts[index] < right_parts[index])); then
      version_order=-1
      return
    fi
    if ((left_parts[index] > right_parts[index])); then
      version_order=1
      return
    fi
  done

  local left_prerelease="" right_prerelease=""
  if [[ "${left_suffix}" == -* ]]; then
    left_prerelease=${left_suffix#-}
    left_prerelease=${left_prerelease%%+*}
  fi
  if [[ "${right_suffix}" == -* ]]; then
    right_prerelease=${right_suffix#-}
    right_prerelease=${right_prerelease%%+*}
  fi
  if [[ -z "${left_prerelease}" && -z "${right_prerelease}" ]]; then
    version_order=0
  elif [[ -z "${left_prerelease}" ]]; then
    version_order=1
  elif [[ -z "${right_prerelease}" ]]; then
    version_order=-1
  elif [[ "${left_prerelease}" == "${right_prerelease}" ]]; then
    version_order=0
  elif [[ "${left_prerelease}" < "${right_prerelease}" ]]; then
    version_order=-1
  else
    version_order=1
  fi
}

classify_installation() {
  installation_action="install"
  detect_current_version
  if [[ "${current_installed}" != true && "${current_version}" != "unknown" ]]; then
    current_installed=true
  fi
  if [[ "${current_installed}" != true ]]; then
    return
  fi
  if ! compare_versions "${current_version}" "${resolved_version}"; then
    installation_action="replace"
    return
  fi
  case "${version_order}" in
    -1) installation_action="upgrade" ;;
    0) installation_action="current" ;;
    1) installation_action="newer" ;;
  esac
}

detect_architecture() {
  case "$(uname -m)" in
    x86_64 | amd64)
      printf 'amd64\n'
      ;;
    aarch64 | arm64)
      printf 'arm64\n'
      ;;
    *)
      die "不支持的处理器架构：$(uname -m)（仅支持 amd64、arm64）"
      ;;
  esac
}

download() {
  local url=$1
  local output=$2
  local show_progress=${3:-false}
  if command -v curl >/dev/null 2>&1; then
    if [[ "${show_progress}" == true && -t 2 ]]; then
      curl --proto '=https' --tlsv1.2 --fail --location --show-error --progress-bar \
        --retry 3 --output "${output}" "${url}"
    else
      curl --proto '=https' --tlsv1.2 --fail --location --silent --show-error \
        --retry 3 --output "${output}" "${url}"
    fi
    return
  fi
  if command -v wget >/dev/null 2>&1; then
    if [[ "${show_progress}" == true && -t 2 ]]; then
      wget --https-only --secure-protocol=TLSv1_2 --no-verbose --show-progress \
        --progress=bar:force:noscroll --output-document="${output}" "${url}"
    else
      wget --https-only --secure-protocol=TLSv1_2 --quiet --output-document="${output}" "${url}"
    fi
    return
  fi
  die "需要 curl 或 wget 才能下载发布文件"
}

download_direct() {
  local url=$1
  local output=$2
  if command -v curl >/dev/null 2>&1; then
    curl --proto '=https' --tlsv1.2 --fail --location --silent --show-error \
      --retry 3 --noproxy '*' --output "${output}" "${url}"
    return
  fi
  if command -v wget >/dev/null 2>&1; then
    wget --https-only --secure-protocol=TLSv1_2 --quiet --no-proxy \
      --output-document="${output}" "${url}"
    return
  fi
  die "需要 curl 或 wget 才能下载发布文件"
}

read_api_version() {
  local api_file=$1
  local api_size
  api_size=$(wc -c < "${api_file}")
  [[ ${api_size} -le ${max_release_api_size} ]] || return 1

  local versions=()
  mapfile -t versions < <(
    sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "${api_file}"
  )
  [[ ${#versions[@]} -eq 1 ]] || return 1
  [[ "${versions[0]}" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([+-][0-9A-Za-z.-]+)?$ ]] || return 1
  resolved_version=${versions[0]}
}

read_version_file() {
  local version_file=$1
  local version_size
  version_size=$(wc -c < "${version_file}")
  [[ ${version_size} -le ${max_version_size} ]] || return 1

  local version_lines=()
  mapfile -t version_lines < "${version_file}"
  [[ ${#version_lines[@]} -eq 1 ]] || return 1
  [[ "${version_lines[0]}" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([+-][0-9A-Za-z.-]+)?$ ]] || return 1
  resolved_version=${version_lines[0]}
}

resolve_latest_version() {
  local release_base=$1
  local api_file="${temporary_dir}/latest-release.json"
  local version_file="${temporary_dir}/version"

  if download "${latest_release_api}" "${api_file}" && read_api_version "${api_file}"; then
    info "最新正式版本：${resolved_version}（GitHub Releases API）"
    return
  fi
  warn "通过当前网络或代理获取 GitHub Releases API 失败，尝试忽略代理直连"
  if download_direct "${latest_release_api}" "${api_file}" && read_api_version "${api_file}"; then
    info "最新正式版本：${resolved_version}（GitHub Releases API 直连）"
    return
  fi
  warn "GitHub Releases API 不可用，改用 Release version 文件"
  if download "${release_base}/version" "${version_file}" && read_version_file "${version_file}"; then
    info "最新正式版本：${resolved_version}（Release version 文件）"
    return
  fi
  return 1
}

select_asset() {
  local manifest=$1
  local architecture=$2
  local checksum asset extra
  local preferred_matches=0
  local legacy_matches=0
  local preferred_asset="" preferred_checksum=""
  local legacy_asset="" legacy_checksum=""
  local preferred_pattern="^proxyforge_linux_${architecture}_v[0-9]+\\.[0-9]+\\.[0-9]+([+-][0-9A-Za-z.-]+)?$"
  local legacy_pattern="^proxyforge_v[0-9]+\\.[0-9]+\\.[0-9]+([+-][0-9A-Za-z.-]+)?_linux_${architecture}$"

  while read -r checksum asset extra; do
    [[ -z "${extra:-}" ]] || continue
    [[ "${checksum}" =~ ^[0-9A-Fa-f]{64}$ ]] || continue
    if [[ "${asset}" =~ ${preferred_pattern} ]]; then
      preferred_checksum=${checksum,,}
      preferred_asset=${asset}
      preferred_matches=$((preferred_matches + 1))
    elif [[ "${asset}" =~ ${legacy_pattern} ]]; then
      legacy_checksum=${checksum,,}
      legacy_asset=${asset}
      legacy_matches=$((legacy_matches + 1))
    fi
  done < "${manifest}"

  if [[ ${preferred_matches} -eq 1 ]]; then
    selected_checksum=${preferred_checksum}
    selected_asset=${preferred_asset}
    return
  fi
  if [[ ${preferred_matches} -eq 0 && ${legacy_matches} -eq 1 ]]; then
    selected_checksum=${legacy_checksum}
    selected_asset=${legacy_asset}
    return
  fi
  die "校验清单中匹配 ${architecture} 的发布文件数量异常：新格式 ${preferred_matches}，旧格式 ${legacy_matches}"
}

main() {
  requested_action="install"
  parse_arguments "$@"
  [[ "$(uname -s)" == "Linux" ]] || die "该安装脚本仅支持 Linux"
  require_root

  if [[ "${requested_action}" == "uninstall" ]]; then
    require_command rm
    uninstall_proxyforge
    return
  fi

  require_command uname
  require_command mktemp
  require_command sha256sum
  require_command wc
  require_command sed
  require_command install
  require_command mv

  local architecture
  architecture=$(detect_architecture)
  local requested_version=${PROXYFORGE_VERSION:-latest}
  resolved_version=""
  local release_base
  case "${requested_version}" in
    latest)
      release_base="https://github.com/${repository}/releases/latest/download"
      ;;
    v[0-9]*.[0-9]*.[0-9]*)
      [[ "${requested_version}" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([+-][0-9A-Za-z.-]+)?$ ]] || \
        die "PROXYFORGE_VERSION 格式无效：${requested_version}"
      release_base="https://github.com/${repository}/releases/download/${requested_version}"
      resolved_version=${requested_version}
      ;;
    *)
      die "PROXYFORGE_VERSION 格式无效：${requested_version}"
      ;;
  esac

  temporary_dir=$(mktemp -d /tmp/proxyforge-install.XXXXXX)
  trap cleanup EXIT
  trap 'exit 130' INT
  trap 'exit 143' TERM

  print_header
  step "检查发布版本"
  if [[ "${requested_version}" == "latest" ]]; then
    if resolve_latest_version "${release_base}"; then
      release_base="https://github.com/${repository}/releases/download/${resolved_version}"
    else
      die "无法通过 GitHub Releases API 或 version 文件读取最新正式版本"
    fi
  fi

  installation_action="install"
  if [[ -n "${resolved_version}" ]]; then
    classify_installation
    print_release_summary "${architecture}"
    if [[ "${installation_action}" == "current" ]]; then
      if [[ "${requested_version}" == "latest" ]]; then
        result "当前已是最新正式版本，无需更新。"
      else
        result "当前已是指定版本，无需安装。"
      fi
      return
    fi
    if [[ "${installation_action}" == "newer" ]]; then
      result "当前版本高于目标版本，已跳过以避免降级。"
      return
    fi
    if [[ "${installation_action}" == "upgrade" ]]; then
      info "准备从 ${current_version} 更新到 ${resolved_version}"
    elif [[ "${installation_action}" == "replace" ]]; then
      warn "无法识别当前版本 ${current_version}，将重新安装 ${resolved_version}"
    fi
  fi

  step "读取并校验发布文件"
  info "正在读取 Linux/${architecture} 发布清单"
  download "${release_base}/SHA256SUMS" "${temporary_dir}/SHA256SUMS"
  select_asset "${temporary_dir}/SHA256SUMS" "${architecture}"
  if [[ -n "${resolved_version}" ]]; then
    local preferred_asset="proxyforge_linux_${architecture}_${resolved_version}"
    local legacy_asset="proxyforge_${resolved_version}_linux_${architecture}"
    [[ "${selected_asset}" == "${preferred_asset}" || "${selected_asset}" == "${legacy_asset}" ]] || \
      die "version 与校验清单中的二进制文件名不一致"
  fi

  info "正在下载：${selected_asset}"
  download "${release_base}/${selected_asset}" "${temporary_dir}/${selected_asset}" true
  printf '%s  %s\n' "${selected_checksum}" "${selected_asset}" |
    (cd "${temporary_dir}" && sha256sum --check --status -) || die "SHA-256 校验失败"
  result "SHA-256 校验通过"

  step "写入 ProxyForge"
  if [[ ! -d "${install_dir}" ]]; then
    install -d -m 0755 "${install_dir}"
  fi
  staged_path="${install_dir}/.proxyforge.new.$$"
  install -m 0755 "${temporary_dir}/${selected_asset}" "${staged_path}"
  mv -f -- "${staged_path}" "${install_path}"
  staged_path=""

  "${install_path}" --version
  print_completion
}

if [[ "${BASH_SOURCE[0]:-$0}" == "$0" ]]; then
  main "$@"
fi
