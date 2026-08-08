#!/usr/bin/env bash
set -euo pipefail

readonly repository="JokerKernel/proxyforge"
readonly install_dir="/usr/local/sbin"
readonly install_path="${install_dir}/proxyforge"

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
  [[ "$(uname -s)" == "Linux" ]] || die "该安装脚本仅支持 Linux"
  [[ ${EUID} -eq 0 ]] || die "请使用 root 运行，例如：curl ... | sudo bash"

  require_command uname
  require_command mktemp
  require_command sha256sum
  require_command install
  require_command mv

  local architecture
  architecture=$(detect_architecture)
  local requested_version=${PROXYFORGE_VERSION:-latest}
  local resolved_version=""
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

  if [[ "${requested_version}" == "latest" ]]; then
    local version_lines=()
    if download "${release_base}/version" "${temporary_dir}/version"; then
      mapfile -t version_lines < "${temporary_dir}/version"
      [[ ${#version_lines[@]} -eq 1 ]] || die "最新 Release 的 version 文件格式无效"
      resolved_version=${version_lines[0]}
      [[ "${resolved_version}" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([+-][0-9A-Za-z.-]+)?$ ]] || \
        die "最新 Release 的 version 内容无效：${resolved_version}"
      release_base="https://github.com/${repository}/releases/download/${resolved_version}"
      info "最新正式版本：${resolved_version}"
    else
      info "最新 Release 尚未提供 version，使用 latest/download 兼容方式"
    fi
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
  printf '运行命令：sudo %s\n' "${install_path}"
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  main "$@"
fi
