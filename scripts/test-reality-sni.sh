#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
用法：
  ./scripts/test-reality-sni.sh --host <节点IP或域名> --port <端口> --sni <允许的SNI> [选项]

选项：
  --bad-sni <域名>  要拒绝的 SNI，可重复指定；默认测试常见外部域名
  --timeout <秒>    每次探测的超时时间，默认 8 秒
  --verbose         显示 TLS 探测的原始输出
  -h, --help        显示帮助

脚本会自动探测 www.<允许的SNI> 作为允许项的子域，并从允许项证书 SAN 中选取域名做 TLS 探测；这些结果只作附加报告，不纳入 SNI 拦截判定。
结论分别显示 SNI 防护、严格模式和明文 HTTP Host 防护；HTTP Host 通过允许项与非匹配域名的响应差异判断，不改变 TLS SNI 退出码。
允许 SNI 若识别为 Cloudflare，SNI 过滤未生效时会提示严重错误（流量可能被刷）；过滤已生效时仍提示当前有 CDN 风险。

示例：
  ./scripts/test-reality-sni.sh \
    --host 203.0.113.10 --port 443 --sni speed.cloudflare.com

请在节点服务器之外的机器运行。脚本发起未认证 TLS、HTTP 和 HTTPS 探测，不读取或修改服务端配置。
EOF
}

die() {
  printf '操作失败               │ %s\n' "$*" >&2
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
  printf '跳过探测               │ 允许 SNI 已过长，无法构造允许项的子域：%s\n' "${allowed_sni}" >&2
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
color_orange=''
color_bold_orange=''
color_bold_green=''
color_bold_yellow=''
color_bold_red=''
color_bright_black=''
color_red=''
color_dim=''
color_enabled=false
# Same switch as the installer and the CLI color writer: always wins over
# NO_COLOR, never/dumb/NO_COLOR disable, otherwise only a real terminal.
proxyforge_color_mode=${PROXYFORGE_COLOR:-auto}
proxyforge_color_mode=${proxyforge_color_mode,,}
case ${proxyforge_color_mode} in
  always | 1 | true | yes)
    color_enabled=true
    ;;
  never | 0 | false | no)
    ;;
  *)
    if [[ -z ${NO_COLOR+x} && ${TERM:-} != dumb && -t 1 ]]; then
      color_enabled=true
    fi
    ;;
esac
if ${color_enabled}; then
  color_reset=$'\033[0m'
  color_bold=$'\033[1m'
  color_cyan=$'\033[36m'
  color_blue=$'\033[34m'
  color_green=$'\033[32m'
  color_yellow=$'\033[33m'
  color_orange=$'\033[38;5;208m'
  color_bold_orange=$'\033[1;38;5;208m'
  color_bold_green=$'\033[1;32m'
  color_bold_yellow=$'\033[1;33m'
  color_bold_red=$'\033[1;31m'
  color_bright_black=$'\033[90m'
  color_red=$'\033[31m'
  color_dim=$'\033[2m'
fi

print_card_header() {
  printf '%s╭─ %s%s\n' "${color_bold_orange}" "$1" "${color_reset}"
}

print_card_footer() {
  printf '%s╰──────────────────────────────────────────────%s\n' "${color_bold_orange}" "${color_reset}"
}

print_header() {
  print_card_header 'ProxyForge'
  printf '%s│ REALITY SNI 检测器%s\n' "${color_orange}" "${color_reset}"
  print_card_footer
}

print_section() {
  printf '\n'
  print_card_header "$1"
  print_card_footer
}

format_probe_title() {
  local title=$1
  local marker='（证书 SAN）'
  if [[ ${title} == '证书 SAN' ]]; then
    printf '%s%s%s' "${color_bold_orange}" "${title}" "${color_reset}"
    return
  fi
  if [[ ${title} == *"${marker}" ]]; then
    printf '%s%s%s%s%s%s' "${color_bold}" "${title%"${marker}"}" "${color_reset}" "${color_bold_orange}" "${marker}" "${color_reset}"
    return
  fi
  printf '%s%s%s' "${color_bold}" "${title}" "${color_reset}"
}

