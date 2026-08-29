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

脚本会自动把 www.<允许的SNI> 作为应拒绝项，检查子域名是否被误放行。

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

allowed_subdomain="www.${allowed_sni}"
probe_allowed_subdomain=true
if ((${#allowed_subdomain} > 253)); then
  probe_allowed_subdomain=false
  printf '[跳过] 允许 SNI 已过长，无法构造合法子域名：%s\n' "${allowed_sni}" >&2
fi

command -v openssl >/dev/null 2>&1 || die '未找到 openssl'
command -v timeout >/dev/null 2>&1 || die '未找到 timeout（需要 GNU coreutils）'
command -v curl >/dev/null 2>&1 || die '未找到 curl'

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
  printf '\n%s[%s/%s]%s %s%s%s\n' "${color_blue}" "${number}" "${total_probes}" "${color_reset}" "${color_bold}" "${title}" "${color_reset}"
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
  if ${probe_allowed_subdomain} && [[ ${candidate,,} == "${allowed_subdomain,,}" ]]; then
    printf '[跳过] 该 SNI 已由允许项子域名探测覆盖：%s\n' "${candidate}" >&2
    continue
  fi
  filtered_rejected_snis+=("${candidate}")
done
if ((${#filtered_rejected_snis[@]} == 0)); then
  filtered_rejected_snis=('proxyforge-invalid.invalid')
fi
rejected_sni_probe_count=${#filtered_rejected_snis[@]}
if ${probe_allowed_subdomain}; then
  rejected_sni_probe_count=$((rejected_sni_probe_count + 1))
fi

certificate_http_host_count=2
declare -a http_host_candidates=(
  "${allowed_sni}"
  "${filtered_rejected_snis[0]}"
  'example.com'
  'www.google.com'
)
declare -a http_host_candidate_sources=(
  '允许 SNI'
  '错误 SNI'
  '内置域名'
  '内置域名'
)
declare -a http_host_probes=()
declare -a http_host_sources=()
for http_host_candidate_index in "${!http_host_candidates[@]}"; do
  candidate=${http_host_candidates[http_host_candidate_index]}
  duplicate=false
  for http_host in "${http_host_probes[@]}"; do
    if [[ ${candidate,,} == "${http_host,,}" ]]; then
      duplicate=true
      break
    fi
  done
  if ! ${duplicate}; then
    http_host_probes+=("${candidate}")
    http_host_sources+=("${http_host_candidate_sources[http_host_candidate_index]}")
  fi
done
total_probes=$((3 + rejected_sni_probe_count + (2 * (${#http_host_probes[@]} + certificate_http_host_count))))

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
probe_connected=false
probe_certificate_matches_sni=false
probe_certificate_info=''
declare -a probe_certificate_dns_names=()
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
  probe_connected=false
  probe_certificate_matches_sni=false
  probe_certificate_info=''
  probe_certificate_dns_names=()
  if grep -Fq -- '-----BEGIN CERTIFICATE-----' "${output_file}"; then
    probe_has_certificate=true
  fi
  if ${probe_has_certificate}; then
    local certificate_file="${output_file}.pem"
    awk '/-----BEGIN CERTIFICATE-----/{capture=1} capture{print} /-----END CERTIFICATE-----/{exit}' \
      "${output_file}" >"${certificate_file}"
    probe_certificate_info=$(openssl x509 -in "${certificate_file}" -noout -subject -issuer -ext subjectAltName 2>/dev/null || true)
    while IFS= read -r certificate_dns_name; do
      [[ -n ${certificate_dns_name} ]] && probe_certificate_dns_names+=("${certificate_dns_name}")
    done < <(grep -oE 'DNS:[^,[:space:]]+' <<<"${probe_certificate_info}" | sed 's/^DNS://')
    if [[ -n ${server_name} ]] && openssl x509 -in "${certificate_file}" -noout -checkhost "${server_name}" >/dev/null 2>&1; then
      probe_certificate_matches_sni=true
    fi
  fi
  if ${probe_has_certificate} || grep -Eq '^[[:space:]]*<<< ' "${output_file}"; then
    probe_received_tls=true
  fi
  if ${probe_received_tls} || grep -Fq -- 'CONNECTED(' "${output_file}"; then
    probe_connected=true
  fi

  if ${probe_has_certificate}; then
    printf '    %s● 收到证书%s  openssl退出码=%d\n' "${color_green}" "${color_reset}" "${probe_exit}"
    if [[ -n ${server_name} ]]; then
      if ${probe_certificate_matches_sni}; then
        printf '    %s● 证书 SAN 包含当前 SNI%s\n' "${color_green}" "${color_reset}"
      else
        printf '    %s● 证书 SAN 不包含当前 SNI%s\n' "${color_yellow}" "${color_reset}"
      fi
    fi
    if [[ -n ${probe_certificate_info} ]]; then
      while IFS= read -r certificate_line; do
        printf '  证书信息：%s\n' "${certificate_line}"
      done <<<"${probe_certificate_info}"
    fi
  elif ${probe_received_tls}; then
    printf '    %s● 收到 TLS 数据但未获得证书%s  openssl退出码=%d\n' "${color_yellow}" "${color_reset}" "${probe_exit}"
  elif ((probe_exit == 124)); then
    printf '    %s● 未收到 TLS 响应%s 连接超时\n' "${color_red}" "${color_reset}"
  else
    printf '    %s● 未收到 TLS 响应%s 退出码=%d\n' "${color_red}" "${color_reset}" "${probe_exit}"
  fi

  if ${verbose}; then
    printf '%s\n' "----- ${label} 原始输出 -----"
    sed -n '1,200p' "${output_file}"
    printf '%s\n' '------------------------------'
  fi
}

print_http_failure() {
  local http_exit=$1
  local error_file=$2
  local error_line

  printf '    %s● 未收到 HTTP 响应%s curl退出码=%d\n' "${color_red}" "${color_reset}" "${http_exit}"
  if [[ -s ${error_file} ]]; then
    while IFS= read -r error_line; do
      printf '    具体错误：%s\n' "${error_line}"
    done < <(sed -n '1,5p' "${error_file}")
  fi
}

run_http_probe() {
  local output_file
  local error_file
  local http_code
  local http_exit
  local http_url="http://${endpoint}/"

  probe_number=$((probe_number + 1))
  output_file="${probe_tmp}/probe-${probe_number}-http.log"
  error_file="${output_file}.err"
  printf '\n%s[%s/%s]%s %sHTTP 明文访问%s\n' "${color_blue}" "${probe_number}" "${total_probes}" "${color_reset}" "${color_bold}" "${color_reset}"
  printf '    URL: %s%s%s\n' "${color_cyan}" "${http_url}" "${color_reset}"

  set +e
  curl --noproxy '*' --connect-timeout "${probe_timeout}" --max-time "${probe_timeout}" \
    --silent --show-error --output /dev/null --write-out '%{http_code}' \
    "${http_url}" >"${output_file}" 2>"${error_file}"
  http_exit=$?
  set -e
  http_code=$(sed -n 's/.*\([1-5][0-9][0-9]\)$/\1/p' "${output_file}" | tail -n 1)
  http_received=false
  if [[ ${http_exit} -eq 0 && ${http_code} =~ ^[1-5][0-9][0-9]$ ]]; then
    http_received=true
  fi

  if ${http_received}; then
    print_http_result "${http_code}"
  else
    print_http_failure "${http_exit}" "${error_file}"
  fi
  if ${verbose}; then
    printf '%s\n' '----- HTTP 原始输出 -----'
    sed -n '1,80p' "${output_file}" "${error_file}"
    printf '%s\n' '-------------------------'
  fi
}

print_http_result() {
  local http_code=$1
  local description='服务端响应'
  local result_color=${color_yellow}

  case ${http_code} in
    2??)
      description='请求成功'
      result_color=${color_red}
      ;;
    3??)
      description='重定向'
      result_color=${color_red}
      ;;
    400)
      description='错误的请求'
      ;;
    4??)
      description='客户端请求错误'
      ;;
    5??)
      description='服务端错误'
      ;;
  esac
  printf '    %s● 收到 HTTP 响应%s 状态码=%s（%s）\n' \
    "${result_color}" "${color_reset}" "${http_code}" "${description}"
}

run_http_host_probe() {
  local host=$1
  local source=$2
  local output_file error_file http_code http_exit http_url="http://${endpoint}/"
  probe_number=$((probe_number + 1))
  output_file="${probe_tmp}/probe-${probe_number}-http-host.log"
  error_file="${output_file}.err"
  printf '\n%s[%s/%s]%s %sHTTP Host 伪装测试%s\n' "${color_blue}" "${probe_number}" "${total_probes}" "${color_reset}" "${color_bold}" "${color_reset}"
  printf '    Host: %s%s%s\n' "${color_cyan}" "${host}" "${color_reset}"
  printf '    来源: %s\n' "${source}"
  set +e
  curl --noproxy '*' --connect-timeout "${probe_timeout}" --max-time "${probe_timeout}" \
    --silent --show-error --output /dev/null --write-out '%{http_code}' \
    -H "Host: ${host}" "${http_url}" >"${output_file}" 2>"${error_file}"
  http_exit=$?
  set -e
  http_code=$(sed -n 's/.*\([1-5][0-9][0-9]\)$/\1/p' "${output_file}" | tail -n 1)
  if [[ ${http_exit} -eq 0 && ${http_code} =~ ^[1-5][0-9][0-9]$ ]]; then
    print_http_result "${http_code}"
    return 0
  fi
  print_http_failure "${http_exit}" "${error_file}"
  return 1
}

run_https_host_probe() {
  local host=$1
  local source=$2
  local output_file error_file http_code http_exit resolve_address
  local https_url="https://${host}:${node_port}/"

  resolve_address=${node_host#[}
  resolve_address=${resolve_address%]}
  probe_number=$((probe_number + 1))
  output_file="${probe_tmp}/probe-${probe_number}-https-host.log"
  error_file="${output_file}.err"
  printf '\n%s[%s/%s]%s %sHTTPS Host 访问%s\n' "${color_blue}" "${probe_number}" "${total_probes}" "${color_reset}" "${color_bold}" "${color_reset}"
  printf '    URL: %s%s%s\n' "${color_cyan}" "${https_url}" "${color_reset}"
  printf '    来源: %s\n' "${source}"
  set +e
  curl --noproxy '*' --connect-timeout "${probe_timeout}" --max-time "${probe_timeout}" \
    --silent --show-error --insecure --output /dev/null --write-out '%{http_code}' \
    --resolve "${host}:${node_port}:${resolve_address}" "${https_url}" \
    >"${output_file}" 2>"${error_file}"
  http_exit=$?
  set -e
  http_code=$(sed -n 's/.*\([1-5][0-9][0-9]\)$/\1/p' "${output_file}" | tail -n 1)
  if [[ ${http_exit} -eq 0 && ${http_code} =~ ^[1-5][0-9][0-9]$ ]]; then
    print_http_result "${http_code}"
    https_response_count=$((https_response_count + 1))
  else
    print_http_failure "${http_exit}" "${error_file}"
  fi
}

print_http_summary() {
  if ((http_response_count > 0)); then
    printf '%sHTTP 附加探测中有 %d 个请求收到了响应；该结果不改变 TLS SNI 判定。%s\n' \
      "${color_yellow}" "${http_response_count}" "${color_reset}"
  else
    printf 'HTTP 明文附加探测均未收到响应。\n'
  fi
  if ((https_response_count > 0)); then
    printf '%sHTTPS Host 附加探测中有 %d 个请求收到了响应；该结果不改变 TLS SNI 判定。%s\n' \
      "${color_yellow}" "${https_response_count}" "${color_reset}"
  else
    printf 'HTTPS Host 附加探测均未收到响应。\n'
  fi
}

print_log_hint() {
  printf '%s这是外部黑盒检测结果。目标站自身也可能拒绝错误 SNI；如需强确认，请同步查看服务日志：%s\n' "${color_dim}" "${color_reset}"
  printf '  Xray:    sudo journalctl -u xray -f -o cat\n'
  printf '  sing-box: sudo journalctl -u sing-box -f -o cat\n'
  printf '确认错误请求命中 blackhole/blocked-private 或 reject，而不是连接真实 target。\n'
}

append_http_host_probe() {
  local candidate=$1
  local source=$2
  local existing_host

  [[ -n ${candidate} && ${candidate} != *'*'* ]] || return 1
  for existing_host in "${http_host_probes[@]}"; do
    [[ ${candidate,,} != "${existing_host,,}" ]] || return 1
  done
  http_host_probes+=("${candidate}")
  http_host_sources+=("${source}")
}

select_additional_http_hosts() {
  local certificate_dns_name fallback_host swap_dns_name
  local dns_name_index random_index
  local selected_count=0
  local -a shuffled_dns_names=("${allowed_certificate_dns_names[@]}")

  for ((dns_name_index = ${#shuffled_dns_names[@]} - 1; dns_name_index > 0; dns_name_index--)); do
    random_index=$((RANDOM % (dns_name_index + 1)))
    swap_dns_name=${shuffled_dns_names[dns_name_index]}
    shuffled_dns_names[dns_name_index]=${shuffled_dns_names[random_index]}
    shuffled_dns_names[random_index]=${swap_dns_name}
  done

  for certificate_dns_name in "${shuffled_dns_names[@]}"; do
    if append_http_host_probe "${certificate_dns_name}" '允许项证书 SAN'; then
      selected_count=$((selected_count + 1))
      ((selected_count >= certificate_http_host_count)) && break
    fi
  done

  for fallback_host in 'www.microsoft.com' 'github.com' 'www.wikipedia.org' 'www.amazon.com'; do
    ((selected_count >= certificate_http_host_count)) && break
    if append_http_host_probe "${fallback_host}" '证书 SAN 不足时的补充域名'; then
      selected_count=$((selected_count + 1))
    fi
  done
}

print_header
printf '%s节点地址%s  %s%s%s\n' "${color_dim}" "${color_reset}" "${color_bold}" "${endpoint}" "${color_reset}"
printf '%s允许 SNI%s  %s%s%s\n' "${color_dim}" "${color_reset}" "${color_bold}" "${allowed_sni}" "${color_reset}"
printf '%s检测模式%s  未认证 TLS 回落探测\n' "${color_dim}" "${color_reset}"

run_probe '允许项' "${allowed_sni}"
allowed_has_certificate=${probe_has_certificate}
allowed_certificate_matches_sni=${probe_certificate_matches_sni}
allowed_output=${probe_output}
allowed_certificate_dns_names=("${probe_certificate_dns_names[@]}")
tls_connection_seen=${probe_connected}
select_additional_http_hosts

rejected_sni_certificate_count=0
allowed_subdomain_has_certificate=false
if ${probe_allowed_subdomain}; then
  run_probe '允许项的子域名（应拒绝）' "${allowed_subdomain}"
  allowed_subdomain_has_certificate=${probe_has_certificate}
  if ${probe_has_certificate}; then
    rejected_sni_certificate_count=$((rejected_sni_certificate_count + 1))
  fi
  if ${probe_connected}; then
    tls_connection_seen=true
  fi
fi
for rejected_sni in "${filtered_rejected_snis[@]}"; do
  run_probe '错误项' "${rejected_sni}"
  if ${probe_has_certificate}; then
    rejected_sni_certificate_count=$((rejected_sni_certificate_count + 1))
  fi
  if ${probe_connected}; then
    tls_connection_seen=true
  fi
done

run_probe '无 SNI' ''
no_sni_has_certificate=${probe_has_certificate}
if ${probe_connected}; then
  tls_connection_seen=true
fi

http_response_count=0
https_response_count=0
run_http_probe
if ${http_received}; then
  http_response_count=$((http_response_count + 1))
fi

# 测试 HTTP Host 头：HTTP 没有 TLS SNI，只能通过 Host 头模拟域名。
for http_host_index in "${!http_host_probes[@]}"; do
  if run_http_host_probe "${http_host_probes[http_host_index]}" "${http_host_sources[http_host_index]}"; then
    http_response_count=$((http_response_count + 1))
  fi
done
for http_host_index in "${!http_host_probes[@]}"; do
  run_https_host_probe "${http_host_probes[http_host_index]}" "${http_host_sources[http_host_index]}"
done

tls_certificate_count=${rejected_sni_certificate_count}
if ${allowed_has_certificate}; then
  tls_certificate_count=$((tls_certificate_count + 1))
fi
if ${no_sni_has_certificate}; then
  tls_certificate_count=$((tls_certificate_count + 1))
fi

printf '\n'
if ((tls_certificate_count == 0)); then
  printf '\n%s╭─ 结论 ─────────────────────────────────────╮%s\n' "${color_yellow}" "${color_reset}"
  printf '%s│  ? 未获得任何 TLS 证书%s                   %s│%s\n' "${color_yellow}" "${color_reset}" "${color_yellow}" "${color_reset}"
  printf '%s╰────────────────────────────────────────────╯%s\n' "${color_yellow}" "${color_reset}"
  printf '允许 SNI、错误 SNI 和无 SNI 探测均未获得证书。\n'
  if ${tls_connection_seen} || ((http_response_count > 0)); then
    printf '节点端口存在连接或响应，更可能是所有 TLS 探测均被拒绝，或填写的 SNI 与当前配置不一致。\n'
  else
    printf '节点端口可能无法访问，也可能所有探测都被防火墙或增强过滤静默丢弃。\n'
  fi
  print_http_summary
  printf '允许项原始输出：\n'
  sed -n '1,40p' "${allowed_output}"
  exit 2
fi

if ((rejected_sni_certificate_count > 0)); then
  printf '\n%s╭─ 结论 ─────────────────────────────────────╮%s\n' "${color_red}" "${color_reset}"
  printf '%s│  ✗ SNI 过滤未生效%s                         %s│%s\n' "${color_red}" "${color_reset}" "${color_red}" "${color_reset}"
  printf '%s╰────────────────────────────────────────────╯%s\n' "${color_red}" "${color_reset}"
  printf '检测到 %d 个错误 SNI 获得了 TLS 证书。\n' "${rejected_sni_certificate_count}"
  if ${allowed_subdomain_has_certificate}; then
    printf '%s允许项子域名 %s 被放行，服务端规则可能使用了宽泛的域名匹配。%s\n' \
      "${color_red}" "${allowed_subdomain}" "${color_reset}"
  fi
  print_http_summary
  printf '%s请检查当前运行配置、路由规则和服务是否已重启。%s\n' "${color_yellow}" "${color_reset}"
  exit 1
fi

if ! ${allowed_has_certificate}; then
  printf '\n%s╭─ 结论 ─────────────────────────────────────╮%s\n' "${color_yellow}" "${color_reset}"
  printf '%s│  △ 检测到 SNI 过滤行为%s                   %s│%s\n' "${color_yellow}" "${color_reset}" "${color_yellow}" "${color_reset}"
  printf '%s╰────────────────────────────────────────────╯%s\n' "${color_yellow}" "${color_reset}"
  printf '带 SNI 的允许项和错误项均未获得证书，但无 SNI 探测获得了证书。\n'
  printf '这与 SNI 过滤已经启用、但填写的 SNI 不是当前允许回落域名的情况一致。\n'
  print_http_summary
  print_log_hint
  exit 2
fi

if ! ${allowed_certificate_matches_sni}; then
  printf '\n%s╭─ 结论 ─────────────────────────────────────╮%s\n' "${color_yellow}" "${color_reset}"
  printf '%s│  ? 无法确认 SNI 过滤状态%s                 %s│%s\n' "${color_yellow}" "${color_reset}" "${color_yellow}" "${color_reset}"
  printf '%s╰────────────────────────────────────────────╯%s\n' "${color_yellow}" "${color_reset}"
  printf '允许 SNI 收到了证书，但证书 SAN 不包含该 SNI。请确认 target 是否允许 SNI 与证书域名不同。\n'
  print_http_summary
  exit 2
fi

printf '\n%s╭─ 结论 ─────────────────────────────────────╮%s\n' "${color_green}" "${color_reset}"
if ${no_sni_has_certificate}; then
  printf '%s│  ✓ SNI 过滤生效%s                           %s│%s\n' "${color_green}" "${color_reset}" "${color_green}" "${color_reset}"
else
  printf '%s│  ✓ 增强 SNI 过滤生效%s                      %s│%s\n' "${color_green}" "${color_reset}" "${color_green}" "${color_reset}"
fi
printf '%s╰────────────────────────────────────────────╯%s\n' "${color_green}" "${color_reset}"
if ${probe_allowed_subdomain}; then
  printf '允许 SNI 收到匹配证书，%d 个应拒绝 SNI（含允许项子域名）均未获得证书。\n' "${rejected_sni_probe_count}"
else
  printf '允许 SNI 收到匹配证书，%d 个错误 SNI 均未获得证书；允许项子域名因域名长度限制未检测。\n' "${rejected_sni_probe_count}"
fi
if ${no_sni_has_certificate}; then
  printf '%s无 SNI 探测仍获得了证书，说明无 SNI 回落链路保持开放。%s\n' "${color_yellow}" "${color_reset}"
else
  printf '无 SNI 探测也未获得证书，说明无 SNI 访问已被增强过滤拒绝。\n'
fi
print_http_summary
print_log_hint
