# REALITY SNI 检测指南

`scripts/test-reality-sni.sh` 用于从节点外部黑盒检查 REALITY 的 SNI 回落过滤。它不需要 UUID、REALITY 公钥或 short ID，也不会读取或修改服务端配置。

## 基本用法

请在节点服务器之外运行，并确保本机已安装 Bash、OpenSSL、GNU `timeout` 和 curl：

```bash
./scripts/test-reality-sni.sh \
  --host 192.0.2.10 \
  --port 443 \
  --sni se-edge.itunes.apple.com
```

参数：

- `--host HOST`：节点 IP 或域名。
- `--port PORT`：REALITY 公网监听端口。
- `--sni DOMAIN`：预期允许的回落域名。
- `--bad-sni DOMAIN`：预期拒绝的域名，可以重复传入；默认测试 `www.cloudflare.com` 和 `example.com`。
- `--timeout SECONDS`：单次 TLS/HTTP 探测超时，默认 8 秒。
- `--verbose`：显示 OpenSSL 和 HTTP 探测的原始输出。

没有项目源码时可以单独下载脚本：

```bash
wget --https-only \
  -O ~/proxyforge-test-reality-sni.sh \
  https://raw.githubusercontent.com/JokerKernel/proxyforge/main/scripts/test-reality-sni.sh

chmod +x ~/proxyforge-test-reality-sni.sh
~/proxyforge-test-reality-sni.sh \
  --host YOUR_SERVER_IP \
  --port 443 \
  --sni YOUR_ALLOWED_SNI
```

## 探测内容

默认执行十项探测（域名重复时会自动去重）：

1. 使用允许 SNI 建立 TLS 连接，并检查证书 SAN 是否匹配。
2. 使用 `proxyforge-test.<允许 SNI>` 检查允许项的子域名是否也被错误放行。
3. 使用 `www.cloudflare.com` 作为错误 SNI。
4. 使用 `example.com` 作为错误 SNI。
5. 不发送 SNI 建立 TLS 连接。
6. 向 REALITY 端口发送普通 HTTP 请求。
7. 使用允许域名作为 HTTP `Host` 请求头。
8. 使用错误域名作为 HTTP `Host` 请求头。
9. 使用 `example.com` 作为 HTTP `Host` 请求头。
10. 使用 `www.google.com` 作为 HTTP `Host` 请求头。

前五项决定 TLS SNI 结论，后五项仅报告明文 HTTP 暴露面，不改变 TLS SNI 判定。允许项的子域名会按错误 SNI 处理：若它获得证书，脚本返回过滤未生效。

## TLS 判定

“允许项成功”必须同时满足：收到 TLS 证书，并且证书 SAN 匹配 `--sni` 指定的域名。TLS Alert、连接关闭或其他没有证书的响应都不算放行。

| 允许 SNI | 错误 SNI | 无 SNI | 结论 |
|---|---|---|---|
| 匹配证书 | 均无证书 | 无证书 | 增强 SNI 过滤生效，无 SNI 也被拒绝 |
| 匹配证书 | 均无证书 | 有证书 | SNI 过滤生效，但无 SNI 回落仍开放 |
| 无证书 | 均无证书 | 有证书 | 检测到 SNI 过滤行为，但填写的允许域名可能与服务端配置不一致 |
| 无证书 | 均无证书 | 无证书 | 无法确认；端口可能不可访问，也可能所有探测均被过滤 |
| 任意 | 任一有证书 | 任意 | 错误 SNI 被放行，过滤未生效或不完整 |

如果允许项收到证书但 SAN 不匹配，脚本也会返回“无法确认”，需要检查 REALITY target 和允许域名。

## HTTP 状态

HTTP 请求没有 TLS SNI，因此 HTTP 探测只作为附加信息：

- `2xx（请求成功）`：请求已被处理。
- `3xx（重定向）`：服务端要求访问其他位置。
- `400（错误的请求）`：端口收到了明文 HTTP，但认为请求或协议不正确。
- 其他 `4xx（客户端请求错误）`：服务端拒绝或无法处理请求。
- `5xx（服务端错误）`：请求到达服务端，但服务端处理失败。

未收到 HTTP 响应时，脚本会同时显示 curl 退出码和原始错误信息，例如 `curl: (52) Empty reply from server`。

例如 REALITY/TLS 端口返回 `400（错误的请求）`，通常说明端口可以连接，但明文 HTTP 不符合它预期的 TLS 协议。这不代表代理认证成功，也不代表错误 SNI 被放行。

## 退出码

| 退出码 | 含义 |
|---|---|
| `0` | 普通或增强 SNI 过滤生效 |
| `1` | 至少一个错误 SNI 获得了 TLS 证书 |
| `2` | 无法确认，或填写的允许 SNI 可能与服务端配置不一致 |
| `64` | 命令参数错误 |

HTTP 附加探测收到响应不会单独改变退出码。

## 排障与限制

黑盒检测只能证明观察到的网络行为。目标站自身也可能拒绝错误 SNI，从而造成过滤生效的假象；建议同时查看内核日志，确认错误请求命中了 `blackhole`、`blocked-private` 或 `reject`，而不是连接到真实 target：

```bash
sudo journalctl -u xray -f -o cat
sudo journalctl -u sing-box -f -o cat
```

所有 TLS 探测都没有证书时，优先检查：

1. 节点 IP、端口和防火墙是否正确。
2. Xray 或 sing-box 服务是否已启动并监听该端口。
3. `--sni` 是否与当前服务端配置一致。
4. REALITY target 是否可从服务器正常完成 TLS 握手。