print_source_line() {
  local source=$1
  if [[ ${source} == '允许项证书 SAN' ]]; then
    printf '    来源: %s%s%s\n' "${color_orange}" "${source}" "${color_reset}"
    return
  fi
  printf '    来源: %s\n' "${source}"
}

domain_has_suffix() {
  local host=${1,,}
  local suffix=${2,,}
  host=${host%.}
  suffix=${suffix%.}
  [[ -n ${host} && -n ${suffix} ]] || return 1
  [[ ${host} == "${suffix}" || ${host} == *."${suffix}" ]]
}

match_cdn_provider() {
  local name=$1
  local provider suffixes suffix
  while IFS=$'\t' read -r provider suffixes; do
    [[ -n ${provider} ]] || continue
    for suffix in ${suffixes}; do
      if domain_has_suffix "${name}" "${suffix}"; then
        printf '%s' "${provider}"
        return 0
      fi
    done
  done <<'EOF'
Cloudflare	cloudflare.com cloudflare.net cdn.cloudflare.net pages.dev trycloudflare.com workers.dev r2.dev
Akamai	akamai.net akamaiedge.net akamaihd.net akamaized.net edgekey.net edgesuite.net akadns.net akamaitech.net akamai.com
AWS CloudFront	cloudfront.net
Fastly	fastly.net fastlylb.net fastly.com
Microsoft/Azure CDN	azureedge.net azurefd.net msedge.net trafficmanager.net gallerycdn.vsassets.io aspnetcdn.com office.net
Google CDN	gstatic.com googleusercontent.com 1e100.net googlehosted.com googlevideo.com
Apple CDN	mzstatic.com cdn-apple.com apple-dns.net icloud-content.com
阿里云 CDN	alicdn.com kunlun.com kunlunar.com kunlunle.com kunlunpi.com cdngslb.com taobaocdn.com tbcdn.cn
腾讯云 CDN	qcloudcdn.com cdn.dnsv1.com tcdn.qq.com gdtimg.com qpic.cn myqcloud.com
百度云 CDN	bdstatic.com bcebos.com bdydns.com
华为云 CDN	hwcdrn.com cdn.myhuaweicloud.com
字节跳动 CDN	bytecdn.cn byteimg.com pstatp.com bytescm.com douyincdn.com ibytedtos.com
网宿	wscdns.com lxdns.com ourglb0.com wscloudcdn.com chinacache.net
七牛	qiniucdn.com qnssl.com clouddn.com qiniudn.com
又拍云	upaiyun.com upyun.com
金山云 CDN	ks-cdn.com ksyuncdn.com
CDN77	cdn77.org cdn77.com
Bunny CDN	b-cdn.net
Imperva	incapdns.net
Sucuri	sucuri.net
KeyCDN	kxcdn.com
StackPath	stackpathcdn.com stackpathdns.com
CacheFly	cachefly.net
Gcore	gcdn.co
QUIC.cloud	quic.cloud
Edgio	llnwd.net llnw.net edgecastcdn.net
jsDelivr	jsdelivr.net
Facebook CDN	fbcdn.net
X CDN	twimg.com
Cloudinary	cloudinary.com
Imgix	imgix.net
Shopify CDN	shopifycdn.com shopifycloud.com
GitHub CDN	githubassets.com
Amazon 图片 CDN	images-amazon.com ssl-images-amazon.com media-amazon.com
Vercel	vercel-dns.com vercel.app
Netlify	netlify.app netlify.com
EOF
  return 1
}

lookup_cname() {
  local domain=$1
  local record=''
  if command -v dig >/dev/null 2>&1; then
    record=$(timeout --signal=TERM 3s dig +short +time=2 +tries=1 CNAME "${domain}" 2>/dev/null | awk 'NF && $1 !~ /^;/ {print $1; exit}' || true)
  elif command -v host >/dev/null 2>&1; then
    record=$(timeout --signal=TERM 3s host -t CNAME "${domain}" 2>/dev/null | awk '/alias for/{print $NF; exit}' || true)
  fi
  record=${record%.}
  printf '%s' "${record}"
}

