# ProxyForge

ProxyForge 是一个面向 Linux/systemd 的 Go 单二进制管理器，用来在同一台服务器上彼此隔离地管理一个 sing-box 和一个 Xray-core `VLESS + REALITY + Vision` 节点。两个内核拥有独立的配置、凭据、端口、状态和 systemd 服务，可以同时运行。

首版支持 Debian/Ubuntu 与 RHEL/CentOS/Rocky/AlmaLinux/Fedora 系发行版的 amd64、arm64。除 `--help` 和 `--version` 外，菜单及所有操作命令都必须由 root 执行；PID 1 不是 systemd 时会拒绝安装。

## 构建

```bash
./scripts/build.sh
go test ./...
```

构建脚本会从 Git 自动取得版本和提交号，并注入 UTC 构建时间；`make build` 是它的快捷入口。可以通过环境变量显式覆盖，便于发布流水线构建可复现版本：

```bash
VERSION=v1.0.0 COMMIT=abc1234 BUILD_DATE=2026-08-08T12:00:00Z ./scripts/build.sh
./proxyforge --version
```

不使用 Make 时，对应的直接构建命令为：

```bash
VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
COMMIT="${COMMIT:-$(git rev-parse --short HEAD 2>/dev/null || echo unknown)}"
BUILD_DATE="${BUILD_DATE:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}"

go build -trimpath -ldflags="-s -w \
  -X proxyforge/internal/version.Version=${VERSION} \
  -X proxyforge/internal/version.Commit=${COMMIT} \
  -X proxyforge/internal/version.BuildDate=${BUILD_DATE}" \
  -o proxyforge ./cmd/proxyforge
```

无参数运行会进入中文数字菜单：

```bash
sudo ./proxyforge
```

## CLI

```text
proxyforge install <sing-box|xray> [--version VERSION]
proxyforge uninstall <sing-box|xray> [--yes]
proxyforge cleanup <sing-box|xray|all> [--yes]
proxyforge config generate <sing-box|xray> --server HOST --port PORT --sni DOMAIN [--target HOST:PORT] [--user-name NAME] [--inbound-tag TAG]
proxyforge config client <sing-box|xray> [--format native|clash] [--output FILE] [--force]
proxyforge config reset <sing-box|xray> [--sni DOMAIN] [--target HOST:PORT] [--yes]
proxyforge service <sing-box|xray> <start|stop|restart|status|logs>
```

`install` 同时用于首次安装和后续升级；旧的 `upgrade` 名称保留为兼容别名。无参数运行时会先选择 sing-box 或 Xray-core，再进入该内核独立的安装/升级、配置、客户端、重置、服务、卸载和清理菜单。
安装 Xray 时会按 `HTTPS_PROXY`/`HTTP_PROXY` 和 `NO_PROXY` 检查当前进程的运行时代理；检测到代理后会自动传递给 Xray 官方管理脚本。通过 `sudo` 运行时需确保这些环境变量被保留，例如使用经过本机 sudo 策略允许的 `sudo -E`。日志只会提示已检测到代理，不会输出代理地址或认证信息。

非交互安装必须显式固定本次下载内容，`--yes` 不能跳过：

```bash
sudo proxyforge install sing-box --yes --trust-script-sha256 <64位哈希>
```

卸载必须交互确认，自动化时必须显式提供 `--yes`。卸载前会备份当前服务端配置；卸载命令完成后会核验二进制、systemd unit 和服务状态，核验通过后才删除受管配置和状态，核验失败时保留两者并报告残留项。若三项均已不存在，会跳过重复卸载并清理受管数据。外部修改或非受管配置、历史备份及脚本信任记录始终保留。Xray 卸载仍会执行其官方管理脚本，因此非交互模式还必须提供当前脚本哈希（已确认内核不存在并跳过官方脚本时除外）：

```bash
sudo proxyforge uninstall sing-box --yes
sudo proxyforge uninstall xray --yes --trust-script-sha256 <64位哈希>
```

需要彻底删除卸载后保留的数据时，可使用清理命令。程序检测到内核仍安装时会拒绝清理，必须先执行 `uninstall`。清理不会创建新备份，会永久删除所选内核的配置目录、运行数据、文件日志、ProxyForge 状态、信任记录和历史备份；`all` 会先确认两个内核均已卸载，再同时清理。程序不会删除 systemd journal，也不会记录或删除此前导出到用户指定位置的客户端配置。

```bash
sudo proxyforge cleanup sing-box
sudo proxyforge cleanup all --yes
```

