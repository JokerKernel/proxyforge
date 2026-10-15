#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
probe_script="${repo_root}/scripts/test-reality-sni.sh"
test_tmp=$(mktemp -d /tmp/proxyforge-reality-sni-test.XXXXXX)
trap 'rm -rf -- "${test_tmp}"' EXIT
mkdir -p "${test_tmp}/bin"

cat >"${test_tmp}/bin/openssl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

if [[ ${1:-} == s_client ]]; then
  server_name=''
  allowed=${MOCK_ALLOWED_NAME:-se-edge.itunes.apple.com}
  shift
  while (($# > 0)); do
    if [[ $1 == -servername ]]; then
      server_name=${2:-}
      break
    fi
    shift
  done

  if [[ (-z ${server_name} && ${MOCK_NO_SNI_RESPONDS:-1} == 1) ||
        (${server_name} == "${allowed}" && ${MOCK_ALLOWED_SNI_RESPONDS:-1} == 1) ||
        (${server_name} == "www.${allowed}" && ${MOCK_SUBDOMAIN_SNI_RESPONDS:-0} == 1) ||
        (${MOCK_BAD_SNI_RESPONDS:-0} == 1 && ${server_name} == www.cloudflare.com) ]]; then
    printf '%s\n' '-----BEGIN CERTIFICATE-----' 'mock' '-----END CERTIFICATE-----'
    exit 0
  fi
  if [[ ${MOCK_BAD_SNI_ALERTS:-0} == 1 && ${server_name} == www.cloudflare.com ]]; then
    printf '%s\n' '<<< TLS 1.3, Alert [length 0002], fatal unrecognized_name'
  fi
  exit 1
fi

if [[ ${1:-} == x509 ]]; then
  allowed=${MOCK_ALLOWED_NAME:-se-edge.itunes.apple.com}
  for ((index = 1; index <= $#; index++)); do
    if [[ ${!index} == -checkhost ]]; then
      host_index=$((index + 1))
      [[ ${MOCK_CERT_MATCHES:-1} == 1 && (${!host_index} == "${allowed}" || ${!host_index} == "www.${allowed}") ]]
      exit
    fi
  done
  if [[ ${MOCK_CHILD_SANS:-0} == 1 ]]; then
    printf '%s\n' 'X509v3 Subject Alternative Name:' "    DNS:${allowed}, DNS:assets.${allowed}, DNS:cdn.${allowed}"
  elif [[ ${allowed} == se-edge.itunes.apple.com ]]; then
    printf '%s\n' \
      'subject=CN=mock.example' \
      'issuer=CN=Mock CA' \
      'X509v3 Subject Alternative Name:' \
      '    DNS:se-edge.itunes.apple.com, DNS:assets.itunes.apple.com, DNS:init.itunes.apple.com, DNS:store.itunes.apple.com'
  else
    printf '%s\n' \
      'subject=CN=mock.example' \
      'issuer=CN=Mock CA' \
      'X509v3 Subject Alternative Name:' \
      "    DNS:${allowed}"
  fi
  exit 0
fi

exit 1
EOF

cat >"${test_tmp}/bin/curl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
header_host=''
url=''
while (($# > 0)); do
  case $1 in
    -H)
      header_host=${2#Host: }
      shift 2
      ;;
    http://*|https://*) url=$1; shift ;;
    *) shift ;;
  esac
done
if [[ ${url} == http://* && -n ${header_host} ]]; then
  allowed=${MOCK_ALLOWED_NAME:-se-edge.itunes.apple.com}
  mode=${MOCK_HTTP_HOST_MODE:-unrestricted}
  if [[ ${header_host,,} == "${allowed,,}" ]]; then
    if [[ ${mode} == baseline-fails ]]; then
      printf '000'
      exit 52
    fi
  else
    case ${mode} in
      restricted) exit 52 ;;
      restricted-reset) exit 56 ;;
      subdomains)
        [[ ${header_host,,} == *."${allowed,,}" ]] || exit 52
        ;;
      timeout) exit 28 ;;
      refused) exit 7 ;;
      mixed)
        [[ ${header_host} == www.cloudflare.com ]] || exit 52
        ;;
      strict-child-leak)
        [[ ${header_host} == "www.${allowed}" ]] || exit 52
        ;;
      mixed-timeout)
        [[ ${header_host} != www.cloudflare.com ]] || exit 28
        exit 52
        ;;
      partial-response)
        printf '403'
        exit 56
        ;;
    esac
  fi
