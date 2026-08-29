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
  shift
  while (($# > 0)); do
    if [[ $1 == -servername ]]; then
      server_name=${2:-}
      break
    fi
    shift
  done

  if [[ (-z ${server_name} && ${MOCK_NO_SNI_RESPONDS:-1} == 1) ||
        (${server_name} == se-edge.itunes.apple.com && ${MOCK_ALLOWED_SNI_RESPONDS:-1} == 1) ||
        (${server_name} == www.se-edge.itunes.apple.com && ${MOCK_SUBDOMAIN_SNI_RESPONDS:-0} == 1) ||
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
  for ((index = 1; index <= $#; index++)); do
    if [[ ${!index} == -checkhost ]]; then
      host_index=$((index + 1))
      [[ ${!host_index} == se-edge.itunes.apple.com || ${!host_index} == www.se-edge.itunes.apple.com ]]
      exit
    fi
  done
  printf '%s\n' \
    'subject=CN=mock.example' \
    'issuer=CN=Mock CA' \
    'X509v3 Subject Alternative Name:' \
    '    DNS:se-edge.itunes.apple.com, DNS:assets.itunes.apple.com, DNS:init.itunes.apple.com, DNS:store.itunes.apple.com'
  exit 0
fi

exit 1
EOF

cat >"${test_tmp}/bin/curl" <<'EOF'
#!/usr/bin/env bash
if [[ ${MOCK_HTTP_RESPONDS:-1} != 1 ]]; then
  printf '%s\n' 'curl: (52) Empty reply from server' >&2
  exit 52
fi
printf '400'
EOF
chmod +x "${test_tmp}/bin/openssl" "${test_tmp}/bin/curl"

run_probe() {
  local bad_sni_responds=$1
  local allowed_sni_responds=$2
  local no_sni_responds=$3
  local http_responds=$4
  local bad_sni_alerts=$5
  local subdomain_sni_responds=$6
  local output_file=$7
  set +e
  MOCK_BAD_SNI_RESPONDS=${bad_sni_responds} \
    MOCK_ALLOWED_SNI_RESPONDS=${allowed_sni_responds} \
    MOCK_NO_SNI_RESPONDS=${no_sni_responds} \
    MOCK_HTTP_RESPONDS=${http_responds} \
    MOCK_BAD_SNI_ALERTS=${bad_sni_alerts} \
    MOCK_SUBDOMAIN_SNI_RESPONDS=${subdomain_sni_responds} \
    PATH="${test_tmp}/bin:${PATH}" \
    "${probe_script}" \
      --host 192.0.2.10 \
      --port 443 \
      --sni se-edge.itunes.apple.com \
      --bad-sni www.cloudflare.com >"${output_file}" 2>&1
  probe_status=$?
  set -e
}

success_output="${test_tmp}/success.log"
run_probe 0 1 1 1 1 0 "${success_output}"
[[ ${probe_status} -eq 0 ]]
grep -Fq '✓ SNI 过滤生效' "${success_output}"
grep -Fq '证书 / TLS SNI 探测' "${success_output}"
grep -Fq 'HTTP 明文与 Host 探测' "${success_output}"
grep -Fq 'HTTPS Host 探测' "${success_output}"
grep -Fq '允许项的子域名（应拒绝）' "${success_output}"
grep -Fq '2 个应拒绝 SNI（含允许项子域名）均未获得证书' "${success_output}"
grep -Fq '收到 TLS 数据但未获得证书' "${success_output}"
grep -Fq '无 SNI 探测仍获得了证书' "${success_output}"
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
[[ $(grep -Fc '来源: 允许项证书 SAN' "${success_output}") -eq 4 ]]
grep -Fq 'HTTP 附加探测中有 7 个请求收到了响应' "${success_output}"
grep -Fq 'HTTPS Host 附加探测中有 6 个请求收到了响应' "${success_output}"

failure_output="${test_tmp}/failure.log"
run_probe 1 1 1 1 0 0 "${failure_output}"
[[ ${probe_status} -eq 1 ]]
grep -Fq '✗ SNI 过滤未生效' "${failure_output}"
grep -Fq '1 个错误 SNI 获得了 TLS 证书' "${failure_output}"

subdomain_failure_output="${test_tmp}/subdomain-failure.log"
run_probe 0 1 1 1 0 1 "${subdomain_failure_output}"
[[ ${probe_status} -eq 1 ]]
grep -Fq '允许项的子域名（应拒绝）' "${subdomain_failure_output}"
grep -Fq '✗ SNI 过滤未生效' "${subdomain_failure_output}"
grep -Fq '1 个错误 SNI 获得了 TLS 证书' "${subdomain_failure_output}"
grep -Fq '允许项子域名 www.se-edge.itunes.apple.com 被放行' "${subdomain_failure_output}"

mismatch_output="${test_tmp}/mismatch.log"
run_probe 0 0 1 1 1 0 "${mismatch_output}"
[[ ${probe_status} -eq 2 ]]
grep -Fq '△ 检测到 SNI 过滤行为' "${mismatch_output}"
grep -Fq '填写的 SNI 不是当前允许回落域名' "${mismatch_output}"

enhanced_output="${test_tmp}/enhanced.log"
run_probe 0 1 0 0 1 0 "${enhanced_output}"
[[ ${probe_status} -eq 0 ]]
grep -Fq '✓ 增强 SNI 过滤生效' "${enhanced_output}"
grep -Fq '无 SNI 访问已被增强过滤拒绝' "${enhanced_output}"
grep -Fq 'curl退出码=52' "${enhanced_output}"
grep -Fq '具体错误：curl: (52) Empty reply from server' "${enhanced_output}"

unreachable_output="${test_tmp}/unreachable.log"
run_probe 0 0 0 0 0 0 "${unreachable_output}"
[[ ${probe_status} -eq 2 ]]
grep -Fq '? 未获得任何 TLS 证书' "${unreachable_output}"
grep -Fq '节点端口可能无法访问' "${unreachable_output}"

printf 'test-reality-sni: ok\n'