首次交互安装会展示来源、最终重定向地址、大小、危险操作摘要和 SHA-256，输入 `yes`、`y` 或 `Y` 才执行；该规则适用于所有交互确认。脚本变化后，非交互执行会被阻止，必须重新进行交互确认。安装器下载受限临时文件，不使用 `curl | sh`，并检查 HTTPS 主机白名单、重定向、HTTP 状态、大小、shebang、NUL/文本格式和 `bash -n`。执行安装脚本时会实时转发 stdout/stderr，并保留最近 64 KiB 输出用于失败诊断。

生成首个节点时交互默认端口为 443；检测到另一个受管节点后默认建议 8443。生成 sing-box 服务端配置时可选择“标准安全配置”（默认）或“简化配置”；简化配置不启用 sing-box 内部 DNS 和路由预解析，改由出站连接使用系统 DNS，DNS 日志更少，但域名解析到私网地址时可能绕过路由私网拦截。非交互模式可用 `--simplified-config` 明确选择简化配置，重置凭证时会保留原配置模式。SNI 输入留空时，程序会并发验证内置候选域名的 DNS、TCP/TLS 和证书名称，展示当前最快的 10 个，并显示延迟、协商的 TLS 版本（包括 TLS 1.3）、ALPN、证书 SAN 和 CDN 特征，然后从中随机设置默认选择；手动输入的 SNI 也会执行相同检测，展示结果并单独确认采用后，再进行 SNI/REALITY target 最终确认。CDN 信息仅根据 CNAME、域名和解析地址数量进行启发式判断，不代表权威的服务归属；测速也只反映当前探测速度，最终 SNI/target 仍需人工确认。重新生成会保留 UUID、REALITY 密钥和 short ID；只有 `--rotate-credentials` 或凭证重置会同时轮换它们并让旧客户端失效。主菜单的“重置节点”只定点更新 SNI、target、UUID、REALITY 密钥和 short ID；“重置凭证”只定点更新 UUID、REALITY 密钥和 short ID，保留当前 SNI/target。两者都会保留 DNS、路由、出站、日志、其他用户及其他手动配置，找不到唯一受管入站或用户时会拒绝修改；修改前仍会备份，失败时回滚。`config reset` 保留地址和端口，并允许同时修改 SNI/target；只指定新 SNI 时 target 默认变为 `<新 SNI>:443`。只有“生成/更新服务端配置”会在确认后使用 ProxyForge 模板完整覆盖现有文件，原有自定义内容不会合并；非交互覆盖必须显式传入 `--yes`。

在真实终端中，交互菜单切换页面时会自动清屏；操作结果和错误会保留到按 Enter 返回菜单。管道、文件重定向及非交互命令不会输出 ANSI 控制字符，也不会额外等待输入。

所有操作都会输出 `[步骤]` 进度信息，并以 `[命令]` 展示实际执行的外部命令及成功/失败状态。步骤和命令日志写入 stderr，客户端 JSON 仍单独写入 stdout 或 `--output` 指定的文件；密钥生成命令的返回内容不会作为命令日志输出。安装脚本以及 `dpkg`/`rpm` 的安装卸载输出会实时转发。

示例：

```bash
sudo proxyforge config generate sing-box \
  --yes --server 203.0.113.10 --port 443 --sni www.example.com --user-name phone --inbound-tag phone-in

sudo proxyforge config generate xray \
  --yes --server 203.0.113.10 --port 8443 --sni www.example.com --user-name laptop --inbound-tag laptop-in

sudo proxyforge config client sing-box --output ./sing-box-client.json
sudo proxyforge config client xray --output ./xray-client.json
sudo proxyforge config client sing-box --format clash --output ./clash.yaml
```

服务端用户名称默认为 `proxyforge-user`，可通过交互提示或 `--user-name` 修改；它在 sing-box 中写入 `users[].name`，在 Xray 中写入 `clients[].email`。入站标签也可在交互配置时自定义，直接回车会按内核自动使用 `proxyforge-sing-box-in` 或 `proxyforge-xray-in`，非交互模式可通过 `--inbound-tag` 指定；两种内核都写入入站的 `tag` 字段。客户端文件以 `0600` 创建。`--format native` 是默认值，输出前会调用对应内核的原生配置校验：sing-box 客户端提供 `127.0.0.1:2080` mixed 入站，Xray 客户端提供 `127.0.0.1:10808` SOCKS 与 `127.0.0.1:10809` HTTP 入站。`--format clash` 输出带 `mixed-port: 7890`、`PROXY` 策略组和 `MATCH` 规则的完整 Mihomo/Clash Meta YAML；由于传统 Clash 不支持 VLESS REALITY，不能使用该文件，且 ProxyForge 不会用 sing-box/Xray 二进制校验这种非原生格式。

