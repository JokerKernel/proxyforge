#!/usr/bin/env bash
set -euo pipefail

readonly repository="JokerKernel/proxyforge"
readonly install_dir="/usr/local/sbin"
readonly install_path="${install_dir}/proxyforge"
readonly max_version_size=256
readonly max_release_api_size=$((1024 * 1024))
readonly latest_release_api="https://api.github.com/repos/${repository}/releases/latest"
readonly release_tag_pattern='^v[0-9]+\.[0-9]+\.[0-9]+([+-][0-9A-Za-z.-]+)?$'

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
  readonly color_bold=$'\033[1m'
  readonly color_green=$'\033[32m'
  readonly color_dim=$'\033[2m'
  readonly color_yellow=$'\033[33m'
  readonly color_red=$'\033[31m'
  readonly color_bold_red=$'\033[1;31m'
else
  readonly color_reset=""
  readonly color_bold_orange=""
  readonly color_orange=""
  readonly color_blue=""
  readonly color_bold_green=""
  readonly color_bold=""
  readonly color_green=""
  readonly color_dim=""
  readonly color_yellow=""
  readonly color_red=""
  readonly color_bold_red=""
fi

current_version=""
current_installed=false
requested_action="install"
requested_version_arg=""

selected_asset=""
selected_checksum=""
temporary_dir=""
staged_path=""
release_spinner_pid=""
download_pid=""
download_route=""
last_printed_route=""

paint() {
  printf '%s%s%s' "$1" "$2" "${color_reset}"
}

detail() {
  local label=$1 text=$2 color=$3
  printf '%s %s\n' "$(paint "${color}" "[${label}]")" "${text}"
}

info() {
  detail "信息" "$1：$2" "${color_blue}"
}

step() {
  printf '\n'
  detail "步骤" "$1" "${color_bold_orange}"
}

result() {
  print_card "$2" "${color_bold_green}"
}

die() {
  local color=${color_red}
  if [[ "${requested_action}" == uninstall ]]; then
    color=${color_bold_red}
  fi
  detail "错误" "$*" "${color}" >&2
  exit 1
}

card_open() {
  printf '%s\n' "$(paint "${color_bold_orange}" '╭─ ProxyForge')"
}

card_line() {
  local text=$1 color=${2-${color_orange}}
  printf '%s %s\n' "$(paint "${color_orange}" '│')" "$(paint "${color}" "${text}")"
}

card_close() {
  printf '%s\n' "$(paint "${color_bold_orange}" '╰──────────────────────────────────────────────')"
}

print_card() {
  printf '\n'
  card_open
  card_line "$1" "${2:-${color_orange}}"
  card_close
}

update_warning() {
  detail "警告" "$1" "${color_yellow}"
}

print_header() {
  card_open
  card_line "${1:-自身安装与更新}"
  card_close
}

success() {
  printf '%s %s\n' "$(paint "${color_green}" '✓')" "$1"
}

progress_terminal() {
  [[ -t 2 && "${TERM:-}" != dumb ]]
}

stop_release_spinner() {
  if [[ -n "${release_spinner_pid}" ]]; then
    kill "${release_spinner_pid}" 2>/dev/null || true
    wait "${release_spinner_pid}" 2>/dev/null || true
    release_spinner_pid=""
    printf '\r\033[K' >&2
  fi
}

