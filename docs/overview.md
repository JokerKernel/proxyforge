# 项目概览与快速上手

ProxyForge 是面向 Linux/systemd 的双内核代理管理器，通过中文交互菜单或命令行，在同一台服务器上独立管理 **Xray-core** 和 **sing-box** 的 `VLESS + REALITY + Vision` 节点。

使用 Go 编写，单二进制部署，涵盖内核安装、节点配置、客户端导出、服务管理和卸载。

## 核心能力

- **双内核独立管理**：配置、凭据、监听端口和 systemd 服务分别管理。
- **安装渠道选择**：支持稳定版、开发版和指定版本，两个内核分别记忆成功安装的渠道。
- **REALITY 回落防护**：默认生成防偷跑配置，支持 SNI/target 检测、HTTP Host 限制和严格域名匹配。
- **配置与服务管理**：生成配置、调整 DNS 和出站 IP、重置凭据、查看日志；应用配置时校验、备份，失败时回滚。
- **客户端导出**：支持原生 sing-box/Xray JSON 和 Mihomo/Clash Meta YAML。
- **中转与落地**：按用户配置多条中转线路；落地可复用 REALITY 入站，或创建带自签证书和固定指纹校验的独立 TLS 入站。

## 运行环境

| 项目 | 要求 |
| --- | --- |
| 系统 | Debian、Ubuntu、RHEL、CentOS、Rocky Linux、AlmaLinux、Fedora |
| 架构 | amd64 / arm64 |
| 服务管理 | systemd，PID 1 必须为 systemd |
| 权限 | 除 `--help` 和 `--version` 外，所有操作需要 root |

安装和更新需要能够访问 GitHub 及内核官方安装来源。

## 快速上手

安装 ProxyForge：

```bash
curl -fsSL https://raw.githubusercontent.com/JokerKernel/proxyforge/main/scripts/install.sh | sudo bash
```

安装脚本会校验 Release 的 `SHA256SUMS`，并将程序原子安装到 `/usr/local/sbin/proxyforge`。固定版本安装、脚本审阅和代理设置见[安装文档](installation.md)。

无参数运行进入中文交互菜单：

```bash
sudo proxyforge
```

首次使用可按以下顺序操作：

1. 选择 Xray-core 或 sing-box。
2. 进入「安装/升级」，确认安装渠道后执行安装。
3. 进入「服务端配置 → 生成/更新配置」，设置公网地址、监听端口和 REALITY SNI。
4. 进入「客户端配置」，获取原生 JSON 或 Clash YAML 配置。

ProxyForge 会提示需要放行的 TCP 端口，防火墙和云安全组需自行配置。

## 内核安装渠道

两个内核的安装/更新页都提供以下选项：

| 选项 | 用途 |
| --- | --- |
| 安装/更新 | 按信息卡中的「安装渠道」执行安装或升级 |
| 选择版本 | 切换稳定版（最新正式版）或开发版（最新预发布） |
| 指定版本 | 输入官方版本号，仅用于本次安装 |

首次默认稳定版；成功安装并通过检查后保存渠道，下次进入交互安装页会恢复该选择。仅切换选项、取消或安装失败不会修改记录，指定版本也不会覆盖已保存的渠道。

主菜单的「更新渠道」显示 `[稳定版]` 或 `[开发版]`，两者均为橙色，无记录时默认显示稳定版。该字段读取保存的更新偏好，不根据已安装的版本号判断渠道。两个内核的设置分别保存在 `/var/lib/proxyforge/preferences/sing-box.json` 和 `xray.json`。

## 常用命令

在终端中安装或更新内核，未指定版本参数时会打开安装/更新页：

```bash
sudo proxyforge install sing-box
sudo proxyforge install xray

# 安装或更新到最新开发版
sudo proxyforge install xray --beta
```

生成节点并导出客户端配置（将占位符替换为实际公网地址和可用的 SNI 域名）：

```bash
sudo proxyforge config generate xray \
  --yes --server YOUR_SERVER_IP --port 443 --sni YOUR_ALLOWED_SNI

sudo proxyforge config client xray --output ./xray-client.json
sudo proxyforge config client xray --format clash --output ./clash.yaml
sudo proxyforge service xray status
sudo proxyforge service xray logs
```

上述命令中的 `xray` 可替换为 `sing-box`。`config generate` 会备份并完整覆盖服务端配置；客户端输出文件默认不覆盖已有文件。

自动化安装需提供 `--trust-script-sha256` 固定官方脚本哈希，`--yes` 不能跳过此要求。非交互安装不读取保存的渠道，未指定 `--beta` 或 `--version` 时使用稳定版。完整参数和示例见[配置文档](configuration.md)与[安装文档](installation.md)。

使用 `proxyforge --help` 或 `proxyforge <command> --help` 查看帮助。

## 更新与卸载

| 命令 | 作用 |
| --- | --- |
| `sudo proxyforge update` | 更新 ProxyForge 自身 |
| `sudo proxyforge install xray` | 安装或更新 Xray 内核 |
| `sudo proxyforge install sing-box` | 安装或更新 sing-box 内核 |
| `sudo proxyforge uninstall` | 仅卸载 ProxyForge 程序，保留内核、节点配置和管理数据 |
| `sudo proxyforge uninstall xray` | 卸载 Xray，并在核验通过后清理其配置和运行数据 |
| `sudo proxyforge uninstall sing-box` | 卸载 sing-box，并在核验通过后清理其配置和运行数据 |

更新 ProxyForge 自身不会更新代理内核。卸载确认、自动化参数和残留清理见[安装文档](installation.md)。

## 文档

| 文档 | 内容 |
| --- | --- |
| [安装、升级与卸载](installation.md) | 固定版本、脚本信任、代理环境与清理残留 |
| [配置与日常使用](configuration.md) | 服务端、客户端、DNS、SNI、中转与落地 |
| [文件与安全边界](security.md) | 文件路径、权限、备份、回滚与下载检查 |
| [REALITY SNI 检测指南](reality-sni-check.md) | 可独立下载的黑盒检测脚本、结果判定与排障 |
| [构建、测试与发布](development.md) | 本地构建、集成测试、CI 与 Release 流程 |

## 开发

在项目根目录运行，需要 Go 1.23 或更高版本：

```bash
./scripts/build.sh
go test ./...
```

构建参数、原生集成测试、CI 和发布流程见[开发文档](development.md)。
