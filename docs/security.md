# 文件与安全边界

## 受管文件

| 用途 | sing-box | Xray |
|---|---|---|
| 服务端配置 | `/etc/sing-box/config.json` | `/usr/local/etc/xray/config.json` |
| 状态 | `/var/lib/proxyforge/state/sing-box.json` | `/var/lib/proxyforge/state/xray.json` |
| systemd unit | `sing-box.service` | `xray.service` |

信任记录位于 `/var/lib/proxyforge/trust/`，备份位于 `/var/lib/proxyforge/backups/<core>/<timestamp>/`。每个内核只保留最近 3 份 ProxyForge 时间戳备份。

状态、信任和备份都是 root-only。服务配置会根据 systemd unit 的实际 `User=` 设置为 root 私有或 root 所有、服务组只读，不会设为世界可读。

## 配置事务

配置更新先写入临时文件，运行 `sing-box check` 或 `xray run -test`，再原子替换目标文件。ProxyForge 只重启目标服务，并确认其 active 状态和监听端口。

任一步失败都会恢复该内核的旧配置和状态，并重启旧服务，不触碰另一个内核。

## 网络目标校验

ProxyForge 会验证 REALITY target 的 DNS、TCP/TLS、证书名称和地址属性，拒绝本机、私网及保留地址。使用 CDN 目标可能把未认证回落流量转发到第三方基础设施，需要自行评估。

生成的服务端和客户端配置会拒绝访问 IPv4/IPv6 私网、本机、链路本地、CGNAT、云元数据、基准测试、多播和保留地址。域名目标会先解析为 IP 再匹配黑洞规则；Xray Freedom 出站固定使用 IP 解析策略。

ProxyForge 只提示 ufw/firewalld 所需的 TCP 端口，不会自行修改防火墙。

## 下载与执行边界

安装和更新流程会验证 HTTPS 来源、重定向、HTTP 状态、文件大小、文本格式、shebang、Bash 语法和 SHA-256。首次执行或脚本内容发生变化时需要重新确认信任。更完整的操作说明见[安装、升级与卸载](installation.md)。

## 官方参考

- [sing-box 安装](https://sing-box.sagernet.org/installation/package-manager/)
- [sing-box VLESS](https://sing-box.sagernet.org/configuration/inbound/vless/)
- [sing-box REALITY TLS](https://sing-box.sagernet.org/configuration/shared/tls/)
- [Xray-install](https://github.com/XTLS/Xray-install)
- [Xray VLESS](https://xtls.github.io/en/config/inbounds/vless.html)
- [XTLS REALITY](https://github.com/XTLS/REALITY/blob/main/README.en.md)
