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
proxyforge config landing add <sing-box|xray> NAME [--security reality|tls] [TLS OPTIONS]
proxyforge config relay add <sing-box|xray> NAME --upstream-stdin
proxyforge config relay client <sing-box|xray> NAME [--format native|clash] [--output FILE]
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

通过“服务端配置 → 编辑配置”保存并退出编辑器后，ProxyForge 会使用对应内核的原生命令校验当前配置。校验通过时自动重启服务使配置生效；校验失败时显示具体错误，不会重启服务，按 Enter 后会重新打开同一配置文件继续修改。

### 配置模式

sing-box 和 Xray 默认生成回落防偷跑配置。可用 `--standard-config` 恢复标准模板；sing-box 还支持 `--simplified-config`。这些模式参数不能组合。

- sing-box 防偷跑模式在 `127.0.0.1` 创建 `direct` 入站，将 REALITY handshake 指向该入站，通过 TLS sniff 和域名规则把合法回落转到独立的 `fallback-direct`（默认双栈）；内部端口默认在 30000-65000 间随机分配，已有配置则沿用。
- sing-box 和 Xray 的明文 HTTP 回落默认不限制 Host。分别使用 `--sing-box-fallback-http-domain`、`--xray-fallback-http-domain`（或交互菜单中的对应开关）后，才会按当前 SNI 限制 HTTP Host。
- Xray 防偷跑模式在 `127.0.0.1` 创建 `dokodemo-door` 入站，将 REALITY target 指向该入站。HTTP Host 不受限时沿用 TLS sniff 和单条域名放行规则；要求 Host 匹配 SNI 时改用 TLS/HTTP sniff，并为两种协议分别写入域名规则。其余流量进入 blackhole，内部端口默认在 30000-65000 间随机分配，已有配置则沿用。
- sing-box 的域名匹配方式是独立选项。交互菜单为「1 允许子域名（默认）」或「2 严格匹配」；默认写入 `domain_suffix`，选择严格匹配或使用 `--sing-box-fallback-exact-domain` 后改写为 `domain` 完整匹配，不受 HTTP Host 策略影响。
- Xray 的域名匹配方式同样是独立选项，菜单文案与 sing-box 一致。默认写入普通的 `<SNI>`；选择严格匹配或使用 `--xray-fallback-exact-domain` 后改写为 `full:<SNI>`，不受 HTTP Host 策略影响。
- `--sing-box-fallback-port` 和 `--xray-fallback-port` 可修改内部端口。端口不能与公网监听端口或另一个受管节点冲突。
- 原有的 `--sing-box-fallback-guard` 和 `--xray-fallback-guard` 参数继续兼容，但默认模式不需要显式提供。

sing-box 简化配置不启用内部 DNS 和路由预解析，改由出站连接使用系统 DNS。日志更少，但域名解析到私网地址时可能绕过路由私网拦截。

### SNI 与 REALITY target

SNI 留空时，若当前节点已有 REALITY SNI，会先询问是否继续使用。不使用或尚无配置时，程序会并发验证内置候选域名的 DNS、TCP/TLS、证书名称，并分别测量 IPv4 与 IPv6 延迟，默认按 IPv4 延迟排序展示全部有效结果，每页 20 个。可用 `v` 切换 IPv4/IPv6 排序，或输入 `v4`/`v6` 指定；n/p 翻页、g/G 到首尾页，或输入 p3 跳到指定页。选择时只能输入当前页上的编号。手动输入的 SNI 会执行相同检查，并显示详细证书 SAN。

已经生成节点后，可在交互菜单进入“服务端配置 → 修改配置 → REALITY SNI 候选检测”重新测试。程序会同时复测当前 SNI 和全部内置候选，显示当前 SNI 是否仍然有效、在全部有效结果中的排名，并分页列出全部候选。结果页可以直接选择“重新测试”连续复测，或返回服务端配置；检测只读取节点状态和网络结果，不修改 SNI、target 或服务状态。

