#!/usr/bin/env bash
set -euo pipefail

readonly repository="JokerKernel/proxyforge"
readonly install_dir="/usr/local/sbin"
readonly install_path="${install_dir}/proxyforge"
readonly max_version_size=256
readonly max_release_api_size=$((1024 * 1024))
readonly latest_release_api="https://api.github.com/repos/${repository}/releases/latest"

operation="install"
current_version=""
assume_yes=false

selected_asset=""
selected_checksum=""
temporary_dir=""
staged_path=""

info() {
  printf '[ProxyForge/安装] %s\n' "$*"
}

die() {
  printf '[ProxyForge/错误] %s\n' "$*" >&2
  exit 1
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

parse_arguments() {
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --update)
        operation="update"
        shift
        ;;
      --yes | -y)
        assume_yes=true
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
    local version_output
    version_output=$("${install_path}" --version 2>/dev/null || true)
    current_version=${version_output%%$'\n'*}
    current_version=${current_version#proxyforge }
  fi
  if [[ -z "${current_version}" ]]; then
    current_version="unknown"
  fi
}

confirm_update() {
  if [[ "${assume_yes}" == true ]]; then
    return
  fi
  local answer
  while true; do
    printf '确认将 ProxyForge 从 %s 升级到 %s？请输入 yes/y，或输入 q 取消：' \
      "${current_version}" "${resolved_version}"
    if ! IFS= read -r answer; then
      die "执行自升级前需要交互确认，自动化时请显式提供 --yes"
    fi
    case "${answer}" in
      yes | YES | Yes | y | Y)
        return
        ;;
      q | Q)
        die "用户取消自升级"
        ;;
      *)
        info "输入无效，请输入 yes、y 或 q"
        ;;
    esac
  done
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
  if command -v curl >/dev/null 2>&1; then
    curl --proto '=https' --tlsv1.2 --fail --location --silent --show-error \
      --retry 3 --output "${output}" "${url}"
    return
  fi
  if command -v wget >/dev/null 2>&1; then
    wget --https-only --secure-protocol=TLSv1_2 --quiet --output-document="${output}" "${url}"
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
    info "通过 GitHub Releases API 获取最新正式版本：${resolved_version}"
    return
  fi
  info "GitHub Releases API 不可用，尝试读取 Release version 文件"
  if download "${release_base}/version" "${version_file}" && read_version_file "${version_file}"; then
    info "通过 version 文件获取最新正式版本：${resolved_version}"
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
  parse_arguments "$@"
  [[ "$(uname -s)" == "Linux" ]] || die "该安装脚本仅支持 Linux"
  require_root

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
  if [[ "${operation}" == "update" ]]; then
    requested_version="latest"
  fi
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

  if [[ "${requested_version}" == "latest" ]]; then
    if resolve_latest_version "${release_base}"; then
      release_base="https://github.com/${repository}/releases/download/${resolved_version}"
    else
      if [[ "${operation}" == "update" ]]; then
        die "无法通过 GitHub Releases API 或 version 文件读取最新正式版本"
      fi
      info "无法读取最新版本元数据，使用 latest/download 兼容方式"
    fi
  fi

  if [[ "${operation}" == "update" ]]; then
    detect_current_version
    if [[ "${current_version}" == "${resolved_version}" ]]; then
      printf '[ProxyForge/结果] 当前版本 %s 已是最新正式版本。\n' "${current_version}"
      return
    fi
    printf '[ProxyForge/更新] 当前版本：%s\n' "${current_version}"
    printf '[ProxyForge/更新] 目标版本：%s\n' "${resolved_version}"
    printf '[ProxyForge/更新] 脚本 SHA-256：%s\n' "$(sha256sum "${BASH_SOURCE[0]}" | while read -r hash _; do printf '%s' "${hash}"; done)"
    printf '[ProxyForge/风险] 将以 root 执行脚本并替换 %s。\n' "${install_path}"
    confirm_update
  fi

  info "检测到 Linux/${architecture}，正在读取发布清单"
  download "${release_base}/SHA256SUMS" "${temporary_dir}/SHA256SUMS"
  select_asset "${temporary_dir}/SHA256SUMS" "${architecture}"
  if [[ -n "${resolved_version}" ]]; then
    local preferred_asset="proxyforge_linux_${architecture}_${resolved_version}"
    local legacy_asset="proxyforge_${resolved_version}_linux_${architecture}"
    [[ "${selected_asset}" == "${preferred_asset}" || "${selected_asset}" == "${legacy_asset}" ]] || \
      die "version 与校验清单中的二进制文件名不一致"
  fi

  info "正在下载 ${selected_asset}"
  download "${release_base}/${selected_asset}" "${temporary_dir}/${selected_asset}"
  printf '%s  %s\n' "${selected_checksum}" "${selected_asset}" |
    (cd "${temporary_dir}" && sha256sum --check --status -) || die "SHA-256 校验失败"
  info "SHA-256 校验通过"

  if [[ ! -d "${install_dir}" ]]; then
    install -d -m 0755 "${install_dir}"
  fi
  staged_path="${install_dir}/.proxyforge.new.$$"
  install -m 0755 "${temporary_dir}/${selected_asset}" "${staged_path}"
  mv -f -- "${staged_path}" "${install_path}"
  staged_path=""

  info "已安装到 ${install_path}"
  "${install_path}" --version
  if [[ "${operation}" == "update" ]]; then
    printf '[ProxyForge/结果] ProxyForge 已升级到 %s。\n' "${resolved_version}"
  fi
  printf '运行命令：sudo %s\n' "${install_path}"
}

if [[ "${BASH_SOURCE[0]:-$0}" == "$0" ]]; then
  main "$@"
fi
