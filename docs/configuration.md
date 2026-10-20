# 配置与日常使用

运行 `sudo proxyforge` 进入菜单，选择 Xray-core 或 sing-box 后管理对应内核。以下示例中的 `xray` 可替换为 `sing-box`；完整参数见命令帮助：

```bash
proxyforge config --help
proxyforge config generate --help
proxyforge service --help
```

## 生成服务端配置

```bash
sudo proxyforge config generate xray \
  --yes --server YOUR_SERVER_IP --port 443 --sni YOUR_ALLOWED_SNI
```

将占位符替换为公网地址和可用 SNI。生成前检查内核及 systemd unit，写入前备份；完整生成会覆盖现有服务端配置，非交互覆盖必须指定 `--yes`。交互输入 `q` 可取消。

可用 `--user-name` 和 `--inbound-tag` 自定义普通用户及入站标签。重新生成默认保留当前协议、同协议凭据、中转线路和落地接入；`--rotate-credentials` 轮换凭据，`--drop-links` 清除附加线路和接入。

“服务端配置 → 编辑配置”用于手动修改：退出编辑器后校验，成功才重启；失败时可返回编辑器修正。

### 自签 TLS 主节点

TLS 模式不需要域名或自备证书：

```bash
sudo proxyforge config generate xray --yes \
  --security tls --server YOUR_SERVER_IP --port 443
```

程序生成 10 年有效的 ECDSA P-256 自签证书和随机 TLS 名称，最低 TLS 版本为 1.3。客户端连接提供的 IP/域名，通过导出配置中的固定指纹验证。TLS 模式不接受 `--sni`、`--target` 或 REALITY 回落参数。

保持 TLS 协议重新生成时，默认保留 UUID 和证书；两者可独立重置：

```bash
# 轮换 UUID，保留证书
sudo proxyforge config reset xray --yes

# 轮换证书及 TLS 名称，保留 UUID
sudo proxyforge config reset xray --yes --rotate-certificate
```

REALITY 与 TLS 互相切换时会更换主节点 UUID，附加落地和中转用户的 UUID 保留。证书轮换或切换协议后需重新导出普通客户端及复用主入站的落地连接文本。旧主节点证书保留供配置备份使用，卸载或清理内核时删除。

### 配置模式

| 模式 | 选择方式 | 说明 |
| --- | --- | --- |
| 回落防偷跑 | REALITY 默认 | 通过本机内部入站限制回落目标，内部端口默认从 30000–65000 分配 |
| 标准配置 | `--standard-config` | 不启用上述回落防护 |
| 简化配置 | `--simplified-config` | 仅 sing-box；使用系统解析，域名解析到私网时可能绕过路由私网拦截 |

这些模式参数不能组合。两个内核均提供以下回落选项，按内核使用 `--xray-` 或 `--sing-box-` 前缀：

- `fallback-http-domain`：要求明文 HTTP Host 匹配当前 SNI，默认关闭。
- `fallback-exact-domain`：严格匹配完整域名，默认允许子域名；与 HTTP Host 选项独立。
- `fallback-port`：指定内部回落端口，不能与公网监听端口或其他受管节点冲突。

例如 `--xray-fallback-exact-domain`。已有配置默认沿用内部端口；回落防护只限制未认证流量，不替代访问控制。

### SNI 与 REALITY target

交互生成时可保留已有 SNI、选择自动检测的候选或手动输入。候选会验证 DNS、TCP/TLS 和证书名称，并测量 IPv4/IPv6 延迟；每页 20 项，按提示切换排序及翻页，仅能选择当前页编号。手动输入也执行相同检查。

“修改配置 → REALITY SNI 候选检测”可重新检测当前 SNI 与候选，检测本身不修改配置。测速仅反映当前网络，CDN 识别为启发式结果，最终 SNI/target 需要人工确认。

连接地址可从物理网卡选择、通过公网 HTTPS 探测或手动输入；程序不会将私网、NAT 或保留地址自动写成公网地址。

## 重置节点