cleanup() {
  stop_release_spinner
  if [[ -n "${download_pid}" ]]; then
    kill "${download_pid}" 2>/dev/null || true
    wait "${download_pid}" 2>/dev/null || true
  fi
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
  step "检查安装状态"
  if ! proxyforge_binary_exists; then
    result "卸载结果" "ProxyForge 当前未安装，无需卸载。"
    return
  fi

  info "卸载位置" "${install_path}"
  step "删除程序文件"
  remove_proxyforge_binary
  if proxyforge_binary_exists; then
    die "卸载后仍检测到文件：${install_path}"
  fi
  result "卸载结果" "ProxyForge 自身已卸载。"
  info "保留组件数据" "sing-box、Xray、节点配置和 ProxyForge 数据均已保留。"
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
        [[ "$1" =~ ${release_tag_pattern} ]] || die "未知参数：$1"
        [[ -z "${requested_version_arg}" ]] || die "只能指定一个版本标签"
        requested_version_arg=$1
        shift
        ;;
    esac
  done
  if [[ "${requested_action}" == uninstall && -n "${requested_version_arg}" ]]; then
    die "卸载时不能指定版本标签"
  fi
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
  local url=$1 output=$2
  if command -v curl >/dev/null 2>&1; then
    curl --proto '=https' --tlsv1.2 --fail --location --silent --show-error \
      --retry 3 --output "${output}" "${url}"
  elif command -v wget >/dev/null 2>&1; then
    wget --https-only --secure-protocol=TLSv1_2 --quiet --output-document="${output}" "${url}"
  else
    die "需要 curl 或 wget 才能下载发布文件"
  fi
}

