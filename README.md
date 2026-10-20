# ProxyForge

ProxyForge 是面向 Linux 的代理管理工具，提供中文交互菜单和命令行，通过 systemd 独立管理原生 Xray-core 和 sing-box，包含版本更新、配置管理和日常维护。

Docker 代理管理已分离为独立项目 **ProxyDock**，使用 `proxydock` 命令。原有 `proxyforge docker …` 改为 `proxydock …`。

使用 Go 编写，单二进制部署；使用预编译版本无需安装 Go。

## 核心能力

| 功能 | 说明 |
| --- | --- |
| 组件管理 | 分别管理配置、凭据、监听端口和 systemd 服务 |
| 版本选择 | 支持稳定版、开发版和指定版本，按组件保存安装渠道 |
| 节点部署 | VLESS + RAW + REALITY/TLS + Vision，TLS 自动生成自签证书 |
| 访问控制 | 域名/IP/网段黑白名单及独立端口限制，首次白名单自动限制，支持仅白名单模式 |
| 配置管理 | 生成、编辑和校验配置，应用配置时备份，失败时回滚 |
| 服务管理 | 启动、停止、重启服务，查看运行状态和日志 |
| 配置导出 | 将客户端配置导出为 JSON 或 YAML |

## 运行环境

| 项目 | 要求 |
| --- | --- |
| 系统 | Debian、Ubuntu、RHEL、CentOS、Rocky Linux、AlmaLinux、Fedora |
| 架构 | amd64（x86_64）、arm64（aarch64） |
| 原生服务管理 | systemd，PID 1 必须为 systemd |
| 权限 | 除 `--help` 和 `--version` 外，所有操作需要 root |

## 快速开始

安装程序：

```bash
curl -fsSL https://raw.githubusercontent.com/JokerKernel/proxyforge/main/scripts/install.sh | sudo bash
```

安装脚本自动识别架构，校验 Release 的 `SHA256SUMS`，并将程序原子安装到 `/usr/local/sbin/proxyforge`。安装与更新需要能够访问下载来源。

启动中文交互菜单：

```bash
sudo proxyforge
```

首次使用可按以下顺序操作：

1. 选择 Xray-core 或 sing-box。
2. 进入「安装/升级」，确认安装渠道后执行安装。
3. 进入「服务端配置 → 生成/更新配置」，设置公网地址、监听端口和节点协议；REALITY 模式还需选择 SNI。
4. 进入「客户端配置」，导出原生 JSON 或 Mihomo/Clash Meta YAML 配置。

ProxyForge 会提示需要放行的 TCP 端口，防火墙和云安全组需自行配置。固定版本安装和其他安装方式见[安装指南](docs/installation.md)。

## 版本与更新

组件首次安装默认选择稳定版。安装/更新页可切换开发版或指定版本；成功安装并通过检查后，会保存该组件的渠道选择，下次进入交互安装页时恢复。

仅切换选项或取消操作不会保存，安装失败也不会覆盖已有记录；指定版本只对本次安装生效。自动化安装的参数和渠道规则见[安装指南](docs/installation.md)。

`proxyforge update` 用于更新 ProxyForge 程序。组件更新通过交互菜单中的安装/升级入口执行。

## 常用命令

| 命令 | 用途 |
| --- | --- |
| `sudo proxyforge` | 进入中文交互菜单 |
| `proxyforge --help` | 查看命令帮助 |
| `proxyforge --version` | 查看程序版本 |
| `sudo proxyforge update` | 更新 ProxyForge 程序 |
| `sudo proxyforge uninstall` | 卸载 ProxyForge 程序 |

卸载程序会保留受管组件、配置和管理数据。组件卸载与残留清理见[安装指南](docs/installation.md)。使用 `proxyforge <command> --help` 查看子命令参数。

## 文档

| 文档 | 内容 |
| --- | --- |
| [安装指南](docs/installation.md) | 安装、升级、版本选择、卸载与清理 |
| [使用指南](docs/configuration.md) | 配置管理、客户端导出与服务操作 |
| [文件与权限](docs/security.md) | 数据目录、权限、备份与安全边界 |
| [REALITY SNI 检测](docs/reality-sni-check.md) | 黑盒检测脚本、结果判读与排障 |
| [开发指南](docs/development.md) | 构建、测试、CI 与发布流程 |

更多专题说明见[文档目录](docs/README.md)。

## 开发

需要 Go 1.23 或更高版本，在项目根目录运行：

```bash
./scripts/build.sh
go test ./...
```

构建产物为项目根目录下的 `proxyforge`。构建参数和集成测试方法见[开发指南](docs/development.md)。
