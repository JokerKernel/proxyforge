# 文件与安全边界

## 受管文件

| 用途 | sing-box | Xray |
| --- | --- | --- |
| 服务端配置 | `/etc/sing-box/config.json` | `/usr/local/etc/xray/config.json` |
| 状态 | `/var/lib/proxyforge/state/sing-box.json` | `/var/lib/proxyforge/state/xray.json` |
| 访问控制 | `/var/lib/proxyforge/access/sing-box.json` | `/var/lib/proxyforge/access/xray.json` |
| 落地接入 | `/var/lib/proxyforge/landings/sing-box.json` | `/var/lib/proxyforge/landings/xray.json` |
| 中转线路 | `/var/lib/proxyforge/relays/sing-box.json` | `/var/lib/proxyforge/relays/xray.json` |
| 安装渠道 | `/var/lib/proxyforge/preferences/sing-box.json` | `/var/lib/proxyforge/preferences/xray.json` |
| TLS 证书 | `/var/lib/proxyforge/tls/sing-box/<名称>/` | `/var/lib/proxyforge/tls/xray/<名称>/` |
| systemd 服务 | `sing-box.service` | `xray.service` |

信任记录位于 `/var/lib/proxyforge/trust/`；备份位于 `/var/lib/proxyforge/backups/<内核>/<时间戳>/`，每个内核保留最近 3 份时间戳备份。

状态、名单、线路、安装偏好、信任及备份仅允许 root 访问。配置和 TLS 私钥由 root 所有，根据服务用户设置为 root 私有或允许服务组读取，不设为世界可读。

名单和线路通过独立 JSON 文件保存，旧状态文件中的内嵌记录不自动读取或迁移。手动修改文件前应保留备份；字段、规则或权限无效时会拒绝应用。

## 配置事务

配置变更先生成临时文件，执行 `sing-box check` 或 `xray run -test`，再原子替换目标文件。完整生成会启动并检查目标服务及监听端口；DNS、IP 策略和日志设置仅在服务已运行时重启。

应用失败时尝试恢复原配置和相关状态；恢复失败会报告错误，不能将失败提示视为恢复成功。配置文件恢复失败时不继续重启服务。单文件原子替换不等于多文件更新具有断电恢复保证。

新证书在配置和状态应用成功后成为受管证书；失败时清理本次生成的文件。主节点证书轮换保留旧文件供备份引用。

卸载及清理范围见[安装指南](installation.md#清理卸载残留)。历史 `/var/lib/proxyforge/docker/` 由 ProxyDock 管理，原生清理不会删除它。

## 网络目标校验

REALITY target 会检查 DNS、TCP/TLS、证书名称和地址属性，拒绝本机、私网及保留地址。使用 CDN 目标可能将未认证回落流量转发到第三方，SNI 检测结果不能替代对目标的评估。

生成配置默认拦截私网、本机、链路本地、CGNAT、云元数据、多播及其他保留目标；明确的访问白名单可放行对应目标。sing-box 简化模式的解析限制见[配置模式](configuration.md#配置模式)。

白名单匹配代理请求的实际目标，伪造 HTTP Host/TLS SNI 不会获得域名许可。访问控制只控制代理流量，REALITY 回落由独立规则限制。ProxyForge 不修改主机防火墙或云安全组。

自签 TLS 使用证书或公钥 SHA-256 固定验证，信任依赖首次安全传递的客户端配置或连接文本，不能改为跳过证书验证。

## 下载与执行边界

程序下载管理脚本时校验 HTTPS 主机白名单、重定向、HTTP 状态、大小、文本格式、shebang、Bash 语法和 SHA-256，再写入受限临时文件执行。内核官方脚本首次执行或内容变化后需重新确认信任，非交互操作必须固定哈希，详见[安装指南](installation.md#安装或升级代理内核)。

官方脚本输出实时转发，保留最近 64 KiB 用于失败诊断；ProxyForge 命令日志隐藏代理凭据，不记录密钥生成结果。
