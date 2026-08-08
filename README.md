# ProxyForge

ProxyForge 是一个面向 Linux/systemd 的 Go 单二进制管理器，用来在同一台服务器上彼此隔离地管理一个 sing-box 和一个 Xray-core `VLESS + REALITY + Vision` 节点。两个内核拥有独立的配置、凭据、端口、状态和 systemd 服务，可以同时运行。

首版支持 Debian/Ubuntu 与 RHEL/CentOS/Rocky/AlmaLinux/Fedora 系发行版的 amd64、arm64。修改系统的命令必须由 root 执行；PID 1 不是 systemd 时会拒绝安装。

## 构建

```bash
go build -trimpath -ldflags "-s -w" -o proxyforge ./cmd/proxyforge
go test ./...
```

无参数运行会进入中文数字菜单：

```bash
sudo ./proxyforge
```

## CLI

```text
proxyforge install <sing-box|xray> [--version VERSION]
proxyforge upgrade <sing-box|xray> [--version VERSION]
proxyforge config generate <sing-box|xray> --server HOST --port PORT --sni DOMAIN [--target HOST:PORT]
proxyforge config client <sing-box|xray> [--output FILE] [--force]
proxyforge service <sing-box|xray> <start|stop|restart|status|logs>
```

非交互安装必须显式固定本次下载内容，`--yes` 不能跳过：

```bash
sudo proxyforge install sing-box --yes --trust-script-sha256 <64位哈希>
```

首次交互安装会展示来源、最终重定向地址、大小、危险操作摘要和 SHA-256，只有输入完整的 `yes` 才执行。脚本变化后，非交互执行会被阻止，必须重新进行交互确认。安装器下载受限临时文件，不使用 `curl | sh`，并检查 HTTPS 主机白名单、重定向、HTTP 状态、大小、shebang、NUL/文本格式和 `bash -n`。

生成首个节点时交互默认端口为 443；检测到另一个受管节点后默认建议 8443。重新生成会保留 UUID、REALITY 密钥和 short ID；只有 `--rotate-credentials` 会轮换它们并让旧客户端失效。未知或被外部修改的配置必须交互确认，自动化时使用 `--take-over`。

示例：

```bash
sudo proxyforge config generate sing-box \
  --yes --server 203.0.113.10 --port 443 --sni www.example.com

sudo proxyforge config generate xray \
  --yes --server 203.0.113.10 --port 8443 --sni www.example.com

proxyforge config client sing-box --output ./sing-box-client.json
proxyforge config client xray --output ./xray-client.json
```

客户端文件以 `0600` 创建；stdout 输出前也会调用对应内核的原生配置校验。sing-box 客户端提供 `127.0.0.1:2080` mixed 入站，Xray 客户端提供 `127.0.0.1:10808` SOCKS 与 `127.0.0.1:10809` HTTP 入站。

## 文件与安全边界

| 用途 | sing-box | Xray |
|---|---|---|
| 服务端配置 | `/etc/sing-box/config.json` | `/usr/local/etc/xray/config.json` |
| 状态 | `/var/lib/proxyforge/state/sing-box.json` | `/var/lib/proxyforge/state/xray.json` |
| systemd unit | `sing-box.service` | `xray.service` |

信任记录位于 `/var/lib/proxyforge/trust/`，接管前备份位于 `/var/lib/proxyforge/backups/<core>/<timestamp>/`。状态、信任和备份为 root-only；服务配置按 unit 的实际 `User=` 设置为 root 私有或 root 所有、服务组只读，绝不设为世界可读。

配置更新先写临时文件并运行 `sing-box check` 或 `xray run -test`，再原子替换、只重启目标服务并确认 active/监听状态。任一步失败都会恢复该内核的旧配置和状态并重启旧服务，不触碰另一个内核。

ProxyForge 会验证 REALITY target 的 DNS、TCP/TLS、证书名称和地址属性，拒绝本机、私网及保留地址。使用 CDN 目标可能把未认证的回落流量转发给第三方，必须自行评估。工具只提示 ufw/firewalld 所需 TCP 端口，不会修改防火墙。

官方参考：[sing-box 安装](https://sing-box.sagernet.org/installation/package-manager/)、[sing-box VLESS](https://sing-box.sagernet.org/configuration/inbound/vless/)、[sing-box REALITY TLS](https://sing-box.sagernet.org/configuration/shared/tls/)、[Xray-install](https://github.com/XTLS/Xray-install)、[Xray VLESS](https://xtls.github.io/en/config/inbounds/vless.html)、[XTLS REALITY](https://github.com/XTLS/REALITY/blob/main/README.en.md)。

## 验收测试

golden、安全与事务回滚测试由 `go test ./...` 执行。提供官方二进制后可复跑四份配置的原生验收：

```bash
SING_BOX_BIN=/path/to/sing-box XRAY_BIN=/path/to/xray \
  go test -v ./internal/integration
```

在一次性 Debian/Ubuntu 或 Rocky/AlmaLinux systemd VM 中，可设置脚本要求的公网地址、SNI 和两个已人工核对的安装脚本哈希，再以 root 运行 `scripts/smoke-systemd.sh`。它会真实安装两个内核、生成 443/8443 节点、验证两个客户端并确认两项服务同时 active，因此不应在生产主机上直接运行。
