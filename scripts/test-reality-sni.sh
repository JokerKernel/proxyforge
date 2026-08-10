#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
用法：
  ./scripts/test-reality-sni.sh --host <节点IP或域名> --port <端口> --sni <允许的SNI> [选项]

选项：
  --bad-sni <域名>  要拒绝的 SNI，可重复指定；默认测试常见外部域名
  --timeout <秒>    每次 TLS 探测的超时时间，默认 8 秒
  --verbose         显示每次 openssl 的原始输出
  -h, --help        显示帮助

示例：
  ./scripts/test-reality-sni.sh \
    --host 203.0.113.10 --port 443 --sni speed.cloudflare.com

请在节点服务器之外的机器运行。脚本只发起未认证 TLS 连接，不读取或修改服务端配置。
EOF
}

die() {
  printf '[错误] %s\n' "$*" >&2
  exit 64
}

node_host=''
node_port=''
allowed_sni=''
probe_timeout=8
verbose=false
declare -a rejected_snis=()

while (($# > 0)); do
  case $1 in
    --host)
      (($# >= 2)) || die '--host 缺少参数'
      node_host=$2
      shift 2
      ;;
    --port)
      (($# >= 2)) || die '--port 缺少参数'
      node_port=$2
      shift 2
      ;;
    --sni)
      (($# >= 2)) || die '--sni 缺少参数'
      allowed_sni=$2
      shift 2
      ;;
    --bad-sni)
      (($# >= 2)) || die '--bad-sni 缺少参数'
      rejected_snis+=("$2")
      shift 2
      ;;
    --timeout)
      (($# >= 2)) || die '--timeout 缺少参数'
      probe_timeout=$2
      shift 2
      ;;
    --verbose)
      verbose=true
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      die "未知参数：$1"
      ;;
  esac
done

[[ -n ${node_host} ]] || die '必须提供 --host'
[[ ${node_port} =~ ^[0-9]+$ ]] || die '--port 必须是数字'
((node_port >= 1 && node_port <= 65535)) || die '--port 必须在 1 到 65535 之间'
[[ -n ${allowed_sni} ]] || die '必须提供 --sni'
[[ ${allowed_sni} != *:* && ${allowed_sni} != */* ]] || die '--sni 必须是域名，不能包含端口或路径'
[[ ${probe_timeout} =~ ^[0-9]+$ ]] || die '--timeout 必须是整数秒'
((probe_timeout >= 1 && probe_timeout <= 60)) || die '--timeout 必须在 1 到 60 秒之间'

command -v openssl >/dev/null 2>&1 || die '未找到 openssl'
command -v timeout >/dev/null 2>&1 || die '未找到 timeout（需要 GNU coreutils）'

color_reset=''
color_bold=''
color_cyan=''
color_blue=''
color_green=''
color_yellow=''
color_red=''
color_dim=''
color_enabled=false
if [[ -t 1 && ${NO_COLOR:-} != 1 && ${NO_COLOR:-} != true && ${PROXYFORGE_COLOR:-auto} != never ]] || [[ ${PROXYFORGE_COLOR:-auto} == always ]]; then
  color_enabled=true
  color_reset=$'\033[0m'
  color_bold=$'\033[1m'
  color_cyan=$'\033[36m'
  color_blue=$'\033[34m'
  color_green=$'\033[32m'
  color_yellow=$'\033[33m'
  color_red=$'\033[31m'
  color_dim=$'\033[2m'
fi

print_header() {
  printf '%s╭────────────────────────────────────────────╮%s\n' "${color_cyan}" "${color_reset}"
  printf '%s│%s  %sProxyForge REALITY SNI 检测器%s            %s│%s\n' \
    "${color_cyan}" "${color_reset}" "${color_bold}" "${color_reset}" "${color_cyan}" "${color_reset}"
  printf '%s╰────────────────────────────────────────────╯%s\n' "${color_cyan}" "${color_reset}"
}

print_probe_header() {
  local number=$1
  local title=$2
  local sni=$3
  printf '\n%s[%s/3]%s %s%s%s\n' "${color_blue}" "${number}" "${color_reset}" "${color_bold}" "${title}" "${color_reset}"
  printf '    SNI: %s%s%s\n' "${color_cyan}" "${sni}" "${color_reset}"
}

if ((${#rejected_snis[@]} == 0)); then
  rejected_snis=('www.cloudflare.com' 'example.com')
fi

declare -a filtered_rejected_snis=()
for candidate in "${rejected_snis[@]}"; do
  [[ -n ${candidate} ]] || die '--bad-sni 不能为空'
  [[ ${candidate} != *:* && ${candidate} != */* ]] || die '--bad-sni 必须是域名，不能包含端口或路径'
  if [[ ${candidate,,} == "${allowed_sni,,}" ]]; then
    printf '[跳过] 错误 SNI 与允许 SNI 相同：%s\n' "${candidate}" >&2
    continue
  fi
  filtered_rejected_snis+=("${candidate}")
done
if ((${#filtered_rejected_snis[@]} == 0)); then
  filtered_rejected_snis=('proxyforge-invalid.invalid')
fi

case ${node_host} in
  \[*\]) endpoint="${node_host}:${node_port}" ;;
  *:*) endpoint="[${node_host}]:${node_port}" ;;
  *) endpoint="${node_host}:${node_port}" ;;
esac

probe_tmp=$(mktemp -d /tmp/proxyforge-reality-sni.XXXXXX)
trap 'rm -rf -- "${probe_tmp}"' EXIT

probe_number=0
probe_has_certificate=false
probe_received_tls=false
probe_certificate_info=''
probe_exit=0
probe_output=''

run_probe() {
  local label=$1
  local server_name=$2
  local output_file
  local -a command

  probe_number=$((probe_number + 1))
  output_file="${probe_tmp}/probe-${probe_number}.log"
  command=(timeout --signal=TERM "${probe_timeout}s" openssl s_client -connect "${endpoint}" -showcerts -msg)
  if [[ -n ${server_name} ]]; then
    command+=(-servername "${server_name}")
  else
    command+=(-noservername)
  fi

  print_probe_header "${probe_number}" "${label}" "${server_name:-<无 SNI>}"

  set +e
  "${command[@]}" </dev/null >"${output_file}" 2>&1
  probe_exit=$?
  set -e

  probe_output=${output_file}
  probe_has_certificate=false
  probe_received_tls=false
  probe_certificate_info=''
  if grep -Fq -- '-----BEGIN CERTIFICATE-----' "${output_file}"; then
    probe_has_certificate=true
  fi
  if ${probe_has_certificate}; then
    local certificate_file="${output_file}.pem"
    awk '/-----BEGIN CERTIFICATE-----/{capture=1} capture{print} /-----END CERTIFICATE-----/{exit}' \
      "${output_file}" >"${certificate_file}"
    probe_certificate_info=$(openssl x509 -in "${certificate_file}" -noout -subject -issuer -ext subjectAltName 2>/dev/null || true)
  fi
  if ${probe_has_certificate} || grep -Eq '^[[:space:]]*<<< ' "${output_file}"; then
    probe_received_tls=true
  fi

  if ${probe_has_certificate}; then
    printf '    %s● 收到证书%s  openssl退出码=%d\n' "${color_green}" "${color_reset}" "${probe_exit}"
    if [[ -n ${probe_certificate_info} ]]; then
      while IFS= read -r certificate_line; do
        printf '  证书信息：%s\n' "${certificate_line}"
      done <<<"${probe_certificate_info}"
    fi
  elif ${probe_received_tls}; then
    printf '    %s● 收到 TLS 响应%s  openssl退出码=%d\n' "${color_red}" "${color_reset}" "${probe_exit}"
  elif ((probe_exit == 124)); then
    printf '    %s● 未收到 TLS 响应%s 连接超时\n' "${color_green}" "${color_reset}"
  else
    printf '    %s● 未收到 TLS 响应%s 退出码=%d\n' "${color_green}" "${color_reset}" "${probe_exit}"
  fi

  if ${verbose}; then
    printf '%s\n' "----- ${label} 原始输出 -----"
    sed -n '1,200p' "${output_file}"
    printf '%s\n' '------------------------------'
  fi
}

print_header
printf '%s节点地址%s  %s%s%s\n' "${color_dim}" "${color_reset}" "${color_bold}" "${endpoint}" "${color_reset}"
printf '%s允许 SNI%s  %s%s%s\n' "${color_dim}" "${color_reset}" "${color_bold}" "${allowed_sni}" "${color_reset}"
printf '%s检测模式%s  未认证 TLS 回落探测\n' "${color_dim}" "${color_reset}"

run_probe '允许项' "${allowed_sni}"
allowed_has_certificate=${probe_has_certificate}
allowed_output=${probe_output}

leak_count=0
for rejected_sni in "${filtered_rejected_snis[@]}"; do
  run_probe '错误项' "${rejected_sni}"
  if ${probe_received_tls}; then
    leak_count=$((leak_count + 1))
  fi
done

run_probe '无 SNI' ''
if ${probe_received_tls}; then
  leak_count=$((leak_count + 1))
fi

printf '\n'
if ((leak_count > 0)); then
  printf '\n%s╭─ 结论 ─────────────────────────────────────╮%s\n' "${color_red}" "${color_reset}"
  printf '%s│  ✗ SNI 过滤未生效%s                         %s│%s\n' "${color_red}" "${color_reset}" "${color_red}" "${color_reset}"
  printf '%s╰────────────────────────────────────────────╯%s\n' "${color_red}" "${color_reset}"
  printf '检测到 %d 个不应放行的请求收到了 TLS 响应（证书、ServerHello 或 TLS 告警）。\n' "${leak_count}"
  printf '%s请检查当前运行配置、路由规则和服务是否已重启。%s\n' "${color_yellow}" "${color_reset}"
  exit 1
fi

if ! ${allowed_has_certificate}; then
  printf '\n%s╭─ 结论 ─────────────────────────────────────╮%s\n' "${color_yellow}" "${color_reset}"
  printf '%s│  ? 无法确认 SNI 过滤状态%s                 %s│%s\n' "${color_yellow}" "${color_reset}" "${color_yellow}" "${color_reset}"
  printf '%s╰────────────────────────────────────────────╯%s\n' "${color_yellow}" "${color_reset}"
  printf '允许的 SNI 也没有收到 TLS 证书，无法证明回落链路正常。\n'
  printf '请检查节点地址、端口、REALITY target、防火墙和目标站 TLS 状态。允许项输出：\n'
  sed -n '1,40p' "${allowed_output}"
  exit 2
fi

printf '\n%s╭─ 结论 ─────────────────────────────────────╮%s\n' "${color_green}" "${color_reset}"
printf '%s│  ✓ SNI 过滤生效%s                           %s│%s\n' "${color_green}" "${color_reset}" "${color_green}" "${color_reset}"
printf '%s╰────────────────────────────────────────────╯%s\n' "${color_green}" "${color_reset}"
printf '允许 SNI 收到证书，错误 SNI 和无 SNI 均未收到 TLS 响应。\n'
printf '%s这是外部黑盒检测结果。目标站自身也可能拒绝错误 SNI；如需强确认，请同步查看服务日志：%s\n' "${color_dim}" "${color_reset}"
printf '  Xray:    sudo journalctl -u xray -f -o cat\n'
printf '  sing-box: sudo journalctl -u sing-box -f -o cat\n'
printf '确认错误请求命中 blackhole/blocked-private 或 reject，而不是连接真实 target。\n'