detect_download_route() {
  local host=${1#https://} proxy bypass entry
  host=${host%%/*}
  download_route="直连"
  if command -v curl >/dev/null 2>&1; then
    proxy=${https_proxy:-${HTTPS_PROXY:-${all_proxy:-${ALL_PROXY:-}}}}
    bypass=${no_proxy:-${NO_PROXY:-}}
  else
    proxy=${https_proxy:-}
    bypass=${no_proxy:-}
  fi
  [[ -n "${proxy}" ]] || return 0

  local -a domains
  IFS=', ' read -r -a domains <<< "${bypass,,}"
  for entry in "${domains[@]}"; do
    entry=${entry#.}
    if [[ "${entry}" == '*' || "${host}" == "${entry}" || "${host}" == *."${entry}" ]]; then
      return 0
    fi
  done
  download_route="代理访问"
}

print_download_route() {
  if [[ "${download_route}" != "${last_printed_route}" ]]; then
    success "下载方式：${download_route}"
    last_printed_route=${download_route}
  fi
}

check_release_download() {
  local downloader=$1 url=$2 output=$3 label status=0
  detect_download_route "${url}"
  if [[ "${downloader}" == download_direct && "${download_route}" == "代理访问" ]]; then
    download_route="直连（代理访问受限）"
  fi
  if [[ "${url}" == "${latest_release_api}" ]]; then
    label="API访问：${download_route%访问}"
  else
    label="${release_check_action}-下载方式：${download_route}"
  fi

  if progress_terminal; then
    (
      while :; do
        for frame in ⠋ ⠙ ⠹ ⠸ ⠼ ⠴ ⠦ ⠧ ⠇ ⠏; do
          printf '\r%s%s%s %s' "${color_orange}" "${frame}" "${color_reset}" "${label}" >&2
          sleep 0.1
        done
      done
    ) &
    release_spinner_pid=$!
  elif [[ "${url}" == "${latest_release_api}" ]]; then
    printf '%s\n' "${label}"
  fi

  "${downloader}" "${url}" "${output}" 2>"${temporary_dir}/release-download.err" || status=$?
  stop_release_spinner
  return "${status}"
}

print_download_progress() {
  awk -v bytes="$1" -v total="$2" -v done="$3" -v width="$4" \
    -v accent="${color_orange}" -v muted="${color_dim}" -v reset="${color_reset}" '
      function size(n) {
        if (n >= 1099511627776) return sprintf("%.1f TiB", n / 1099511627776)
        if (n >= 1073741824) return sprintf("%.1f GiB", n / 1073741824)
        if (n >= 1048576) return sprintf("%.1f MiB", n / 1048576)
        if (n >= 1024) return sprintf("%.1f KiB", n / 1024)
        return sprintf("%d B", n)
      }
      BEGIN {
        if (!done && total <= 0) {
          printf "\r已下载 %s\033[K", size(bytes)
          exit
        }
        percent = done ? 100 : int(bytes * 100 / total)
        if (!done && percent > 99) percent = 99
        filled = int(width * percent / 100)
        printf "\r%s", accent
        for (i = 0; i < filled; i++) printf "■"
        printf "%s%s", reset, muted
        for (i = filled; i < width; i++) printf "･"
        printf "%s%s%3d%%  %s / %s\033[K", reset, (width > 0 ? " " : ""), percent, size(bytes), size(total)
      }
    ' >&2
}

download_asset() {
  local url=$1 output=$2 asset=${2##*/} status=0 bytes total
  if ! progress_terminal; then
    download "${url}" "${output}" || die "发布文件下载失败（退出码 $?）：${asset}"
    bytes=$(stat -c %s -- "${output}")
    success "下载完成 · ${asset} · ${bytes} 字节"
    return
  fi

  local rows columns width=50 headers="${output}.headers" errors="${output}.err"
  if read -r rows columns < <(stty size <&2 2>/dev/null) && [[ "${columns}" =~ ^[1-9][0-9]*$ ]]; then
    width=$((columns > 80 ? 50 : columns > 30 ? columns - 30 : 0))
  fi
  if command -v curl >/dev/null 2>&1; then
    curl --proto '=https' --tlsv1.2 --fail --location --silent --show-error --retry 3 \
      --dump-header "${headers}" --output "${output}" "${url}" 2>"${errors}" &
  elif command -v wget >/dev/null 2>&1; then
    wget --https-only --secure-protocol=TLSv1_2 --no-verbose --server-response \
      --output-file="${headers}" --output-document="${output}" "${url}" 2>"${errors}" &
  else
    die "需要 curl 或 wget 才能下载发布文件"
  fi
  download_pid=$!
  while kill -0 "${download_pid}" 2>/dev/null; do
    bytes=$(stat -c %s -- "${output}" 2>/dev/null || printf 0)
    total=0
    if [[ -f "${headers}" ]]; then
      total=$(awk '
        { sub(/^[[:space:]]+/, ""); sub(/\r$/, "") }
        /^HTTP\/[0-9.]+ [0-9][0-9][0-9]/ { status = $2; total = 0 }
        tolower($1) == "content-length:" && $2 ~ /^[0-9]+$/ { total = $2 }
        END { print (status == 200 ? total : 0) }
      ' "${headers}")
    fi
    print_download_progress "${bytes}" "${total}" 0 "${width}"
    sleep 0.15
  done
  wait "${download_pid}" || status=$?
  download_pid=""
  if [[ "${status}" -ne 0 ]]; then
    printf '\r\033[K' >&2
    cat "${errors}" >&2
    die "发布文件下载失败（退出码 ${status}）：${asset}"
  fi
  bytes=$(stat -c %s -- "${output}")
  print_download_progress "${bytes}" "${bytes}" 1 "${width}"
  printf '\n' >&2
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
  [[ "${versions[0]}" =~ ${release_tag_pattern} ]] || return 1
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
  [[ "${version_lines[0]}" =~ ${release_tag_pattern} ]] || return 1
  resolved_version=${version_lines[0]}
}

resolve_latest_version() {
  local release_base=$1
  local api_file="${temporary_dir}/latest-release.json"
  local version_file="${temporary_dir}/version"

  if check_release_download download "${latest_release_api}" "${api_file}" && read_api_version "${api_file}"; then
    if progress_terminal; then success "API访问：${download_route%访问}"; fi
    return
  fi
  if check_release_download download_direct "${latest_release_api}" "${api_file}" && read_api_version "${api_file}"; then
    if progress_terminal; then success "API访问：${download_route%访问}"; fi
    return
  fi
  update_warning "GitHub Releases API 不可用，正在通过 Release version 文件解析最新版本"
  if check_release_download download "${release_base}/version" "${version_file}" && read_version_file "${version_file}"; then
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
  requested_version_arg=""
  download_route=""
  last_printed_route=""
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
  require_command stat
  require_command awk
  if ! command -v curl >/dev/null 2>&1 && ! command -v wget >/dev/null 2>&1; then
    die "需要 curl 或 wget 才能下载发布文件"
  fi

  local architecture
  architecture=$(detect_architecture)
  local requested_version=${requested_version_arg:-${PROXYFORGE_VERSION:-latest}}
  resolved_version=""
  local release_base
  if [[ "${requested_version}" == latest ]]; then
    release_base="https://github.com/${repository}/releases/latest/download"
  elif [[ "${requested_version}" =~ ${release_tag_pattern} ]]; then
    release_base="https://github.com/${repository}/releases/download/${requested_version}"
    resolved_version=${requested_version}
  else
    die "PROXYFORGE_VERSION 格式无效：${requested_version}"
  fi

  temporary_dir=$(mktemp -d /tmp/proxyforge-install.XXXXXX)
  trap cleanup EXIT
  trap 'exit 130' INT
  trap 'exit 143' TERM

  local release_check_action="安装"
  if proxyforge_binary_exists; then
    release_check_action="更新"
  fi
  if [[ "${requested_version}" == "latest" ]]; then
    if resolve_latest_version "${release_base}"; then
      release_base="https://github.com/${repository}/releases/download/${resolved_version}"
    else
      die "无法通过 GitHub Releases API 或 version 文件读取最新正式版本"
    fi
  fi

  classify_installation
  local action_label="安装"
  case "${installation_action}" in
    current)
      print_download_route
      if [[ "${requested_version}" == "latest" ]]; then
        success "当前已是最新正式版本 ${resolved_version}"
      else
        success "当前已是指定版本 ${resolved_version}"
      fi
      return
      ;;
    newer)
      print_download_route
      success "当前版本 ${current_version} 高于目标版本 ${resolved_version}，已跳过以避免降级"
      return
      ;;
    upgrade)
      action_label="更新"
      ;;
    replace)
      action_label="修复安装"
      update_warning "无法识别当前版本 ${current_version}，将重新下载修复"
      ;;
  esac

  if ! check_release_download download "${release_base}/SHA256SUMS" "${temporary_dir}/SHA256SUMS"; then
    cat "${temporary_dir}/release-download.err" >&2
    die "无法下载发布校验文件"
  fi
  print_download_route
  select_asset "${temporary_dir}/SHA256SUMS" "${architecture}"
  local preferred_asset="proxyforge_linux_${architecture}_${resolved_version}"
  local legacy_asset="proxyforge_${resolved_version}_linux_${architecture}"
  [[ "${selected_asset}" == "${preferred_asset}" || "${selected_asset}" == "${legacy_asset}" ]] || \
    die "version 与校验清单中的二进制文件名不一致"

  detect_download_route "${release_base}/${selected_asset}"
  print_download_route
  printf '\n%s %s %s %s\n' "$(paint "${color_dim}" "${action_label}")" \
    "$(paint "${color_bold}" ProxyForge)" "$(paint "${color_dim}" 版本)" "${resolved_version}"
  download_asset "${release_base}/${selected_asset}" "${temporary_dir}/${selected_asset}"

  printf '%s  %s\n' "${selected_checksum}" "${selected_asset}" |
    (cd "${temporary_dir}" && sha256sum --check --status -) || die "安装包 SHA-256 校验失败"

  if [[ ! -d "${install_dir}" ]]; then
    install -d -m 0755 "${install_dir}"
  fi
  staged_path="${install_dir}/.proxyforge.new.$$"
  install -m 0755 "${temporary_dir}/${selected_asset}" "${staged_path}"
  mv -f -- "${staged_path}" "${install_path}"
  staged_path=""

  "${install_path}" --version >/dev/null
  success "${action_label}完成 · ${install_path}"
}

if [[ "${BASH_SOURCE[0]:-$0}" == "$0" ]]; then
  main "$@"
fi