```bash
sudo proxyforge config reset xray --yes --sni YOUR_NEW_SNI
```

不指定 `--sni` / `--target` 的 REALITY 凭据重置会轮换 UUID、密钥和 short ID；仅修改 SNI/target 时保留凭据。地址和监听端口不变；SNI 改变且未指定 target 时，target 默认为 `<新 SNI>:443`，回落目标与域名限制同步更新。

定点重置保留 DNS、路由、出站、日志、其他用户及手工配置；无法唯一识别受管入站或用户时拒绝修改。完整重新生成与定点重置的区别是：前者覆盖服务端文件，后者只修改受管节点字段。

## Xray 专用运行用户

在“Xray → 服务端配置 → 专用运行用户”切换为独立 `xray` 系统账号，避免共用 `nobody`。程序调整 systemd 设置及配置、证书和日志权限，并以该身份校验读取权限；运行中的服务会重启。

迁移失败时尝试恢复原设置。新创建的账号会保留，后续卸载按所有权核验删除；已有账号不会被盲目删除。

## 导出客户端

```bash
sudo proxyforge config client xray --output ./xray-client.json
sudo proxyforge config client sing-box --format clash --output ./clash.yaml
```

文件以 `0600` 创建，已有文件需显式 `--force` 覆盖；省略 `--output` 时直接显示配置。

| 格式 | 本地监听 | 要求 |
| --- | --- | --- |
| sing-box 原生 JSON | `127.0.0.1:2080` mixed | 对应内核校验；自签 TLS 需 sing-box 1.13 或更高版本 |
| Xray 原生 JSON | `127.0.0.1:10808` SOCKS、`127.0.0.1:10809` HTTP | 对应内核校验 |
| `--format clash` | `mixed-port: 7890` | 支持 VLESS 的 Mihomo/Clash Meta，传统 Clash 不适用 |

自签 TLS 保持证书验证开启：Xray/Mihomo 固定证书 SHA-256，sing-box 固定公钥 SHA-256。配置需通过可信方式传递，不应改为跳过验证。

## 访问控制

“修改配置 → 访问控制”管理访问模式、白名单、黑名单、每条规则的目标端口及独立总开关。策略按内核管理，覆盖普通用户、落地接入和中转线路，不改变 REALITY 回落防护。

| 模式 | 规则 |
| --- | --- |
| 空值 | 自动选择已启用；当前白名单有内容则按仅白名单执行，手动选择模式前保持空值 |
| `default` | 明确选择默认模式，后续增删规则不自动切换；白名单优先放行，再匹配黑名单，最后执行原有私网等拦截规则 |
| `whitelist` | 仅放行白名单，忽略黑名单；空白名单拒绝全部代理目标 |

白名单可明确放行私网地址，但最终出口仍需能访问该网络。中转用户命中白名单后仍走指定落地，不回退本机出口。

关闭访问控制会保留模式和名单，恢复内核原有拦截；关闭期间添加规则不会自动启用。删除或清空名单均保留模式、另一份名单及总开关；`whitelist` 模式清空白名单后仍全部拒绝。`mode auto` 清空保存的手动选择，启用自动选择：已有白名单则按仅白名单执行，没有白名单则按默认模式执行。自动选择实时按当前白名单判断：删除最后一条白名单或清空白名单后自动回到默认模式；菜单第 1 项按 `mode` 是否为空显示已启用/已关闭，第 2、3 项显示实际生效的模式。手动保存的 `default`、`whitelist` 优先；空 `mode` 的已有白名单也按自动规则进入仅白名单。