交互生成服务端配置时可选择公网地址来源：默认从已启用的物理网卡读取公网单播 IP（IPv4 排在 IPv6 前，拒绝内网、NAT 和保留地址），检测到多个时会列出网卡名和地址供用户选择；也可选择通过 `api.ipify.org` HTTPS 探测或手动输入。物理网卡未配置公网 IP 时会要求重新选择，不会自动把私网地址写入节点配置。

生成服务端配置的任意输入步骤均可输入 `q` 取消；菜单模式会直接返回内核主菜单，且不会写入配置或重启服务。

## 文件与安全边界

| 用途 | sing-box | Xray |
|---|---|---|
| 服务端配置 | `/etc/sing-box/config.json` | `/usr/local/etc/xray/config.json` |
| 状态 | `/var/lib/proxyforge/state/sing-box.json` | `/var/lib/proxyforge/state/xray.json` |
| systemd unit | `sing-box.service` | `xray.service` |

信任记录位于 `/var/lib/proxyforge/trust/`，备份位于 `/var/lib/proxyforge/backups/<core>/<timestamp>/`，每个内核仅保留最近 3 份，成功创建新备份后自动删除更早的 ProxyForge 时间戳备份。状态、信任和备份为 root-only；服务配置按 unit 的实际 `User=` 设置为 root 私有或 root 所有、服务组只读，绝不设为世界可读。

配置更新先写临时文件并运行 `sing-box check` 或 `xray run -test`，再原子替换、只重启目标服务并确认 active/监听状态。任一步失败都会恢复该内核的旧配置和状态并重启旧服务，不触碰另一个内核。

ProxyForge 会验证 REALITY target 的 DNS、TCP/TLS、证书名称和地址属性，拒绝本机、私网及保留地址。使用 CDN 目标可能把未认证的回落流量转发给第三方，必须自行评估。工具只提示 ufw/firewalld 所需 TCP 端口，不会修改防火墙。

生成的服务端和客户端配置会拒绝访问 IPv4/IPv6 私网、本机、链路本地、CGNAT、云元数据、基准测试、多播和保留地址。域名目标会先解析为 IP 再匹配黑洞规则；Xray 的 Freedom 出站也固定使用 IP 解析策略。

官方参考：[sing-box 安装](https://sing-box.sagernet.org/installation/package-manager/)、[sing-box VLESS](https://sing-box.sagernet.org/configuration/inbound/vless/)、[sing-box REALITY TLS](https://sing-box.sagernet.org/configuration/shared/tls/)、[Xray-install](https://github.com/XTLS/Xray-install)、[Xray VLESS](https://xtls.github.io/en/config/inbounds/vless.html)、[XTLS REALITY](https://github.com/XTLS/REALITY/blob/main/README.en.md)。

## 验收测试

golden、安全与事务回滚测试由 `go test ./...` 执行。提供官方二进制后可复跑四份配置的原生验收：

```bash
SING_BOX_BIN=/path/to/sing-box XRAY_BIN=/path/to/xray \
  go test -v ./internal/integration
```

在一次性 Debian/Ubuntu 或 Rocky/AlmaLinux systemd VM 中，可设置脚本要求的公网地址、SNI 和两个已人工核对的安装脚本哈希，再以 root 运行 `scripts/smoke-systemd.sh`。它会真实安装两个内核、生成 443/8443 节点、验证两个客户端并确认两项服务同时 active，因此不应在生产主机上直接运行。

## CI/CD 与发布

GitHub Actions 会在分支 push、Pull Request 和手动触发时运行测试、`go vet`，随后编译 Linux amd64、arm64，并将独立二进制文件保存为工作流产物。

发布正式版本时创建并推送一个 `v` 开头的 Git 标签：

```bash
git tag -a v1.0.0 -m "ProxyForge v1.0.0"
git push origin v1.0.0
```

Release 工作流会构建该标签并发布以下资产到当前 GitHub 仓库的 Releases；测试与 `go vet` 由独立的 CI 工作流负责：

```text
proxyforge_v1.0.0_linux_amd64
proxyforge_v1.0.0_linux_arm64
SHA256SUMS
```

下载对应架构的二进制后需要赋予执行权限，例如 `chmod +x proxyforge_v1.0.0_linux_amd64`，随后即可直接运行，不需要解压。

也可以在 GitHub Actions 的 `Release` 工作流中手动输入一个已经存在的标签重新发布。发布任务只在最后阶段取得 `contents: write` 权限；普通 CI 和构建任务保持只读权限。

Dependabot 每周一检查 Go Modules 和 GitHub Actions 更新，并分别创建依赖更新 Pull Request。
