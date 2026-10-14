# 配置与日常使用

无参数运行 `sudo proxyforge` 会进入中文数字菜单，内核选择中 `1` 为 Xray-core（直接回车时的默认项）、`2` 为 sing-box，再管理所选内核的安装、配置、客户端、凭据、服务和卸载。

## CLI 概览

```text
proxyforge install <sing-box|xray> [--version VERSION] [--beta]
proxyforge update [--yes]
proxyforge uninstall <sing-box|xray> [--yes]
proxyforge cleanup <sing-box|xray|all> [--yes]
proxyforge config generate <sing-box|xray> --server HOST --port PORT [--security reality|tls] [OPTIONS]
proxyforge config client <sing-box|xray> [--format native|clash] [--output FILE] [--force]
proxyforge config reset <sing-box|xray> [--sni DOMAIN] [--target HOST:PORT] [--yes]
proxyforge config landing add <sing-box|xray> NAME [--security current|reality|tls]
proxyforge config relay add <sing-box|xray> NAME --upstream-stdin
proxyforge config relay client <sing-box|xray> NAME [--format native|clash] [--output FILE]
proxyforge config access list <sing-box|xray>
proxyforge config access mode <sing-box|xray> <default|whitelist>
proxyforge config access enable <sing-box|xray>
proxyforge config access disable <sing-box|xray>
proxyforge config access add <sing-box|xray> <white|black> TARGET [TARGET...] [--exact]
proxyforge config access remove <sing-box|xray> <white|black> TARGET [--exact]
proxyforge config access clear <sing-box|xray> <white|black>
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

### 自签 TLS 主节点

“生成服务端配置”提供 `VLESS + RAW + REALITY + Vision` 和 `VLESS + RAW + TLS + Vision` 两个选项。新节点默认 REALITY；重新生成时默认沿用当前协议。每个内核仍管理一个主节点，切换协议时自动更换主节点 UUID，切换回来也会再次生成新的 UUID，无需额外指定 `--rotate-credentials`。成功应用后需要重新导出客户端配置，以及复用主入站的落地连接文本。落地和中转用户各自的 UUID 保留；切换失败会恢复原协议、UUID 和配置。

TLS 模式仅需连接地址和监听端口，无需准备域名、证书或 DNS：

```bash
sudo proxyforge config generate xray --yes \
  --security tls --server 203.0.113.10 --port 443

sudo proxyforge config generate sing-box --yes \
  --security tls --server 203.0.113.10 --port 8443
```

程序自动生成 10 年有效的 ECDSA P-256 自签证书，证书名称为随机 `pf-<十六进制>.invalid`，服务端最低 TLS 版本为 1.3。客户端连接用户提供的 IP 或域名，TLS 名称和固定指纹由导出配置自动填写，不需要解析该随机名称。TLS 模式不接受 `--sni`、`--target` 或 REALITY 回落防护参数。

保持 TLS 协议重新生成配置时，默认保留 UUID 和证书。UUID 和证书可以分别重置：

```bash
# 重置普通用户 UUID，保留证书
sudo proxyforge config reset xray --yes