fi
if [[ ${MOCK_HTTP_RESPONDS:-1} != 1 ]]; then
  printf '%s\n' 'curl: (52) Empty reply from server' >&2
  exit 52
fi
printf '400'
EOF
cat >"${test_tmp}/bin/dig" <<'EOF'
#!/usr/bin/env bash
exit 1
EOF
cat >"${test_tmp}/bin/host" <<'EOF'
#!/usr/bin/env bash
exit 1
EOF
chmod +x "${test_tmp}/bin/openssl" "${test_tmp}/bin/curl" "${test_tmp}/bin/dig" "${test_tmp}/bin/host"

run_probe() {
  local bad_sni_responds=$1
  local allowed_sni_responds=$2
  local no_sni_responds=$3
  local http_responds=$4
  local bad_sni_alerts=$5
  local subdomain_sni_responds=$6
  local output_file=$7
  local allowed_name=${8:-se-edge.itunes.apple.com}
  local http_host_mode=${9:-unrestricted}
  set +e
  MOCK_BAD_SNI_RESPONDS=${bad_sni_responds} \
    MOCK_ALLOWED_SNI_RESPONDS=${allowed_sni_responds} \
    MOCK_NO_SNI_RESPONDS=${no_sni_responds} \
    MOCK_HTTP_RESPONDS=${http_responds} \
    MOCK_BAD_SNI_ALERTS=${bad_sni_alerts} \
    MOCK_SUBDOMAIN_SNI_RESPONDS=${subdomain_sni_responds} \
    MOCK_ALLOWED_NAME=${allowed_name} \
    MOCK_HTTP_HOST_MODE=${http_host_mode} \
    PATH="${test_tmp}/bin:${PATH}" \
    "${probe_script}" \
      --host 192.0.2.10 \
      --port 443 \
      --sni "${allowed_name}" \
      --bad-sni www.cloudflare.com >"${output_file}" 2>&1
  probe_status=$?
  set -e
}

success_output="${test_tmp}/success.log"
run_probe 0 1 1 1 1 0 "${success_output}"
[[ ${probe_status} -eq 0 ]]
grep -Fq '✓ SNI 过滤生效' "${success_output}"
grep -Fq 'CDN：未发现明显特征' "${success_output}"
grep -Fq '证书 / TLS SNI 探测' "${success_output}"
grep -Fq 'HTTP 明文与 Host 探测' "${success_output}"
grep -Fq 'HTTPS Host 探测' "${success_output}"
grep -Fq '允许项的子域' "${success_output}"
grep -Fxq '│ 错误 SNI  1 个均未获得证书' "${success_output}"
grep -Fq '严格模式  已开启  -- 仅允许完整 SNI 域名' "${success_output}"
grep -Fq 'SNI 防护  已开启  -- 按 SNI 域名规则限制回落' "${success_output}"
grep -Fq 'HTTP Host 未开启  -- 非匹配 Host 仍收到 HTTP 响应' "${success_output}"
grep -Fxq '╭─ 结论' "${success_output}"
awk '
  /╭─ 探测明细/ { details = NR }
  /╭─ 日志确认/ { logs = NR }
  /╭─ 结论/ { conclusion = NR }
  END {
    if (!(details && logs && conclusion && details < logs && logs < conclusion)) exit 1
  }
' "${success_output}"
grep -Fxq '│ SNI 防护  已开启  -- 按 SNI 域名规则限制回落' "${success_output}"
grep -Fxq '│ 严格模式  已开启  -- 仅允许完整 SNI 域名' "${success_output}"
grep -Fxq '│ HTTP Host 未开启  -- 非匹配 Host 仍收到 HTTP 响应' "${success_output}"
grep -Fxq '╰──────────────────────────────────────────────' "${success_output}"
for closed_border in ╮ ╯ ┌ ┐ └ ┘; do
  if grep -Fq "${closed_border}" "${success_output}"; then
    printf 'information cards must use the native CLI open-edge style\n' >&2
    exit 1
  fi
done
grep -Fq '收到 TLS 数据但未获得证书' "${success_output}"
grep -Fxq '│ 无 SNI    仍获得证书' "${success_output}"
grep -Fq '回落链路保持开放' "${success_output}"
grep -Fxq '╭─ 探测明细' "${success_output}"
grep -Fxq '╭─ 日志确认' "${success_output}"
grep -Fq '错误请求应在本机被丢弃，不应连到配置里的回落网站' "${success_output}"
if grep -Fq 'blackhole' "${success_output}" || grep -Fq '该结果不改变 TLS SNI 判定' "${success_output}"; then
  printf 'summary must stay on information cards\n' >&2
  exit 1
