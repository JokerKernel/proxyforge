# 配置与日常使用

无参数运行 `sudo proxyforge` 会进入中文数字菜单，内核选择中 `1` 为 Xray-core（直接回车时的默认项）、`2` 为 sing-box，再管理所选内核的安装、配置、客户端、凭据、服务和卸载。

## CLI 概览

```text
proxyforge install <sing-box|xray> [--version VERSION]
proxyforge update [--yes]
proxyforge uninstall <sing-box|xray> [--yes]
proxyforge cleanup <sing-box|xray|all> [--yes]
proxyforge config generate <sing-box|xray> --server HOST --port PORT --sni DOMAIN [OPTIONS]
proxyforge config client <sing-box|xray> [--format native|clash] [--output FILE] [--force]
proxyforge config reset <sing-box|xray> [--sni DOMAIN] [--target HOST:PORT] [--yes]
proxyforge service <sing-box|xray> <start|stop|restart|status|logs>
```

使用 `proxyforge <command> --help` 查看完整参数。

## 生成服务端配置

```bash
sudo proxyforge config generate sing-box \
  --yes --server 203.0.113.10 --port 443 \
  --sni www.example.com --user-name phone --inbound-tag phone-in

sudo proxyforge config generate xray \
  --yes --server 203.0.113.10 --port 8443 \
  --sni www.example.com --user-name laptop --inbound-tag laptop-in
```

生成配置前会检查内核二进制和 systemd unit。首个节点默认建议端口 443；检测到另一个受管节点后默认建议 8443。只有“生成/更新服务端配置”会完整覆盖现有文件，非交互覆盖必须指定 `--yes`；写入前仍会备份。

服务端用户名默认是 `one`，入站标签默认是 `singbox-one` 或 `xray-one`，可以分别通过 `--user-name` 和 `--inbound-tag` 修改。交互生成过程中的任意输入步骤都可输入 `q` 取消，不会写入配置或重启服务。

### 配置模式

sing-box 和 Xray 默认生成回落防偷跑配置。可用 `--standard-config` 恢复标准模板；sing-box 还支持 `--simplified-config`。这些模式参数不能组合。

- sing-box 防偷跑模式在 `127.0.0.1` 创建 `direct` 入站，将 REALITY handshake 指向该入站，通过 TLS sniff 和域名规则把合法回落转到独立的 `fallback-direct`（默认双栈）；内部端口默认 61432。
- sing-box 的明文 HTTP 回落默认不限制 Host。使用 `--sing-box-fallback-http-domain`（或交互菜单中的对应开关）后，HTTP 规则才会写入当前 SNI 的域名限制。
- Xray 防偷跑模式在 `127.0.0.1` 创建 `dokodemo-door` 入站，将 REALITY target 指向该入站，匹配 `serverNames` 的流量走独立的 `fallback-direct`（默认双栈），其余进入 blackhole；内部端口默认 61431。
- `--sing-box-fallback-port` 和 `--xray-fallback-port` 可修改内部端口。端口不能与公网监听端口或另一个受管节点冲突。
- 原有的 `--sing-box-fallback-guard` 和 `--xray-fallback-guard` 参数继续兼容，但默认模式不需要显式提供。

sing-box 简化配置不启用内部 DNS 和路由预解析，改由出站连接使用系统 DNS。日志更少，但域名解析到私网地址时可能绕过路由私网拦截。

### SNI 与 REALITY target

SNI 留空时，程序会并发验证内置候选域名的 DNS、TCP/TLS、证书名称和延迟，展示最快的 10 个。手动输入的 SNI 会执行相同检查，并显示详细证书 SAN。

已经生成节点后，可在交互菜单进入“服务端配置 → 修改配置 → REALITY SNI 候选检测”重新测试。程序会同时复测当前 SNI 和全部内置候选，显示当前 SNI 是否仍然有效、在有效候选中的排名及最快的 10 个结果。结果页可以直接选择“重新测试”连续复测，或返回服务端配置；检测只读取节点状态和网络结果，不修改 SNI、target 或服务状态。

CDN 识别只基于 CNAME、域名和地址数量进行启发式判断，不代表权威归属；测速也只反映当前网络。最终 SNI 和 target 需要人工确认。

交互生成时可从已启用的物理网卡选择公网单播 IP，也可使用 `api.ipify.org` HTTPS 探测或手动输入。程序拒绝把内网、NAT 和保留地址自动写成节点公网地址。

## 重置节点

`config reset` 保留地址和端口，可修改 SNI 和 target；只指定新 SNI 时，target 默认变为 `<新 SNI>:443`。重置会同步更新 REALITY 配置、真实回落目标和已启用的路由放行域名，同时保留当前配置模式、HTTP 回落策略和内部回落端口。

### Xray 专用运行用户

XTLS 官方安装脚本首次安装时默认在 systemd unit 中写入 `User=nobody`，较新的 systemd 会报告 `Special user nobody configured, this is not safe!`。这通常不影响启动，但 `nobody` 是多个程序可共用的特殊账号，不适合作为长期服务身份。

