# REALITY SNI 检测指南

`scripts/test-reality-sni.sh` 从节点外部检查 REALITY 的 SNI 回落过滤，不需要 UUID、公钥或 short ID，也不读取或修改服务端配置。

## 基本用法

在节点服务器之外运行，需要 Bash、OpenSSL、GNU `timeout` 和 curl：

```bash
./scripts/test-reality-sni.sh \
  --host YOUR_SERVER_IP --port 443 --sni YOUR_ALLOWED_SNI
```

没有源码时可单独下载脚本：

```bash
wget --https-only -O ~/proxyforge-test-reality-sni.sh \
  https://raw.githubusercontent.com/JokerKernel/proxyforge/main/scripts/test-reality-sni.sh
bash ~/proxyforge-test-reality-sni.sh \
  --host YOUR_SERVER_IP --port 443 --sni YOUR_ALLOWED_SNI
```

| 参数 | 说明 |
| --- | --- |
| `--host HOST` | 节点 IP 或域名 |
| `--port PORT` | REALITY 公网监听端口 |
| `--sni DOMAIN` | 预期允许的回落域名 |
| `--bad-sni DOMAIN` | 预期拒绝的域名，可重复；默认测试 `www.cloudflare.com` 和 `example.com` |
| `--timeout SECONDS` | 单次探测超时，默认 8 秒，范围 1–60 秒 |
| `--verbose` | 显示原始探测输出 |

## 探测内容

- **TLS 主判定**：允许 SNI、错误 SNI 和无 SNI，检查是否获得证书及允许项 SAN 是否匹配。
- **附加 TLS 检查**：允许域名的 `www.` 子域及证书 SAN 中的其他域名，用于观察严格匹配和目标站行为。
- **HTTP/HTTPS 检查**：普通 HTTP、允许/错误 Host 及 SAN 域名，仅作为附加报告。

额外域名去重并排除通配符；SAN 域名不足时使用内置域名补足，报告注明来源。附加检查不改变主判定。

CDN 提示根据域名、CNAME 等特征推断，不能视为权威归属。识别为 Cloudflare 且错误 SNI 获得证书时，会特别提示流量风险。

## TLS 判定

允许项成功必须同时满足“收到 TLS 证书”和“证书 SAN 匹配指定 SNI”；TLS Alert 或直接断连不算放行。

| 允许 SNI | 错误 SNI | 无 SNI | 结论 |
| --- | --- | --- | --- |
| 匹配证书 | 均无证书 | 无证书 | 增强过滤生效，无 SNI 被拒绝 |
| 匹配证书 | 均无证书 | 有证书 | 过滤生效，但无 SNI 回落开放 |
| 无证书 | 均无证书 | 有证书 | 观察到过滤，允许域名可能与配置不一致 |
| 无证书 | 均无证书 | 无证书 | 无法确认，检查端口可达性及配置 |
| 任意 | 任一有证书 | 任意 | 错误 SNI 放行，过滤不完整 |

允许项证书 SAN 不匹配时也为“无法确认”。子域名严格匹配单独报告；允许项未成功时无法判断。其他 SAN 域名取得证书不会单独判定过滤失败。

HTTP 响应（包括 `400`）只说明请求获得了响应，不代表代理认证成功或错误 SNI 被放行；未收到响应时查看报告中的 curl 退出码和错误信息。

## 退出码

| 退出码 | 含义 |
| --- | --- |
| `0` | 普通或增强 SNI 过滤生效 |
| `1` | 至少一个错误 SNI 获得证书 |
| `2` | 无法确认或允许 SNI 可能有误 |
| `64` | 参数错误 |

HTTP 附加结果和 CDN 提示不单独改变退出码。

## 排障与限制

目标站自身也可能拒绝错误 SNI，造成过滤生效的假象。结合内核日志确认请求被拦截，而非转发到真实 target：

```bash
sudo journalctl -u xray -f -o cat
sudo journalctl -u sing-box -f -o cat
```

所有 TLS 探测均无证书时，依次检查节点 IP/端口和防火墙、服务运行状态、SNI 与配置是否一致，以及服务器到 REALITY target 的 TLS 连通性。