```bash
# 新域名默认包含子域名、目标端口 80,443；IP/CIDR 默认全部端口
sudo proxyforge config access add xray white example.com 192.168.1.0/24 --yes
sudo proxyforge config access add xray white api.example.com --exact --yes
sudo proxyforge config access add xray black denied.example.com --yes
# --ports 对本批所有目标生效；菜单可逐条指定端口
sudo proxyforge config access add xray white example.com --ports 553 --yes
sudo proxyforge config access add xray white full.example.com --ports all --yes

sudo proxyforge config access mode xray whitelist --yes
sudo proxyforge config access mode xray auto --yes
sudo proxyforge config access list xray
sudo proxyforge config access disable xray --yes
sudo proxyforge config access enable xray --yes

# 删除原始记录，端口和匹配方式必须一致；旧的全部端口域名使用 --ports all
sudo proxyforge config access remove xray white example.com --ports 553 --yes
sudo proxyforge config access remove xray white api.example.com --exact --yes
sudo proxyforge config access clear xray white --yes
```

端口支持 `80,443`、`80.443`、中文逗号、`8000-8100` 和 `all`；自动排序、去重并合并重叠和相邻范围。保存值为空或缺少 `ports` 字段表示全部端口，旧规则保持原有范围。地址和端口须同时匹配。

批量输入支持空格、英文或中文逗号；`--exact` 仅影响域名。所有地址及端口先校验、标准化并去重，任一无效则整批拒绝。域名不能包含协议、端口、路径或通配符；单 IP 与 CIDR 不等价，IPv6 同样支持。

管理列表保留原始记录；生成配置时先为同一地址和匹配方式合并端口，再把最终端口相同的地址合并。比如 `a.com → 80`、`b.com → 80`、`a.com → 553` 生成 `a.com → 80,553` 与 `b.com → 80`，不会扩大 `b.com` 的范围。完整域名、包含子域名、黑白名单及中转出站分别计算。删除针对原始记录（菜单支持编号删除）；一条 `80,443,553` 不能通过删除 `553` 减去端口，需删整条后重新添加 `80,443`。

域名匹配使用代理请求的实际目标。按 IP 访问需匹配 IP/网段规则，伪造 HTTP Host 或 TLS SNI 不会获得域名放行；域名黑名单也不等于封禁该站所有 IP。

访问控制保存为独立 JSON，格式如下。`mode` 只记录手动选择，空值表示自动选择；此时按当前白名单判断实际模式。旧文件中的 `auto_whitelist` 字段读取时忽略，后续保存时移除。手动保存的模式和各条规则的端口范围保持不变。

```json
{
  "disabled": false,
  "mode": "default",
  "whitelist": [{ "type": "domain", "value": "example.com", "include_subdomains": true, "ports": "80,443" }],
  "blacklist": [{ "type": "cidr", "value": "192.168.1.0/24" }]
}
```