fi
grep -Fq '状态码=400（错误的请求）' "${success_output}"
grep -Fq 'Host: example.com' "${success_output}"
grep -Fq 'Host: www.google.com' "${success_output}"
selected_certificate_host_count=0
for certificate_host in assets.itunes.apple.com init.itunes.apple.com store.itunes.apple.com; do
  if grep -Fq "Host: ${certificate_host}" "${success_output}"; then
    selected_certificate_host_count=$((selected_certificate_host_count + 1))
  fi
done
[[ ${selected_certificate_host_count} -eq 2 ]]
selected_certificate_sni_count=0
for certificate_host in assets.itunes.apple.com init.itunes.apple.com store.itunes.apple.com; do
  if grep -Fq "SNI: ${certificate_host}" "${success_output}"; then
    selected_certificate_sni_count=$((selected_certificate_sni_count + 1))
  fi
done
[[ ${selected_certificate_sni_count} -eq 2 ]]
[[ $(grep -cE '\[[0-9]+/[0-9]+\] 证书 SAN$' "${success_output}") -eq 2 ]]
awk '
  /\[[0-9]+\/[0-9]+\] 无 SNI$/ { seen_no_sni=1 }
  /\[[0-9]+\/[0-9]+\] 证书 SAN$/ { if (!seen_no_sni) exit 1 }
' "${success_output}"
[[ $(grep -Fc '来源: 允许项证书 SAN' "${success_output}") -eq 6 ]]
[[ $(grep -Fc 'HTTP Host 伪装测试（证书 SAN）' "${success_output}") -eq 2 ]]
[[ $(grep -Fc 'HTTPS Host 访问（证书 SAN）' "${success_output}") -eq 2 ]]
grep -Fxq '│ 证书 SAN  均未获得证书' "${success_output}"
grep -Fxq '│ HTTP 附加 8 个收到响应' "${success_output}"
grep -Fxq '│ HTTPS 附加 7 个收到响应' "${success_output}"

color_output="${test_tmp}/color.log"
PROXYFORGE_COLOR=always \
  MOCK_BAD_SNI_RESPONDS=0 MOCK_ALLOWED_SNI_RESPONDS=1 MOCK_NO_SNI_RESPONDS=1 \
  MOCK_HTTP_RESPONDS=1 MOCK_BAD_SNI_ALERTS=1 MOCK_SUBDOMAIN_SNI_RESPONDS=0 \
  PATH="${test_tmp}/bin:${PATH}" \
  "${probe_script}" --host 192.0.2.10 --port 443 --sni se-edge.itunes.apple.com \
  --bad-sni www.cloudflare.com >"${color_output}" 2>&1
grep -Fq $'\033[1;38;5;208m证书 SAN\033[0m' "${color_output}"
grep -Fq $'\033[1;38;5;208m（证书 SAN）\033[0m' "${color_output}"
grep -Fq $'\033[1;38;5;208m╭─ ProxyForge\033[0m' "${color_output}"
grep -Fq $'\033[1;38;5;208m╰──────────────────────────────────────────────\033[0m' "${color_output}"
grep -Fq $'\033[38;5;208m│\033[0m SNI 防护  \033[38;5;208m已开启\033[0m  \033[90m-- 按 SNI 域名规则限制回落\033[0m' "${color_output}"
grep -Fq $'\033[38;5;208m│\033[0m 严格模式  \033[38;5;208m已开启\033[0m  \033[90m-- 仅允许完整 SNI 域名\033[0m' "${color_output}"
grep -Fq $'\033[38;5;208m│\033[0m HTTP Host \033[90m未开启\033[0m  \033[90m-- 非匹配 Host 仍收到 HTTP 响应\033[0m' "${color_output}"
grep -Fq $'\033[38;5;208m│\033[0m 检测结果  \033[1;32m✓ SNI 过滤生效\033[0m' "${color_output}"