CDN 识别只基于 CNAME、域名和地址数量进行启发式判断，不代表权威归属；测速也只反映当前网络。最终 SNI 和 target 需要人工确认。

交互生成时可从已启用的物理网卡选择公网单播 IP，也可使用 `api.ipify.org` HTTPS 探测或手动输入。程序拒绝把内网、NAT 和保留地址自动写成节点公网地址。

## 重置节点

`config reset` 保留地址和端口，可修改 SNI 和 target；只指定新 SNI 时，target 默认变为 `<新 SNI>:443`。重置会同步更新 REALITY 配置、真实回落目标和已启用的路由放行域名，同时保留当前配置模式、HTTP 回落策略和内部回落端口。

### Xray 专用运行用户

XTLS 官方安装脚本首次安装时默认在 systemd unit 中写入 `User=nobody`，较新的 systemd 会报告 `Special user nobody configured, this is not safe!`。这通常不影响启动，但 `nobody` 是多个程序可共用的特殊账号，不适合作为长期服务身份。

在交互菜单进入“Xray → 服务端配置 → 专用运行用户”，ProxyForge 会写入 `/usr/lib/sysusers.d/proxyforge-xray.conf`，通过 `systemd-sysusers` 创建禁止登录且不创建 home 目录的独立 `xray` 系统用户和组，再更新 `xray.service` 与 `xray@.service`、写入持久化 drop-in，并同步配置及日志权限。切换前会以 `xray` 身份校验配置及引用文件的读取权限；若服务正在运行，修改完成后会自动重启。任一步骤失败会恢复原 systemd unit 和文件权限；已经创建的专用账号会保留并记录 UID/GID，之后卸载 Xray 时仅在身份未变化的情况下安全删除。预先存在或没有所有权标记的账号不会被删除。

普通重新生成会保留 UUID、REALITY 密钥和 short ID。只有使用 `--rotate-credentials` 或执行凭据重置时才会轮换它们，并让旧客户端失效。

完整重新生成默认同时保留 ProxyForge 管理的中转线路和落地接入；只有显式使用 `--drop-links` 才会清除这些附加用户、出站和路由。

定点重置会保留 DNS、路由、出站、日志、其他用户及手动配置；找不到唯一受管入站或用户时会拒绝修改。修改前会备份，失败时自动回滚。

## 导出客户端

```bash
sudo proxyforge config client sing-box --output ./sing-box-client.json
sudo proxyforge config client xray --output ./xray-client.json
sudo proxyforge config client sing-box --format clash --output ./clash.yaml
```

客户端文件以 `0600` 创建。默认的 `native` 格式会通过对应内核校验：sing-box 客户端提供 `127.0.0.1:2080` mixed 入站；Xray 客户端提供 `127.0.0.1:10808` SOCKS 和 `127.0.0.1:10809` HTTP 入站。

`clash` 格式输出完整的 Mihomo/Clash Meta YAML，包含 `mixed-port: 7890`、`PROXY` 策略组和 `MATCH` 规则。传统 Clash 不支持 VLESS REALITY，不能使用该文件。

交互菜单的“客户端配置”同时提供普通节点和中转节点入口。选择中转节点后会列出当前线路及启用状态，再选择原生 JSON 或 Clash YAML，配置内容直接显示在终端。

## 中转与落地

“服务端配置 → 中转与落地配置”按 VLESS 用户身份选择出口。普通用户继续使用现有 `direct`，每条中转线路使用独立 UUID 并固定连接指定落地；落地不可用时不会回退本机出口。

中转线路名称、客户端用户名、内部出站 tag 和路由引用完全一致。例如创建线路 `cs`，这些位置都使用 `cs`，不会生成 `proxyforge-relay-cs`。旧前缀配置不会自动迁移，需要删除旧线路后重新创建。

