# ProxyForge

ProxyForge 是面向 Linux/systemd 的 Go 单二进制管理器，用于在同一台服务器上独立管理 sing-box 和 Xray-core 的 `VLESS + REALITY + Vision` 节点。

## 核心能力

- 同时管理 sing-box 与 Xray，配置、凭据、端口和 systemd 服务彼此隔离。
- 交互菜单与完整 CLI，支持安装、升级、配置、客户端导出、服务管理和卸载。
- 默认生成 REALITY 回落防偷跑配置，并提供 SNI/target 自动检测。
- 配置写入前校验和备份，失败时自动回滚。
- 支持原生 sing-box/Xray 客户端及 Mihomo/Clash Meta 配置。
- 支持 Debian/Ubuntu 与 RHEL/CentOS/Rocky/AlmaLinux/Fedora 的 amd64、arm64。

除 `--help` 和 `--version` 外，所有操作都需要 root 权限和 systemd 环境。

## 快速安装

```bash
curl -fsSL https://raw.githubusercontent.com/JokerKernel/proxyforge/main/scripts/install.sh | sudo bash
```

```
sudo /usr/local/sbin/proxyforge
```

安装脚本会校验 Release 的 `SHA256SUMS`，并将 ProxyForge 原子安装到 `/usr/local/sbin/proxyforge`。安装固定版本、审阅脚本、代理环境和卸载说明见[安装文档](docs/installation.md)。

卸载 ProxyForge 自身：

```bash
sudo proxyforge uninstall
```

也可以直接调用在线安装脚本：

```bash
curl -fsSL https://raw.githubusercontent.com/JokerKernel/proxyforge/main/scripts/install.sh | \
  sudo bash -s -- uninstall
```

安装脚本同时支持 `sudo bash scripts/install.sh uninstall` 和 `--uninstall`。以上方式都只删除 `/usr/local/sbin/proxyforge`，不会卸载 sing-box、Xray，也不会删除节点配置、状态或备份。

## 快速使用

无参数运行进入中文交互菜单：

```bash
sudo proxyforge
```

常用非交互命令：

```bash
sudo proxyforge install sing-box

sudo proxyforge config generate sing-box \
  --yes --server 203.0.113.10 --port 443 --sni www.example.com

sudo proxyforge config client sing-box --output ./sing-box-client.json
sudo proxyforge service sing-box status
```

使用 `proxyforge --help` 或 `proxyforge <command> --help` 查看命令参数。

## REALITY SNI 检测

没有项目源码时可单独下载黑盒检测脚本：

```bash
wget --https-only \
  -O ~/proxyforge-test-reality-sni.sh \
  https://raw.githubusercontent.com/JokerKernel/proxyforge/main/scripts/test-reality-sni.sh

chmod +x ~/proxyforge-test-reality-sni.sh
~/proxyforge-test-reality-sni.sh \
  --host YOUR_SERVER_IP --port 443 --sni YOUR_ALLOWED_SNI
```

判定逻辑和排障说明见 [REALITY SNI 检测指南](docs/reality-sni-check.md)。

## 文档

- [安装、升级与卸载](docs/installation.md)
- [配置与日常使用](docs/configuration.md)
- [文件与安全边界](docs/security.md)
- [REALITY SNI 检测指南](docs/reality-sni-check.md)
- [构建、测试与发布](docs/development.md)

## 开发

```bash
./scripts/build.sh
go test ./...
```

构建参数、原生集成测试、CI 和发布流程见[开发文档](docs/development.md)。