# 重新生成证书及 TLS 名称，保留 UUID
sudo proxyforge config reset xray --yes --rotate-certificate
```

菜单“修改配置”也提供这两个独立操作。生成命令的 `--rotate-credentials` 在 TLS 模式仅轮换 UUID；`--rotate-certificate` 重新生成证书。更换证书后，普通客户端、中转线路客户端及复用主入站的落地连接文本都需要重新导出。

旧主节点证书文件在更换证书或切换到 REALITY 后继续保留，供旧配置备份使用；内核卸载或清理时统一删除。首次生成失败或证书更换失败会清理本次新证书，并保留旧证书。

### 配置模式

在 REALITY 模式下，sing-box 和 Xray 默认生成回落防偷跑配置。可用 `--standard-config` 恢复标准模板；sing-box 还支持 `--simplified-config`。这些模式参数不能组合。

两个内核的内部回落入站统一使用 `fallback-in`，回落出站使用 `fallback-out`；Xray 的原有拦截出站使用 `private-block`。这些标签不兼容旧名称，已有配置需要重新生成。

- sing-box 防偷跑模式在 `127.0.0.1` 创建 `direct` 入站，将 REALITY handshake 指向该入站，通过 TLS sniff 和域名规则把合法回落转到独立的 `fallback-out`（默认双栈）；内部端口默认在 30000-65000 间随机分配，已有配置则沿用。
- sing-box 和 Xray 的明文 HTTP 回落默认不限制 Host。分别使用 `--sing-box-fallback-http-domain`、`--xray-fallback-http-domain`（或交互菜单中的对应开关）后，才会按当前 SNI 限制 HTTP Host。
- Xray 防偷跑模式在 `127.0.0.1` 创建 `tunnel` 入站，将 REALITY target 指向该入站。HTTP Host 不受限时沿用 TLS sniff 和单条域名放行规则；要求 Host 匹配 SNI 时改用 TLS/HTTP sniff，并为两种协议分别写入域名规则。其余流量进入 blackhole，内部端口默认在 30000-65000 间随机分配，已有配置则沿用。
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

保持 REALITY 协议重新生成时，默认保留 UUID、REALITY 密钥和 short ID。使用 `--rotate-credentials` 或执行凭据重置时会轮换它们；从 TLS 切换到 REALITY 时也会生成新的 UUID、REALITY 密钥和 short ID，旧客户端需要更新。

完整重新生成默认同时保留 ProxyForge 管理的中转线路和落地接入；只有显式使用 `--drop-links` 才会清除这些附加用户、出站和路由。

定点重置会保留 DNS、路由、出站、日志、其他用户及手动配置；找不到唯一受管入站或用户时会拒绝修改。修改前会备份，失败时自动回滚。

## 导出客户端

```bash
sudo proxyforge config client sing-box --output ./sing-box-client.json
sudo proxyforge config client xray --output ./xray-client.json
sudo proxyforge config client sing-box --format clash --output ./clash.yaml
```

客户端文件以 `0600` 创建。默认的 `native` 格式会通过对应内核校验：sing-box 客户端提供 `127.0.0.1:2080` mixed 入站；Xray 客户端提供 `127.0.0.1:10808` SOCKS 和 `127.0.0.1:10809` HTTP 入站。

`clash` 格式输出完整的 Mihomo/Clash Meta YAML，包含 `mixed-port: 7890`、`PROXY` 策略组和 `MATCH` 规则。请使用支持 VLESS 的 Mihomo/Clash Meta，传统 Clash 不能使用该文件。

自签 TLS 导出保持证书验证开启：Xray 和 Mihomo 固定证书 SHA-256，sing-box 固定公钥 SHA-256（需要 sing-box 1.13 或更高版本）。导出前会检查受管证书、私钥、有效期、名称和指纹是否一致。客户端配置需通过可信方式传递，不能将指纹替换为跳过证书验证。

交互菜单的“客户端配置”同时提供普通节点和中转节点入口。选择中转节点后会列出当前线路及启用状态，再选择原生 JSON 或 Clash YAML，配置内容直接显示在终端。

## 访问控制

“服务端配置 → 修改配置 → 访问控制”提供四个入口：「1 切换访问模式」「2 白名单管理」「3 黑名单管理」「4 关闭访问控制／启用访问控制」。顶部信息卡显示开关状态、模式及当前实际规则顺序，名单管理入口显示规则数量，完整名单在对应子菜单中展示。

访问控制主菜单中的第 4 项是独立开关，按当前状态显示「关闭访问控制」或「启用访问控制」。「切换访问模式」只提供默认模式和仅允许白名单模式。关闭时保留模式和两份名单，移除生成的访问控制规则，执行内核原有拦截；再次启用时按保存的设置恢复规则。关闭期间修改模式或名单仅保存设置，不会自动启用。

白名单和黑名单管理采用相同布局：「1 添加规则」「2 删除规则」「3 清空名单」。清空白名单时，当前为仅允许白名单模式则同时切回默认模式，影响会在清空确认页说明；清空黑名单保留访问模式。清空其中一份名单会保留另一份名单和总开关状态，执行前显示变更后的策略并确认。各级菜单及确认页均支持 `0/q` 直接返回，取消修改不要求额外回车。策略按内核独立管理，适用于该内核的普通用户、复用主入站的落地用户、独立 TLS 落地接入和中转线路；REALITY 未认证回落的 SNI/Host 防护继续使用原来的规则。

「添加规则」支持在一行中输入多个域名、IP 和 CIDR，使用空格、英文或中文逗号分隔。例如 `baidu.com, example.com 192.168.1.0/24`。本次所有域名统一选择包括子域名或精确匹配，IP 和网段不受该选择影响；命令行批量添加时 `--exact` 同样只影响域名。所有目标先校验、标准化和去重，再统一预览并一次性应用；任一目标无效时整批不添加。关闭访问控制时批量添加也保持关闭状态。

总开关、访问模式、白名单和黑名单保存在独立配置文件中：Xray 使用 `/var/lib/proxyforge/access/xray.json`，sing-box 使用 `/var/lib/proxyforge/access/sing-box.json`。`disabled: true` 表示关闭；默认是 `false`，即启用。文件权限为 `0600`，目录权限为 `0700`。节点状态文件不再保存 `access_policy`，也不读取或迁移旧字段。独立文件不存在时使用默认模式、启用开关和空名单；空名单下仍执行原有拦截。文件损坏、规则无效或权限不安全时会报错。

文件格式如下，两份名单为空时也保留数组：

```json
{
  "disabled": false,
  "mode": "default",
  "whitelist": [
    { "type": "domain", "value": "example.com", "include_subdomains": true }
  ],
  "blacklist": [
    { "type": "cidr", "value": "192.168.1.0/24" }
  ]
}
```

菜单或命令行修改会同步更新内核运行配置。手动编辑独立文件后，需要重新生成服务端配置才能应用；也可以在首次生成节点前准备此文件。配置更新失败时保持原有访问控制文件，清理或卸载对应内核时会一并删除。

规则优先级固定如下：

1. 命中白名单立即放行；普通用户及落地用户走当前 `direct`，中转用户继续走该线路指定的落地。
2. 默认模式下，命中黑名单拒绝。
3. 现有私网、保留地址等拦截规则兜底。
4. 默认模式最终放行；仅允许白名单模式最终拒绝。

白名单优先于黑名单和现有私网拦截。明确将 `192.168.1.0/24` 加入白名单后，该网段可以访问；其他未允许的私网仍拦截。内网地址必须能从最终出口访问到。白名单为空时，`whitelist` 模式拒绝全部代理目标；黑名单为空时，默认模式保留原有拦截行为。

仅保留 `default` 和 `whitelist` 两种模式。两份名单独立保存，切换模式不会清空：白名单在两个模式中都优先生效，黑名单在 `default` 模式中生效；`whitelist` 模式只允许白名单，其余拒绝。信息卡按模式和名单内容显示生效的规则顺序，空名单不显示对应匹配步骤。配置重新生成、凭据重置和中转/落地增删也会保留并重新应用策略。

不接受或自动转换已移除的 `blacklist` 模式；已有访问控制文件若使用此模式，需手动将 `mode` 改为 `default`，两份名单保持原内容。

Xray 的域名和 IP/网段规则分别使用 `white-domain`、`white-ip`、`black-domain`、`black-ip` 作为 `ruleTag`。其他访问控制辅助标签使用 `access-` 前缀，例如 `access-final`、`access-scope` 和 `access-dns`。不兼容旧的 `pf-access-` 标签，使用旧标签的配置需要重新生成。

```bash
# 域名及全部子域名：example.com、www.example.com、a.b.example.com
sudo proxyforge config access add xray white example.com --yes