落地接入采用相同规则：输入名称 `cs` 后，落地名称、接入用户名以及独立 TLS 入站 tag 都是 `cs`，不会添加 `proxyforge-landing-` 前缀。

创建前会检查名称格式和可用性：名称限 1–32 个字母、数字、下划线或连字符，且必须以字母或数字开头；中转线路与落地接入之间不允许重名（忽略大小写），也不能与普通用户、主入站 tag 或 `direct` 等系统保留名称冲突。若手动编辑的现有配置中已经存在同名 tag，应用配置时也会拒绝覆盖。

先在落地服务器创建独立接入。交互菜单会提供两种模式：

- 使用当前协议：把落地用户加入当前 `VLESS + RAW + REALITY + Vision` 入站，不新增端口。
- 创建独立 TLS：新增一个 `VLESS + RAW + TLS + Vision` 入站，默认从 `30000–65000` 随机选择可用端口，并使用独立证书域名、证书链和私钥。交互时可以修改随机结果。

命令会把一段可复制的 JSON 连接文本直接显示在终端，不会默认生成文件。默认模式是复用当前 REALITY：

```bash
sudo proxyforge config landing add xray from-relay
```

纯命令行创建独立 TLS 落地的示例：

```bash
sudo proxyforge config landing add xray from-relay-tls \
  --security tls \
  --server-name exit.example.com \
  --cert-file /etc/letsencrypt/live/exit.example.com/fullchain.pem \
  --key-file /etc/letsencrypt/live/exit.example.com/privkey.pem
```

TLS 模式要求证书和私钥文件已存在、证书在有效期内且 SAN 与 `--server-name` 匹配；中转机按系统 CA 验证证书，不会自动启用跳过验证。证书路径只保存在落地服务器本地状态和内核配置里，不会写进连接文本。还需确保内核运行用户可读取证书文件，并在防火墙中放行所选 TCP 端口。

命令行使用 `--security tls` 时可以省略 `--port`，此时同样会在 `30000–65000` 中随机选择当前可用且未被 ProxyForge 管理的端口。

复制完整 JSON 文本。在中转服务器的交互菜单选择“添加中转线路”，程序会打开一个临时编辑文件；粘贴后保存并退出即可。临时文件权限为 `0600`，导入完成后会自动删除。

使用纯命令行时，通过标准输入粘贴文本，粘贴完成后按 `Ctrl+D`：

```bash
sudo proxyforge config relay add sing-box us \
  --upstream-stdin

sudo proxyforge config relay client sing-box us \
  --format clash --output ./us-client.yaml
```

连接文本包含安全协议、地址、端口、UUID 和 SNI；REALITY 模式还包含公钥与 short ID。连接文本不含 REALITY 服务端私钥、TLS 私钥或证书文件路径，但其中 UUID 仍是敏感凭据。支持 Xray 与 sing-box 两端任意组合，两种模式都使用 VLESS + Vision、RAW 传输。

`landing export`（别名 `landing show`）用于再次显示连接文本，`relay update --upstream-stdin` 用于粘贴更新。为兼容已有脚本，仍保留落地命令的 `--output FILE` 和中转命令的 `--upstream FILE`；新流程无需使用这两个文件参数。

交互管理中转线路和落地接入时，程序会按 `1/2/3...` 显示列表。输入编号选择对应项目，不需要再次手动输入线路名称。

常用管理命令：

```bash
sudo proxyforge config landing list xray
sudo proxyforge config landing show xray from-relay
sudo proxyforge config landing disable xray from-relay
sudo proxyforge config landing rotate xray from-relay --yes
sudo proxyforge config landing remove xray from-relay --yes

sudo proxyforge config relay list sing-box
sudo proxyforge config relay test sing-box us
sudo proxyforge config relay update sing-box us --upstream-stdin
sudo proxyforge config relay disable sing-box us
sudo proxyforge config relay rotate sing-box us --yes
sudo proxyforge config relay remove sing-box us --yes
```