在交互菜单进入“Xray → 服务端配置 → 专用运行用户”，ProxyForge 会写入 `/usr/lib/sysusers.d/proxyforge-xray.conf`，通过 `systemd-sysusers` 创建禁止登录且不创建 home 目录的独立 `xray` 系统用户和组，再更新 `xray.service` 与 `xray@.service`、写入持久化 drop-in，并同步配置及日志权限。切换前会以 `xray` 身份校验配置及引用文件的读取权限；若服务正在运行，修改完成后会自动重启。任一步骤失败会恢复原 systemd unit 和文件权限；已经创建的专用账号会保留，避免删除仍可能拥有文件的系统账号。

普通重新生成会保留 UUID、REALITY 密钥和 short ID。只有使用 `--rotate-credentials` 或执行凭据重置时才会轮换它们，并让旧客户端失效。

定点重置会保留 DNS、路由、出站、日志、其他用户及手动配置；找不到唯一受管入站或用户时会拒绝修改。修改前会备份，失败时自动回滚。

## 导出客户端

```bash
sudo proxyforge config client sing-box --output ./sing-box-client.json
sudo proxyforge config client xray --output ./xray-client.json
sudo proxyforge config client sing-box --format clash --output ./clash.yaml
```

客户端文件以 `0600` 创建。默认的 `native` 格式会通过对应内核校验：sing-box 客户端提供 `127.0.0.1:2080` mixed 入站；Xray 客户端提供 `127.0.0.1:10808` SOCKS 和 `127.0.0.1:10809` HTTP 入站。

`clash` 格式输出完整的 Mihomo/Clash Meta YAML，包含 `mixed-port: 7890`、`PROXY` 策略组和 `MATCH` 规则。传统 Clash 不支持 VLESS REALITY，不能使用该文件。

## DNS 设置

“服务端配置 → 修改配置 → DNS 设置”支持系统 DNS（推荐）、Cloudflare/Google 明文 DNS 和 DoH。所有选项只影响代理内核，不修改系统全局 DNS。

- Xray 的明文 DNS 和 DoH 都写入两家服务器并按所选顺序回退；DoH 使用 IP 形式的 `https+local` 地址。
- sing-box 只写入所选的一个公共上游；DoH 额外保留系统 DNS 用于引导解析，并同步更新 `dns.final`、`route.default_domain_resolver` 和所有 `resolve` 规则。
- Xray 默认显式使用系统 DNS、`UseIP` 查询策略和 `IPOnDemand` 路由解析。

修改前会运行内核原生校验，随后备份并原子写入。服务运行时才会重启；失败会恢复旧配置。

## 出站 IP

“服务端配置 → 修改配置 → 出站 IP”只改用户代理走的 `direct` 出站，不改 DNS 服务器列表，也不改 REALITY 回落访问目标站的地址族。

- 优先 IPv4 / 优先 IPv6：Xray 写入 Freedom `UseIPv4v6` / `UseIPv6v4`（先解析优先族，解析不到再试另一族；已解析出优先族后连接失败不会回退）。sing-box 给 `resolve` 规则加上 `prefer_ipv4` / `prefer_ipv6`（可连接回退）；简化配置没有该规则时会补一条同样的 `resolve`。
- 仅 IPv4 / 仅 IPv6：Xray 写入 `ForceIPv4` / `ForceIPv6`；sing-box 同上写入 `ipv4_only` / `ipv6_only`。对端只有另一地址族时会失败。
- 回落：防偷跑配置把合法 SNI 回落指到独立的 `fallback-direct`，始终保持默认双栈。给已有配置设置出站 IP 时，若回落还挂在 `direct` 上，会自动拆开。
- 恢复默认：Xray 写回 Freedom `UseIP`；sing-box 去掉这些 `strategy`。标准配置会保留原来的 `resolve` 规则和本地 DNS；简化配置会撤掉补上的 `resolve` 规则，以及仅为它添加的本地 DNS。
- 生成配置的默认状态是未设置（双栈）。重置 SNI/凭证会保留此项；完整生成会覆盖。

## 菜单、日志与输出

真实终端中的菜单会自动清屏并使用固定语义颜色。管道和文件重定向会自动关闭颜色；也可设置 `NO_COLOR=1`、`PROXYFORGE_COLOR=never` 或 `PROXYFORGE_COLOR=always`。

服务管理菜单可以持续查看 systemd journal，按 `Ctrl+C` 只停止日志并返回菜单。日志级别修改同样会先备份、校验并在需要时重启服务。sing-box 支持 `trace/debug/info/warn/error/fatal/panic/关闭`，Xray 支持 `debug/info/warning/error/关闭`。

输出前缀用于区分 ProxyForge 流程、本机命令、官方脚本和服务日志。步骤与命令日志写入 stderr，客户端配置写入 stdout 或 `--output` 文件；密钥生成结果不会写入命令日志。