# 一次添加多个目标；逗号分隔也支持
sudo proxyforge config access add xray white baidu.com example.com 192.168.1.0/24 --yes
sudo proxyforge config access add xray black 'denied.example.com,192.168.2.0/24' --yes

# 只匹配这个完整域名
sudo proxyforge config access add xray white api.example.com --exact --yes

# 单个 IP 和网段；192.168.1.0 不会自动代表整个网段
sudo proxyforge config access add xray white 192.168.1.10 --yes
sudo proxyforge config access add xray white 192.168.1.0/24 --yes

sudo proxyforge config access add xray black denied.example.com --yes
sudo proxyforge config access mode xray whitelist --yes
sudo proxyforge config access mode xray default --yes
sudo proxyforge config access list xray

# 保留模式和名单，临时关闭；随后恢复启用
sudo proxyforge config access disable xray --yes
sudo proxyforge config access enable xray --yes

# 删除时使用相同的匹配类型；精确域名需带 --exact
sudo proxyforge config access remove xray white api.example.com --exact --yes

# 清空白名单，同时退出仅允许白名单模式；或清空黑名单
sudo proxyforge config access clear xray white --yes
sudo proxyforge config access clear xray black --yes
```

把 `xray` 替换为 `sing-box` 可管理另一个内核。IPv6 地址和 IPv6 CIDR 同样支持。输入域名时不要包含协议、路径、端口或通配符；默认已经包含全部子域名。单 IP 会标准化，CIDR 会转换为网络地址，重复条目不会重复添加。

域名匹配使用代理请求提供的实际目标名称。直接按 IP 访问时，需要命中对应 IP/网段规则；仅伪造 HTTP Host 或 TLS SNI 不会获得域名白名单放行。Xray 在访问策略生效期间停用受管用户入站的内容嗅探，策略不再生效时恢复原设置；REALITY 回落防护的内部嗅探不受影响。域名黑名单不等于阻止该站点所有 IP，按 IP 访问需要另行加入 IP/网段黑名单。

修改前使用内核校验配置并备份，失败时回滚配置和状态。策略修改会重启并启用当前内核服务。原生客户端导出会在原有私网拦截前添加白名单例外；如果需要访问新允许的私网目标，请重新导出原生客户端配置。Mihomo YAML 继续将目标送到代理，由服务端执行访问策略。

## 中转与落地

“服务端配置 → 中转与落地配置”按 VLESS 用户身份选择出口。普通用户继续使用现有 `direct`，每条中转线路使用独立 UUID 并固定连接指定落地；落地不可用时不会回退本机出口。

粘贴落地连接文本时，每次编辑使用独立的 `0700` 临时目录，JSON 文件权限为 `0600`。保存并退出编辑器后，程序读取连接文本并删除整个本次编辑目录，包括编辑器生成的备份和交换文件；编辑失败、空文本或解析失败也会清理。删除失败会显示残留路径，不影响已经解析成功的连接信息。

下次使用粘贴导入时，会检查同一临时目录位置、同一用户的遗留编辑目录，清理已无进程持锁的目录。正在使用的目录通过文件锁保护，锁也会传给编辑器进程。最后一个编辑目录清理后，空的外层管理目录也会删除；其他编辑目录或文件仍存在时会保留外层目录。共享临时目录和其他文件会保留。

落地接入和中转线路分别保存在独立文件中：

| 内容 | Xray | sing-box |
|---|---|---|
| 落地接入 | `/var/lib/proxyforge/landings/xray.json` | `/var/lib/proxyforge/landings/sing-box.json` |
| 中转线路 | `/var/lib/proxyforge/relays/xray.json` | `/var/lib/proxyforge/relays/sing-box.json` |

文件内容为 JSON 数组，每条记录保留完整字段；没有记录时写入 `[]`，文件不存在时按空列表处理。落地记录包括名称、用户名、UUID、协议、端口、SNI、证书路径、指纹、启用状态和更新时间；中转记录包括名称、用户凭据、完整远程落地信息、启用状态和更新时间。证书和私钥实体仍在 `/var/lib/proxyforge/tls/`，主节点证书记录仍属于主节点状态。

文件权限为 `0600`，目录权限为 `0700`。节点状态文件不再保存 `landing_accesses` 和 `relay_links`，不读取或迁移旧字段。菜单和命令行修改会同步应用到内核配置；手动编辑独立文件后，重新生成服务端配置才能应用。首次生成节点前也可以准备这些文件，`--drop-links` 会明确清空两份列表。

节点状态、访问控制、落地和中转文件统一提交，后续写入失败时恢复已经写入的文件。服务校验或重启失败时保留原记录；清理或卸载对应内核时，一并删除独立记录及受管证书。

中转线路名称和客户端用户名使用用户输入的原名；内部 tag 使用简短的用途后缀，便于查看配置时区分。例如创建线路 `cs`，用户名为 `cs`，出站 tag 与路由引用为 `cs-out`，不会生成 `proxyforge-relay-cs`。旧前缀配置不会自动迁移，需要删除旧线路后重新创建。

管理中转线路按“序号、用户名、tag 值、访问地址和端口、启用状态”的顺序显示，不额外显示“用户”或“tag”标签，例如 `1  cs · cs-out · 192.168.1.20:443  [已启用]`；详情页会同时列出线路名称、客户端用户名和出站 tag。

落地接入输入名称 `cs` 后，落地名称和接入用户名为 `cs`，独立 TLS 入站 tag 为 `cs-in`，不会添加 `proxyforge-landing-` 前缀。

创建前会检查名称格式和可用性：名称限 1–32 个字母、数字、下划线或连字符，且必须以字母或数字开头；中转线路与落地接入之间不允许重名（忽略大小写），也不能与普通用户、主入站 tag 或 `direct` 等系统保留名称冲突。若手动编辑的现有配置中已经存在同名 tag，应用配置时也会拒绝覆盖。

先在落地服务器创建独立接入。交互菜单会提供两种模式：

- 使用当前协议：把落地用户加入当前 REALITY 或 TLS 主入站，不新增端口。TLS 主入站使用主节点证书和指纹。
- 创建独立 TLS：新增一个 `VLESS + RAW + TLS + Vision` 入站，从 `30000–65000` 随机选择可用端口，并自动生成 ECDSA P-256 自签证书。SNI 和 SAN 使用随机的 `pf-<十六进制>.invalid`，不依赖 DNS。

命令会把一段可复制的 JSON 连接文本直接显示在终端，不会默认生成文件。默认 `--security current` 复用当前主入站；`--security reality` 仅适用于 REALITY 主节点：

```bash
sudo proxyforge config landing add xray from-relay
```

纯命令行创建独立 TLS 落地的示例：

```bash
sudo proxyforge config landing add xray from-relay-tls --security tls
```

TLS 模式的证书有效期为 10 年，保存在 `/var/lib/proxyforge/tls/<core>/<接入名>/cert.pem` 和 `key.pem`。ProxyForge 会按内核运行用户设置目录与文件权限；切换 Xray 专用运行用户时也会同步修正这些权限。还需在防火墙中放行程序显示的随机 TCP 端口。

中转端不依赖系统 CA，也不会跳过证书验证：Xray 使用完整证书的 SHA-256 十六进制指纹，sing-box 使用证书公钥的 SHA-256 Base64 指纹。两种指纹都由落地端写入连接文本。

复制完整 JSON 文本。在中转服务器的交互菜单选择“添加中转线路”，程序会打开一个临时编辑文件；粘贴后保存并退出即可。临时文件权限为 `0600`，导入完成后会自动删除。

使用纯命令行时，通过标准输入粘贴文本，粘贴完成后按 `Ctrl+D`：

```bash
sudo proxyforge config relay add sing-box us \
  --upstream-stdin