创建和更新中转线路时会先检查落地 TCP 端口。落地地址默认要求公网单播地址；为了局域网联调，允许直接使用 `192.168.0.0/16`，但 `10.0.0.0/8`、`172.16.0.0/12`、回环和其他保留地址仍会拒绝。该放行只适用于连接落地服务器，用户代理访问私网的拦截规则不变。确知落地暂时不可达但仍需保存时，可以显式添加 `--allow-unreachable`。每次线路变更都会生成候选配置、调用对应内核原生命令校验、备份并重启当前服务；失败时恢复原配置和状态。停用线路会从入站移除对应用户，而不是让它落入默认 `direct`。

重置本节点 SNI、REALITY 密钥或 short ID 后，已经导出的本节点客户端需要重新导出，落地连接文本需要重新生成并粘贴到中转机。轮换某条中转线路 UUID 只影响该线路的客户端；轮换落地接入 UUID 后，需要重新生成连接文本并更新所有使用它的中转机。

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
- 回落：防偷跑配置把合法 SNI 回落指到独立的 `fallback-direct`。给已有配置设置出站 IP 时，若回落还挂在 `direct` 上，会自动拆开。
- 恢复默认：Xray 写回 Freedom `AsIs`，并在 sockopt 打开 `UseIP` + `happyEyeballs`（先 IPv4，300ms 后竞速 IPv6，与 sing-box 默认一致）；`direct` 若还没有 `finalRules`，会补一条空的 `allow`，避免 VLESS 内置拦私网先把域名解析成单个 IP。优先/仅 IPv4·IPv6 会删掉这条空 `allow`（手写的带 `ip`/`port` 的规则保留）。sing-box 去掉这些 `strategy`。标准配置会保留原来的 `resolve` 规则和本地 DNS；简化配置会撤掉补上的 `resolve` 规则，以及仅为它添加的本地 DNS。
- 生成配置的默认状态是未设置（双栈）。Xray 默认先 IPv4 再竞速 IPv6（`direct` 带空 `allow`；`fallback-direct` 不需要）；sing-box 同样先 IPv4。重置 SNI/凭证会保留此项；完整生成会覆盖。

## 回落 IP

仅当当前节点启用了回落防偷跑时，“服务端配置 → 修改配置 → 回落 IP”才出现。它只改 `fallback-direct`，不影响用户代理出站。

- 优先 IPv4 / 优先 IPv6：Xray 写入回落 Freedom `UseIPv4v6` / `UseIPv6v4`。sing-box 给 `fallback-direct` 的 `domain_resolver` 加上 `prefer_ipv4` / `prefer_ipv6`。
- 仅 IPv4 / 仅 IPv6：Xray 写入 `ForceIPv4` / `ForceIPv6`；sing-box 写入 `ipv4_only` / `ipv6_only`。
- 恢复默认：回落回到生成时的双栈。Xray 与用户出站相同，先 IPv4、300ms 后竞速 IPv6；sing-box 去掉 `fallback-direct` 的 `strategy`。

## 菜单、日志与输出

真实终端中的菜单会自动清屏并使用固定语义颜色。管道和文件重定向会自动关闭颜色；也可设置 `NO_COLOR=1`、`PROXYFORGE_COLOR=never` 或 `PROXYFORGE_COLOR=always`。

“服务端配置 → 日志级别”可直接修改内核日志级别，会先备份、校验并在需要时重启服务。sing-box 支持 `trace/debug/info/warn/error/fatal/panic/关闭`，Xray 支持 `debug/info/warning/error/关闭`。

“服务端配置 → 服务管理”菜单可以启动、停止、重启服务，查看状态或持续查看 systemd journal；按 `Ctrl+C` 只停止实时日志并返回菜单。

输出前缀用于区分 ProxyForge 流程、本机命令、官方脚本和服务日志。步骤与命令日志写入 stderr，客户端配置写入 stdout 或 `--output` 文件；密钥生成结果不会写入命令日志。