plain_output="${test_tmp}/plain-color.log"
NO_COLOR=1 PROXYFORGE_COLOR=never \
  MOCK_BAD_SNI_RESPONDS=0 MOCK_ALLOWED_SNI_RESPONDS=1 MOCK_NO_SNI_RESPONDS=1 \
  MOCK_HTTP_RESPONDS=1 MOCK_BAD_SNI_ALERTS=1 MOCK_SUBDOMAIN_SNI_RESPONDS=0 \
  PATH="${test_tmp}/bin:${PATH}" \
  "${probe_script}" --host 192.0.2.10 --port 443 --sni se-edge.itunes.apple.com \
  --bad-sni www.cloudflare.com >"${plain_output}" 2>&1
if grep -Fq $'\033[' "${plain_output}"; then
  printf 'PROXYFORGE_COLOR=never must disable information card colors\n' >&2
  exit 1
fi
grep -Fxq '│ SNI 防护  已开启  -- 按 SNI 域名规则限制回落' "${plain_output}"

failure_output="${test_tmp}/failure.log"
run_probe 1 1 1 1 0 0 "${failure_output}"
[[ ${probe_status} -eq 1 ]]
grep -Fq '✗ SNI 过滤未生效' "${failure_output}"
grep -Fq '1 个获得了证书' "${failure_output}"
grep -Fq '请核对正在运行的配置，并确认服务已重启' "${failure_output}"

subdomain_loose_output="${test_tmp}/subdomain-loose.log"
run_probe 0 1 1 1 0 1 "${subdomain_loose_output}"
[[ ${probe_status} -eq 0 ]]
grep -Fq '允许项的子域' "${subdomain_loose_output}"
grep -Fq '✓ SNI 过滤生效' "${subdomain_loose_output}"
grep -Fxq '│ 错误 SNI  1 个均未获得证书' "${subdomain_loose_output}"
grep -Fq '严格模式  未开启' "${subdomain_loose_output}"
if grep -Fq '✗ SNI 过滤未生效' "${subdomain_loose_output}"; then
  printf 'subdomain probe must not fail SNI filtering\n' >&2
  exit 1
fi

mismatch_output="${test_tmp}/mismatch.log"
run_probe 0 0 1 1 1 0 "${mismatch_output}"
[[ ${probe_status} -eq 2 ]]
grep -Fq '△ 检测到 SNI 过滤行为' "${mismatch_output}"
grep -Fq '填写的 SNI 可能不是当前允许的回落域名' "${mismatch_output}"
grep -Fq '严格模式  无法确认' "${mismatch_output}"

enhanced_output="${test_tmp}/enhanced.log"
run_probe 0 1 0 0 1 0 "${enhanced_output}"
[[ ${probe_status} -eq 0 ]]
grep -Fq '✓ 增强 SNI 过滤生效' "${enhanced_output}"
grep -Fq '严格模式  已开启' "${enhanced_output}"
grep -Fq 'HTTP Host 无法确认  -- 允许 Host 未收到 HTTP 响应' "${enhanced_output}"
grep -Fxq '│ 无 SNI    未获得证书' "${enhanced_output}"
grep -Fq 'curl退出码=52' "${enhanced_output}"
grep -Fq '具体错误：curl: (52) Empty reply from server' "${enhanced_output}"

unreachable_output="${test_tmp}/unreachable.log"
run_probe 0 0 0 0 0 0 "${unreachable_output}"
[[ ${probe_status} -eq 2 ]]
grep -Fq '? 未获得任何 TLS 证书' "${unreachable_output}"
grep -Fq '端口可能不通，或探测被防火墙丢弃' "${unreachable_output}"
grep -Fq '严格模式  无法确认' "${unreachable_output}"
awk '
  /╭─ 探测明细/ { details = NR }
  /允许项原始输出：/ { raw = NR }
  /╭─ 结论/ { conclusion = NR }
  END {
    if (!(details && raw && conclusion && details < raw && raw < conclusion)) exit 1
  }
' "${unreachable_output}"
grep -Fq 'HTTP Host 无法确认  -- 允许 Host 未收到 HTTP 响应' "${unreachable_output}"

cloudflare_filtered_output="${test_tmp}/cloudflare-filtered.log"
run_probe 0 1 1 1 1 0 "${cloudflare_filtered_output}" speed.cloudflare.com
[[ ${probe_status} -eq 0 ]]
grep -Fq '✓ SNI 过滤生效' "${cloudflare_filtered_output}"
grep -Fq 'CDN：Cloudflare' "${cloudflare_filtered_output}"
grep -Fq '△ 回落仍可能到达 Cloudflare' "${cloudflare_filtered_output}"
if grep -Fq '严重错误' "${cloudflare_filtered_output}"; then
  printf 'filtered Cloudflare must not be a severe error\n' >&2
  exit 1