sudo proxyforge config relay client sing-box us \
  --format clash --output ./us-client.yaml
```

连接文本包含安全协议、地址、端口、UUID 和 SNI；REALITY 模式还包含公钥与 short ID，TLS 模式包含 Xray 与 sing-box 分别使用的两种证书指纹。连接文本不含 REALITY 服务端私钥、TLS 私钥或证书文件路径，但其中 UUID 和证书指纹仍应作为敏感连接资料处理。TLS 连接文本缺少任一合法指纹时会被拒绝；旧的无指纹 TLS 接入需要删除并重新创建，REALITY 连接文本不受影响。支持 Xray 与 sing-box 两端任意组合，两种模式都使用 VLESS + Vision、RAW 传输。

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

重置本节点 SNI、REALITY 密钥或 short ID 后，已经导出的本节点客户端需要重新导出，落地连接文本需要重新生成并粘贴到中转机。轮换某条中转线路 UUID 只影响该线路的客户端；轮换落地接入 UUID 后，需要重新生成连接文本并更新所有使用它的中转机，但不会更换 TLS 证书。停用 TLS 接入也会保留证书，再启用时指纹不变。需要更换 TLS 证书时，请删除并重建接入，然后在所有中转机重新导入连接文本。删除接入、使用 `--drop-links` 或清理内核会删除对应受管证书。

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
- 回落：防偷跑配置把合法 SNI 回落指到独立的 `fallback-out`。给已有配置设置出站 IP 时，若回落还挂在 `direct` 上，会自动拆开。
- 恢复默认：Xray 写回 Freedom `AsIs`，并在 sockopt 打开 `UseIP` + `happyEyeballs`（先 IPv4，300ms 后竞速 IPv6，与 sing-box 默认一致）；`direct` 若还没有 `finalRules`，会补一条空的 `allow`，避免 VLESS 内置拦私网先把域名解析成单个 IP。优先/仅 IPv4·IPv6 会删掉这条空 `allow`（手写的带 `ip`/`port` 的规则保留）。sing-box 去掉这些 `strategy`。标准配置会保留原来的 `resolve` 规则和本地 DNS；简化配置会撤掉补上的 `resolve` 规则，以及仅为它添加的本地 DNS。
- 生成配置的默认状态是未设置（双栈）。Xray 默认先 IPv4 再竞速 IPv6（`direct` 带空 `allow`；`fallback-out` 不需要）；sing-box 同样先 IPv4。重置 SNI/凭证会保留此项；完整生成会覆盖。

## 回落 IP

仅当当前节点启用了回落防偷跑时，“服务端配置 → 修改配置 → 回落 IP”才出现。它只改 `fallback-out`，不影响用户代理出站。

- 优先 IPv4 / 优先 IPv6：Xray 写入回落 Freedom `UseIPv4v6` / `UseIPv6v4`。sing-box 给 `fallback-out` 的 `domain_resolver` 加上 `prefer_ipv4` / `prefer_ipv6`。
- 仅 IPv4 / 仅 IPv6：Xray 写入 `ForceIPv4` / `ForceIPv6`；sing-box 写入 `ipv4_only` / `ipv6_only`。
- 恢复默认：回落回到生成时的双栈。Xray 与用户出站相同，先 IPv4、300ms 后竞速 IPv6；sing-box 去掉 `fallback-out` 的 `strategy`。

## 菜单、日志与输出

真实终端中的菜单会自动清屏并使用固定语义颜色。管道和文件重定向会自动关闭颜色；也可设置 `NO_COLOR=1`、`PROXYFORGE_COLOR=never` 或 `PROXYFORGE_COLOR=always`。

“服务端配置 → 日志级别”可直接修改内核日志级别，会先备份、校验并在需要时重启服务。sing-box 支持 `trace/debug/info/warn/error/fatal/panic/关闭`，Xray 支持 `debug/info/warning/error/关闭`。

“服务端配置 → 服务管理”菜单可以启动、停止、重启服务，查看状态或持续查看 systemd journal；按 `Ctrl+C` 只停止实时日志并返回菜单。

运行信息统一采用“用途 │ 详情”格式，左侧按中文显示宽度对齐，右侧保留完整命令、路径、参数和结果，不使用 `[步骤]`、`[命令]` 等分类前缀。多行说明在详情列继续显示；服务日志和官方脚本实时输出也会注明来源。终端中的成功、失败和风险提示保留语义颜色。

```text
应用访问策略           │ 白名单优先，现有拦截规则兜底
校验 xray 配置         │ xray run -test -config /usr/local/etc/xray/.proxyforge-validate-example.json
命令结果               │ 校验 xray 配置：执行成功
备份位置               │ /var/lib/proxyforge/backups/xray/20261014T081753.483797182Z/config.json
重启服务               │ systemctl restart xray.service
命令结果               │ 重启服务：执行成功
更新结果               │ 白名单已更新。
```

流程与命令日志写入 stderr，客户端 JSON/YAML 和落地连接文本仍直接写入 stdout 或指定文件，保持原始内容。密钥生成结果不会写入命令日志，代理参数继续隐藏凭据。自身安装脚本使用同样的两列格式；直接连接终端的子进程仍保留其原生交互与下载进度。
