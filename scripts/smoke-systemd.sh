#!/usr/bin/env bash
set -euo pipefail

: "${PROXYFORGE_SMOKE_SERVER:?set the public server address}"
: "${PROXYFORGE_SMOKE_SNI:?set a verified REALITY SNI}"
: "${PROXYFORGE_SING_SCRIPT_SHA256:?set the reviewed sing-box installer SHA-256}"
: "${PROXYFORGE_XRAY_SCRIPT_SHA256:?set the reviewed Xray installer SHA-256}"

if [[ ${EUID} -ne 0 ]]; then
  echo "run this smoke test as root" >&2
  exit 1
fi

proxyforge_bin=${PROXYFORGE_BIN:-./proxyforge}
target=${PROXYFORGE_SMOKE_TARGET:-${PROXYFORGE_SMOKE_SNI}:443}
smoke_tmp=$(mktemp -d /tmp/proxyforge-smoke.XXXXXX)
trap 'rm -rf -- "${smoke_tmp}"' EXIT

"${proxyforge_bin}" install sing-box --yes --trust-script-sha256 "${PROXYFORGE_SING_SCRIPT_SHA256}"
"${proxyforge_bin}" install xray --yes --trust-script-sha256 "${PROXYFORGE_XRAY_SCRIPT_SHA256}"

"${proxyforge_bin}" config generate sing-box --yes --take-over \
  --server "${PROXYFORGE_SMOKE_SERVER}" --port 443 --sni "${PROXYFORGE_SMOKE_SNI}" --target "${target}"
"${proxyforge_bin}" config generate xray --yes --take-over \
  --server "${PROXYFORGE_SMOKE_SERVER}" --port 8443 --sni "${PROXYFORGE_SMOKE_SNI}" --target "${target}"

"${proxyforge_bin}" config client sing-box --output "${smoke_tmp}/sing-box-client.json"
"${proxyforge_bin}" config client xray --output "${smoke_tmp}/xray-client.json"

sing-box check -c "${smoke_tmp}/sing-box-client.json"
xray run -test -config "${smoke_tmp}/xray-client.json"
systemctl is-active --quiet sing-box.service
systemctl is-active --quiet xray.service

echo "ProxyForge systemd smoke test passed"