文件路径见[文件与安全边界](security.md#受管文件)。手动编辑后需重新生成服务端配置才能应用；旧 `blacklist` 模式需改为 `default`，旧受管标签不兼容时需备份后重新生成。名单修改使用访问控制进程锁和事务日志，通过当前原生内核校验后备份、应用、检查运行和监听状态，再保存名单及节点状态；失败回滚，后续访问控制读取或修改会先恢复未完成事务。并发修改导致确认预览过期时拒绝覆盖。访问新放行的私网目标时，还需重新导出原生客户端配置；Mihomo 由服务端执行策略。

## 中转与落地

每条中转线路使用独立 UUID 并固定连接指定落地；普通用户继续走本机出口，落地不可用时中转线路不会回退本机。

先在落地服务器创建接入：

```bash
# 复用当前 REALITY/TLS 主入站及端口
sudo proxyforge config landing add xray from-relay

# 创建独立 TLS 入站、随机端口及自签证书
sudo proxyforge config landing add xray from-relay-tls --security tls
```

默认 `--security current` 复用主入站；`--security reality` 仅适用于 REALITY 主节点。独立 TLS 端口从 30000–65000 分配，需放行防火墙和云安全组；证书与主节点一样使用固定指纹验证。

复制输出的完整 JSON 连接文本。在中转服务器的菜单选择“添加中转线路”并粘贴到编辑器，或通过标准输入导入，粘贴后按 `Ctrl+D`：

```bash
sudo proxyforge config relay add sing-box us --upstream-stdin
sudo proxyforge config relay client sing-box us --format clash --output ./us-client.yaml
```

连接文本包含 UUID 和证书指纹，应通过可信方式传递；不包含服务端私钥。Xray 与 sing-box 可任意组合。TLS 文本必须包含两种合法指纹，旧文本缺少指纹时需重新创建接入并导入。

菜单粘贴使用私有临时目录，完成或失败后清理，包括编辑器备份与交换文件；清理失败会提示残留路径。

名称限 1–32 个字母、数字、下划线或连字符，首位必须是字母或数字；与现有接入、线路、普通用户、主入站或保留名称冲突时拒绝创建。

```bash
sudo proxyforge config landing list xray
sudo proxyforge config landing export xray from-relay
sudo proxyforge config landing rotate xray from-relay --yes
sudo proxyforge config landing remove xray from-relay --yes

sudo proxyforge config relay list sing-box
sudo proxyforge config relay test sing-box us
sudo proxyforge config relay update sing-box us --upstream-stdin
sudo proxyforge config relay disable sing-box us
sudo proxyforge config relay rotate sing-box us --yes
sudo proxyforge config relay remove sing-box us --yes
```

`landing export`（别名 `show`）可再次获取连接文本；停用后可用 `enable` 恢复。`landing add/export` 支持 `--output` 文件导出，`relay add/update` 支持 `--upstream` 文件导入，完整参数见子命令帮助。

添加或更新线路前检查 TCP 连通性，确认暂时不可达仍要保存时可显式使用 `--allow-unreachable`。落地地址默认要求公网，也允许 `192.168.0.0/16` 联调；其他私网、回环及保留地址仍拒绝，该例外不影响用户代理的私网拦截。

轮换落地 UUID 后需重新导出连接文本并更新所有中转机；轮换中转 UUID 后需更新该线路客户端。停用 TLS 接入保留证书；更换独立接入证书需删除重建并重新导入。修改主节点 SNI/REALITY 密钥后，也需更新相关客户端和落地连接文本。

线路和接入使用独立文件保存，手动编辑后需重新生成配置。停用线路会移除相应入站用户；删除接入、`--drop-links` 或清理内核会删除相应受管证书。

## DNS 设置

“修改配置 → DNS 设置”提供系统 DNS、Cloudflare/Google 明文 DNS 和 DoH，只影响代理内核，不修改系统 DNS。

Xray 公共 DNS 配置包含两家服务器并按所选顺序回退；sing-box 只使用所选公共上游，DoH 另保留系统 DNS 用于引导解析。修改前校验并备份，服务运行时才重启。

## 出站 IP

“修改配置 → 出站 IP”控制用户代理出口；“回落 IP”仅在启用回落防护时显示，控制未认证回落连接，二者独立。

| 选项 | 行为 |
| --- | --- |
| 优先 IPv4 / IPv6 | 优先使用指定地址族；Xray 只在解析不到优先族时尝试另一族，sing-box 支持连接回退 |
| 仅 IPv4 / IPv6 | 限制地址族，对端只有另一族时连接失败 |
| 恢复默认 | 双栈，先尝试 IPv4；Xray 使用 Happy Eyeballs，sing-box 使用默认策略 |

初次生成默认双栈。定点重置保留 IP 策略，完整重新生成会覆盖；用户出站设置不修改 DNS 服务器列表或回落 IP。

## 菜单、日志与输出

“服务端配置 → 日志级别”可调整内核日志；sing-box 支持 `trace/debug/info/warn/error/fatal/panic/关闭`，Xray 支持 `debug/info/warning/error/关闭`。

“服务管理”可启动、停止、重启、查看状态和日志；菜单实时日志中按 `Ctrl+C` 返回，不停止代理服务。命令行查看最近日志：

```bash
sudo proxyforge service xray status
sudo proxyforge service xray logs
```

管道和文件输出默认关闭颜色，可通过 `NO_COLOR=1` 或 `PROXYFORGE_COLOR=never|always` 调整。流程日志写入 stderr，客户端配置和连接文本写入 stdout 或指定文件；密钥生成结果和代理凭据不写入命令日志。