classify_allowed_cdn() {
  local candidate matched cname
  local -a names=("${allowed_sni}")
  detected_cdn=''
  if ((${#probe_certificate_dns_names[@]} > 0)); then
    names+=("${probe_certificate_dns_names[@]}")
  fi
  for candidate in "${names[@]}"; do
    if matched=$(match_cdn_provider "${candidate}"); then
      detected_cdn=${matched}
      return
    fi
  done
  if [[ ${probe_certificate_info,,} == *cloudflare* ]]; then
    detected_cdn='Cloudflare'
    return
  fi
  cname=$(lookup_cname "${allowed_sni}")
  if [[ -n ${cname} ]] && matched=$(match_cdn_provider "${cname}"); then
    detected_cdn=${matched}
  fi
}

print_allowed_cdn_finding() {
  classify_allowed_cdn
  if [[ ${detected_cdn} == Cloudflare ]]; then
    printf '    %s● CDN：Cloudflare%s\n' "${color_red}" "${color_reset}"
  elif [[ -n ${detected_cdn} ]]; then
    printf '    %s● CDN：%s%s\n' "${color_yellow}" "${detected_cdn}" "${color_reset}"
  else
    printf '    %s● CDN：未发现明显特征%s\n' "${color_dim}" "${color_reset}"
  fi
}

card_display_width() {
  local s=$1
  local LC_ALL=C
  local width=0
  local i char
  # Match the standalone installer's width handling without requiring a locale.
  for ((i = 0; i < ${#s}; i++)); do
    char=${s:i:1}
    if [[ ${char} < $'\200' ]]; then
      width=$((width + 1))
    elif [[ ${char} > $'\277' ]]; then
      width=$((width + 2))
    fi
  done
  printf '%s' "${width}"
}

# Status words follow decorateConfigCardValue in internal/system/color.go.
card_status_color() {
  case $1 in
    运行中 | 已开启 | 已启用) printf '%s' "${color_orange}" ;;
    已停止) printf '%s' "${color_bold_yellow}" ;;
    已失败 | 无法读取) printf '%s' "${color_bold_red}" ;;
    未开启 | 未生成 | 不限制 | 未使用 | 已停用) printf '%s' "${color_bright_black}" ;;
  esac
}

# Match the CLI's printLabeledCard: a 10-column label, an open right edge,
# an orange bar, and a dim "--" description.
print_card_row() {
  local label=$1
  local value=$2
  local value_color=${3-}
  local width padding status extra status_color
  width=$(card_display_width "${label}")
  padding=$((10 - width))
  ((padding >= 1)) || padding=1
  if [[ ${value} == *'  -- '* ]]; then
    status=${value%%  -- *}
    extra=${value#*  -- }
    status_color=$(card_status_color "${status}")
    printf '%s│%s %s%*s%s%s%s  %s-- %s%s\n' \
      "${color_orange}" "${color_reset}" "${label}" "${padding}" '' \
      "${status_color}" "${status}" "${color_reset}" \
      "${color_bright_black}" "${extra}" "${color_reset}"
    return
  fi
  if [[ -n ${value_color} ]]; then
    printf '%s│%s %s%*s%s%s%s\n' \
      "${color_orange}" "${color_reset}" "${label}" "${padding}" '' \
      "${value_color}" "${value}" "${color_reset}"
    return
  fi
  printf '%s│%s %s%*s%s\n' \
    "${color_orange}" "${color_reset}" "${label}" "${padding}" '' "${value}"
}

print_conclusion_cdn_row() {
  [[ -n ${detected_cdn} ]] || return 0
  if [[ ${detected_cdn} == Cloudflare ]] && ((rejected_sni_certificate_count > 0)); then
    print_card_row 'CDN' '✗ 严重错误：未认证回落可能把流量转到 Cloudflare' "${color_bold_red}"
    return
  fi
  if [[ ${detected_cdn} == Cloudflare ]]; then
    print_card_row 'CDN' '△ 回落仍可能到达 Cloudflare' "${color_bold_yellow}"
    return
  fi
  print_card_row 'CDN' "△ 允许 SNI 指向 ${detected_cdn}" "${color_bold_yellow}"
}

print_probe_header() {
  local number=$1
  local title=$2
  local sni=$3
  printf '\n%s[%s/%s]%s %s\n' "${color_blue}" "${number}" "${total_probes}" "${color_reset}" "$(format_probe_title "${title}")"
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
    printf '跳过探测               │ 错误 SNI 与允许 SNI 相同：%s\n' "${candidate}" >&2
    continue
  fi
  if ${probe_allowed_subdomain} && [[ ${candidate,,} == "${allowed_subdomain,,}" ]]; then
    printf '跳过探测               │ 该 SNI 已由允许项的子域探测覆盖：%s\n' "${candidate}" >&2
    continue
  fi
  filtered_rejected_snis+=("${candidate}")
done
if ((${#filtered_rejected_snis[@]} == 0)); then
  filtered_rejected_snis=('proxyforge-invalid.invalid')
fi
rejected_sni_probe_count=${#filtered_rejected_snis[@]}

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
if ${probe_allowed_subdomain}; then
  http_host_candidates+=("${allowed_subdomain}")
  http_host_candidate_sources+=('允许项的子域')
fi
declare -a http_host_probes=()
declare -a http_host_sources=()
declare -a tls_extra_snis=()
declare -a tls_extra_sources=()
declare -a tls_extra_got_certificate=()
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
total_probes=$((3 + rejected_sni_probe_count + certificate_http_host_count + (2 * (${#http_host_probes[@]} + certificate_http_host_count))))
if ${probe_allowed_subdomain}; then
  total_probes=$((total_probes + 1))
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
probe_connected=false
probe_certificate_matches_sni=false
probe_certificate_info=''
declare -a probe_certificate_dns_names=()
probe_exit=0
probe_output=''
detected_cdn=''

run_probe() {
  local label=$1
  local server_name=$2
  local source=${3-}
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
  if [[ -n ${source} ]]; then
    print_source_line "${source}"
  fi

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
  if [[ ${label} == '允许项' ]]; then
    print_allowed_cdn_finding
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

http_host_probe_title() {
  local kind=$1
  local source=$2
  local title
  if [[ ${kind} == https ]]; then
    title='HTTPS Host 访问'
  else
    title='HTTP Host 伪装测试'
  fi
  if [[ ${source} == '允许项证书 SAN' ]]; then
    title+='（证书 SAN）'
  elif [[ ${source} == '证书 SAN 不足时的补充域名' ]]; then
    title+='（补充域名）'
  fi
  printf '%s' "${title}"
}

run_http_host_probe() {
  local host=$1
  local source=$2
  local title
  local output_file error_file http_code http_exit http_url="http://${endpoint}/"
  probe_number=$((probe_number + 1))
  output_file="${probe_tmp}/probe-${probe_number}-http-host.log"
  error_file="${output_file}.err"
  title=$(http_host_probe_title http "${source}")
  printf '\n%s[%s/%s]%s %s\n' "${color_blue}" "${probe_number}" "${total_probes}" "${color_reset}" "$(format_probe_title "${title}")"
  printf '    Host: %s%s%s\n' "${color_cyan}" "${host}" "${color_reset}"
  print_source_line "${source}"
  set +e
  curl --noproxy '*' --connect-timeout "${probe_timeout}" --max-time "${probe_timeout}" \
    --silent --show-error --output /dev/null --write-out '%{http_code}' \
    -H "Host: ${host}" "${http_url}" >"${output_file}" 2>"${error_file}"
  http_exit=$?
  set -e
  http_code=$(sed -n 's/.*\([1-5][0-9][0-9]\)$/\1/p' "${output_file}" | tail -n 1)
  http_host_probe_code=${http_code}
  http_host_probe_exit=${http_exit}
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
  local title
  local output_file error_file http_code http_exit resolve_address
  local https_url="https://${host}:${node_port}/"

  resolve_address=${node_host#[}
  resolve_address=${resolve_address%]}
  probe_number=$((probe_number + 1))
  output_file="${probe_tmp}/probe-${probe_number}-https-host.log"
  error_file="${output_file}.err"
  title=$(http_host_probe_title https "${source}")
  printf '\n%s[%s/%s]%s %s\n' "${color_blue}" "${probe_number}" "${total_probes}" "${color_reset}" "$(format_probe_title "${title}")"
  printf '    URL: %s%s%s\n' "${color_cyan}" "${https_url}" "${color_reset}"
  print_source_line "${source}"
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

print_conclusion_features() {
  if ((rejected_sni_certificate_count > 0)); then
    print_card_row 'SNI 防护' '未开启  -- 错误 SNI 仍获得证书'
  elif ${allowed_has_certificate} && ${allowed_certificate_matches_sni}; then
    print_card_row 'SNI 防护' '已开启  -- 按 SNI 域名规则限制回落'
  else
    print_card_row 'SNI 防护' '无法确认  -- 允许 SNI 未取得匹配证书'
  fi
  if ! ${probe_allowed_subdomain}; then
    print_card_row '严格模式' '未检测  -- 允许 SNI 过长，无法构造子域'
  elif ! ${allowed_has_certificate} || ! ${allowed_certificate_matches_sni} || ((rejected_sni_certificate_count > 0)); then
    print_card_row '严格模式' '无法确认  -- 尚未确认有效的 SNI 防护'
  elif ${allowed_subdomain_has_certificate}; then
    print_card_row '严格模式' '未开启  -- 允许 SNI 域名的子域仍获得证书'
  else
    print_card_row '严格模式' '已开启  -- 仅允许完整 SNI 域名'
  fi
  if [[ ${http_host_status} == 已开启 ]]; then
    print_card_row 'HTTP Host' "${http_host_status}  -- 按 SNI 域名规则检查明文 Host"
  elif [[ ${http_host_status} == 未开启 ]]; then
    print_card_row 'HTTP Host' "${http_host_status}  -- 非匹配 Host 仍收到 HTTP 响应"
  else
    print_card_row 'HTTP Host' "${http_host_status}  -- ${http_host_reason}"
  fi
  print_conclusion_cdn_row
}

record_http_host_result() {
  local host=$1
  if [[ ${host,,} == "${allowed_sni,,}" ]]; then
    if [[ ${http_host_probe_code} =~ ^[1-5][0-9][0-9]$ ]]; then
      http_host_allowed_response=true
    fi
  elif ${http_host_strict} || ! domain_has_suffix "${host}" "${allowed_sni}"; then
    # Confirmed strict SNI matching also requires plaintext subdomains to close.
    if [[ ${http_host_probe_code} =~ ^[1-5][0-9][0-9]$ ]]; then
      http_host_rejected_responses=$((http_host_rejected_responses + 1))
    elif [[ ${http_host_probe_exit} == 52 || ${http_host_probe_exit} == 56 ]]; then
      http_host_rejected_closed=$((http_host_rejected_closed + 1))
    else
      http_host_rejected_uncertain=$((http_host_rejected_uncertain + 1))
    fi
  fi
}

classify_http_host() {
  http_host_status='无法确认'
  if ! ${http_host_allowed_response}; then
    http_host_reason='允许 Host 未收到 HTTP 响应'
  elif ((http_host_rejected_responses > 0)); then
    http_host_status='未开启'
    http_host_reason='非匹配 Host 仍收到 HTTP 响应'
  elif ((http_host_rejected_uncertain > 0)); then
    http_host_reason='非匹配 Host 探测超时或出错'
  elif ((http_host_rejected_closed > 0)); then
    http_host_status='已开启'
    http_host_reason='允许 Host 有响应，非匹配 Host 均被断开'
  else
    http_host_reason='缺少非匹配 Host 探测结果'
  fi
}

tls_extra_probe_title() {
  local source=$1
  case ${source} in
    '允许项证书 SAN') printf '证书 SAN' ;;
    '证书 SAN 不足时的补充域名') printf '补充域名' ;;
    *) printf '证书 SAN' ;;
  esac
}

print_probe_facts() {
  local note=$1
  local index source
  local san_total=0
  local san_certs=0
  local fallback_total=0
  local fallback_certs=0

  print_card_header '探测明细'
  if ${allowed_has_certificate} && ${allowed_certificate_matches_sni}; then
    print_card_row '允许 SNI' '收到匹配证书'
  elif ${allowed_has_certificate}; then
    print_card_row '允许 SNI' '收到证书，名称不匹配'
  else
    print_card_row '允许 SNI' '未获得证书'
  fi
  if ((rejected_sni_certificate_count > 0)); then
    print_card_row '错误 SNI' "${rejected_sni_certificate_count} 个获得了证书"
  else
    print_card_row '错误 SNI' "${rejected_sni_probe_count} 个均未获得证书"
  fi
  if ${no_sni_has_certificate}; then
    print_card_row '无 SNI' '仍获得证书'
  else
    print_card_row '无 SNI' '未获得证书'
  fi
  for index in "${!tls_extra_snis[@]}"; do
    source=${tls_extra_sources[index]}
    if [[ ${source} == '允许项证书 SAN' ]]; then
      san_total=$((san_total + 1))
      if [[ ${tls_extra_got_certificate[index]} == true ]]; then
        san_certs=$((san_certs + 1))
      fi
    else
      fallback_total=$((fallback_total + 1))
      if [[ ${tls_extra_got_certificate[index]} == true ]]; then
        fallback_certs=$((fallback_certs + 1))
      fi
    fi
  done
  if ((san_total > 0)); then
    if ((san_certs > 0)); then
      print_card_row '证书 SAN' "${san_certs} 个获得证书" "${color_bold_yellow}"
    else
      print_card_row '证书 SAN' '均未获得证书'
    fi
  fi
  if ((fallback_total > 0)); then
    if ((fallback_certs > 0)); then
      print_card_row '补充域名' "${fallback_certs} 个获得证书" "${color_bold_yellow}"
    else
      print_card_row '补充域名' '均未获得证书'
    fi
  fi
  if ((http_response_count > 0)); then
    print_card_row 'HTTP 附加' "${http_response_count} 个收到响应"
  else
    print_card_row 'HTTP 附加' '均未收到响应'
  fi
  if ((https_response_count > 0)); then
    print_card_row 'HTTPS 附加' "${https_response_count} 个收到响应"
  else
    print_card_row 'HTTPS 附加' '均未收到响应'
  fi
  if [[ -n ${note} ]]; then
    print_card_row '说明' "${note}" "${color_bold_yellow}"
  fi
  print_card_footer
}

print_log_card() {
  print_card_header '日志确认'
  print_card_row '说明' '目标站自己拒绝错误 SNI 时，外部结果也会像过滤已生效'
  print_card_row '核对' '错误请求应在本机被丢弃，不应连到配置里的回落网站'
  print_card_row 'Xray' 'sudo journalctl -u xray -f -o cat'
  print_card_row 'sing-box' 'sudo journalctl -u sing-box -f -o cat'
  print_card_footer
}

print_conclusion() {
  local result=$1
  local result_color=$2
  local note=$3
  local show_log=$4

  printf '\n'
  print_probe_facts "${note}"
  if ${show_log}; then
    printf '\n'
    print_log_card
  fi
  if ((tls_certificate_count == 0)); then
    printf '\n允许项原始输出：\n'
    sed -n '1,40p' "${allowed_output}"
  fi
  printf '\n'
  print_card_header '结论'
  print_card_row '检测结果' "${result}" "${result_color}"
  print_conclusion_features
  print_card_footer
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
      tls_extra_snis+=("${certificate_dns_name}")
      tls_extra_sources+=('允许项证书 SAN')
      selected_count=$((selected_count + 1))
      ((selected_count >= certificate_http_host_count)) && break
    fi
  done

  for fallback_host in 'www.microsoft.com' 'github.com' 'www.wikipedia.org' 'www.amazon.com'; do
    ((selected_count >= certificate_http_host_count)) && break
    if append_http_host_probe "${fallback_host}" '证书 SAN 不足时的补充域名'; then
      tls_extra_snis+=("${fallback_host}")
      tls_extra_sources+=('证书 SAN 不足时的补充域名')
      selected_count=$((selected_count + 1))
    fi
  done
}

print_header
printf '%s节点地址%s  %s%s%s\n' "${color_dim}" "${color_reset}" "${color_bold}" "${endpoint}" "${color_reset}"
printf '%s允许 SNI%s  %s%s%s\n' "${color_dim}" "${color_reset}" "${color_bold}" "${allowed_sni}" "${color_reset}"
printf '%s检测模式%s  未认证 TLS 回落探测\n' "${color_dim}" "${color_reset}"

print_section '证书 / TLS SNI 探测'
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
  run_probe '允许项的子域' "${allowed_subdomain}"
  allowed_subdomain_has_certificate=${probe_has_certificate}
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
tls_extra_certificate_count=0
tls_extra_got_certificate=()
for tls_extra_index in "${!tls_extra_snis[@]}"; do
  run_probe "$(tls_extra_probe_title "${tls_extra_sources[tls_extra_index]}")" \
    "${tls_extra_snis[tls_extra_index]}" "${tls_extra_sources[tls_extra_index]}"
  tls_extra_got_certificate+=("${probe_has_certificate}")
  if ${probe_has_certificate}; then
    tls_extra_certificate_count=$((tls_extra_certificate_count + 1))
  fi
  if ${probe_connected}; then
    tls_connection_seen=true
  fi
done

http_response_count=0
https_response_count=0
http_host_allowed_response=false
http_host_rejected_responses=0
http_host_rejected_closed=0
http_host_rejected_uncertain=0
http_host_strict=false
if ${probe_allowed_subdomain} && ${allowed_has_certificate} && ${allowed_certificate_matches_sni} &&
  ((rejected_sni_certificate_count == 0)) && ! ${allowed_subdomain_has_certificate}; then
  http_host_strict=true
fi
print_section 'HTTP 明文与 Host 探测'
run_http_probe
if ${http_received}; then
  http_response_count=$((http_response_count + 1))
fi

# 测试 HTTP Host 头：HTTP 没有 TLS SNI，只能通过 Host 头模拟域名。
for http_host_index in "${!http_host_probes[@]}"; do
  if run_http_host_probe "${http_host_probes[http_host_index]}" "${http_host_sources[http_host_index]}"; then
    http_response_count=$((http_response_count + 1))
  fi
  record_http_host_result "${http_host_probes[http_host_index]}"
done
classify_http_host
print_section 'HTTPS Host 探测'
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
if ${allowed_subdomain_has_certificate}; then
  tls_certificate_count=$((tls_certificate_count + 1))
fi
tls_certificate_count=$((tls_certificate_count + tls_extra_certificate_count))

if ((tls_certificate_count == 0)); then
  if ${tls_connection_seen} || ((http_response_count > 0)); then
    print_conclusion '? 未获得任何 TLS 证书' "${color_bold_yellow}" \
      '端口有响应，但所有 TLS 探测都没拿到证书。请核对 SNI 是否与配置一致' false
  else
    print_conclusion '? 未获得任何 TLS 证书' "${color_bold_yellow}" \
      '端口可能不通，或探测被防火墙丢弃' false
  fi
  exit 2
fi

if ((rejected_sni_certificate_count > 0)); then
  print_conclusion '✗ SNI 过滤未生效' "${color_bold_red}" \
    '请核对正在运行的配置，并确认服务已重启' false
  exit 1
fi

if ! ${allowed_has_certificate}; then
  if ${no_sni_has_certificate}; then
    print_conclusion '△ 检测到 SNI 过滤行为' "${color_bold_yellow}" \
      '填写的 SNI 可能不是当前允许的回落域名' true
  else
    print_conclusion '? 无法确认 SNI 过滤状态' "${color_bold_yellow}" '' true
  fi
  exit 2
fi

if ! ${allowed_certificate_matches_sni}; then
  print_conclusion '? 无法确认 SNI 过滤状态' "${color_bold_yellow}" \
    '证书名称不包含这个 SNI，请核对回落目标' false
  exit 2
fi

if ${no_sni_has_certificate}; then
  print_conclusion '✓ SNI 过滤生效' "${color_bold_green}" \
    '无 SNI 仍能拿到证书，回落链路保持开放' true
else
  print_conclusion '✓ 增强 SNI 过滤生效' "${color_bold_green}" '' true
fi