fi

cloudflare_unfiltered_output="${test_tmp}/cloudflare-unfiltered.log"
run_probe 1 1 1 1 0 0 "${cloudflare_unfiltered_output}" speed.cloudflare.com
[[ ${probe_status} -eq 1 ]]
grep -Fq '✗ SNI 过滤未生效' "${cloudflare_unfiltered_output}"
grep -Fq 'CDN：Cloudflare' "${cloudflare_unfiltered_output}"
grep -Fq '✗ 严重错误：未认证回落可能把流量转到 Cloudflare' "${cloudflare_unfiltered_output}"

akamai_output="${test_tmp}/akamai.log"
run_probe 0 1 1 1 1 0 "${akamai_output}" img.akamaiedge.net
[[ ${probe_status} -eq 0 ]]
grep -Fq 'CDN：Akamai' "${akamai_output}"
grep -Fq '△ 允许 SNI 指向 Akamai' "${akamai_output}"

alicdn_output="${test_tmp}/alicdn.log"
run_probe 0 1 1 1 1 0 "${alicdn_output}" img.alicdn.com
[[ ${probe_status} -eq 0 ]]
grep -Fq 'CDN：阿里云 CDN' "${alicdn_output}"
grep -Fq '△ 允许 SNI 指向 阿里云 CDN' "${alicdn_output}"

for http_mode in restricted restricted-reset mixed strict-child-leak partial-response baseline-fails timeout refused mixed-timeout; do
  http_output="${test_tmp}/http-${http_mode}.log"
  run_probe 0 1 1 1 0 0 "${http_output}" se-edge.itunes.apple.com "${http_mode}"
  # Host findings remain independent of the successful TLS exit status.
  [[ ${probe_status} -eq 0 ]]
  [[ $(grep -Fc '│ SNI 防护' "${http_output}") -eq 1 ]]
  [[ $(grep -Fc '│ 严格模式' "${http_output}") -eq 1 ]]
  [[ $(grep -Fc '│ HTTP Host' "${http_output}") -eq 1 ]]
  case ${http_mode} in
    restricted|restricted-reset)
      grep -Fq 'HTTP Host 已开启  -- 按 SNI 域名规则检查明文 Host' "${http_output}"
      # HTTPS succeeds for unrelated hosts in this mock; it cannot override
      # the independently measured plaintext Host result.
      grep -Fxq '│ HTTPS 附加 7 个收到响应' "${http_output}"
      ;;
    mixed|strict-child-leak|partial-response)
      grep -Fq 'HTTP Host 未开启  -- 非匹配 Host 仍收到 HTTP 响应' "${http_output}"
      ;;
    baseline-fails)
      grep -Fq 'HTTP Host 无法确认  -- 允许 Host 未收到 HTTP 响应' "${http_output}"
      ;;
    timeout|refused|mixed-timeout)
      grep -Fq 'HTTP Host 无法确认  -- 非匹配 Host 探测超时或出错' "${http_output}"
      ;;
  esac
done

child_sans_output="${test_tmp}/http-child-sans.log"
MOCK_CHILD_SANS=1 run_probe 0 1 1 1 0 1 "${child_sans_output}" se-edge.itunes.apple.com subdomains
[[ ${probe_status} -eq 0 ]]
grep -Fq '严格模式  未开启' "${child_sans_output}"
grep -Fq 'HTTP Host 已开启' "${child_sans_output}"
grep -Fq 'Host: assets.se-edge.itunes.apple.com' "${child_sans_output}"
grep -Fq 'Host: cdn.se-edge.itunes.apple.com' "${child_sans_output}"
grep -Fxq '│ HTTP 附加 5 个收到响应' "${child_sans_output}"

certificate_mismatch_output="${test_tmp}/certificate-mismatch.log"
MOCK_CERT_MATCHES=0 run_probe 0 1 1 1 0 0 "${certificate_mismatch_output}" se-edge.itunes.apple.com restricted
[[ ${probe_status} -eq 2 ]]
grep -Fq 'SNI 防护  无法确认' "${certificate_mismatch_output}"
grep -Fq '严格模式  无法确认' "${certificate_mismatch_output}"
grep -Fq 'HTTP Host 已开启' "${certificate_mismatch_output}"

printf 'test-reality-sni: ok\n'
