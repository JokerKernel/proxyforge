# 安装、升级与卸载

ProxyForge 支持 Debian/Ubuntu 与 RHEL/CentOS/Rocky/AlmaLinux/Fedora 系发行版的 amd64、arm64。除 `--help` 和 `--version` 外，所有操作都必须由 root 执行；PID 1 不是 systemd 时会拒绝安装。

## 安装 ProxyForge

安装脚本会自动识别架构，通过 Release 的 `version` 获取最新正式版本，校验 `SHA256SUMS` 后原子安装或升级到 `/usr/local/sbin/proxyforge`：

```bash
curl -fsSL https://raw.githubusercontent.com/JokerKernel/proxyforge/main/scripts/install.sh | sudo bash
sudo /usr/local/sbin/proxyforge
```

安装固定版本：

```bash
curl -fsSL https://raw.githubusercontent.com/JokerKernel/proxyforge/main/scripts/install.sh | \
  sudo PROXYFORGE_VERSION=v0.0.4 bash
```

若希望先审阅脚本，请下载 `scripts/install.sh` 后运行 `sudo bash scripts/install.sh`。该脚本只替换 ProxyForge 二进制，不会安装或修改 sing-box、Xray、节点配置和 systemd 服务。

## 安装或升级代理内核

`install` 同时用于首次安装和后续升级；`upgrade` 保留为兼容别名：

```bash
sudo proxyforge install sing-box
sudo proxyforge install xray
```

首次交互安装会展示来源、最终重定向地址、大小、风险摘要和 SHA-256，只有输入 `yes`、`y` 或 `Y` 才执行。脚本发生变化后必须重新确认信任。

非交互安装必须显式固定脚本哈希，`--yes` 不能跳过该要求：

```bash
sudo proxyforge install sing-box --yes --trust-script-sha256 <64位哈希>
```

安装 Xray 时会读取 `HTTPS_PROXY`、`HTTP_PROXY` 和 `NO_PROXY`，并把代理设置传递给官方管理脚本。通过 `sudo` 运行时，应按本机 sudo 策略保留这些变量，例如使用允许的 `sudo -E`。日志只显示代理协议、主机和端口，不显示用户名、密码、路径和查询参数。

## 更新 ProxyForge

```bash
sudo proxyforge update
sudo proxyforge update --yes
```

`proxyforge update` 本身只从仓库 `main` 分支安全下载并检查当前安装脚本，然后以 `--update` 模式启动脚本。最新正式版本检查、当前版本比较、交互确认、Release 文件选择、`SHA256SUMS` 核验和 `/usr/local/sbin/proxyforge` 的原子替换全部由安装脚本完成；`--yes` 会原样传给脚本以跳过交互确认。

`update` 只更新 ProxyForge，不修改代理内核、节点配置或 systemd 服务。版本检查和下载会继承当前进程的标准代理环境。

## 卸载

卸载必须交互确认；自动化时必须指定 `--yes`：

```bash
sudo proxyforge uninstall sing-box --yes
sudo proxyforge uninstall xray --yes --trust-script-sha256 <64位哈希>
```

卸载前会临时备份服务端配置。官方卸载完成后，ProxyForge 会核验二进制、systemd unit、服务状态和开机启用状态；仅在核验通过后清理配置目录、运行数据、文件日志、状态、信任记录和历史备份。失败时不会自动清理。

Xray 非交互卸载通常还需提供当前官方脚本哈希。若内核已经完全不存在，则跳过重复卸载并直接清理残留。

## 清理卸载残留

`cleanup` 是兼容和故障恢复入口，不显示在交互菜单中：

```bash
sudo proxyforge cleanup sing-box
sudo proxyforge cleanup all --yes
```

只有确认目标内核的二进制、systemd unit、运行状态和开机启用状态均已清除后，命令才会删除残留数据。它不会删除 systemd journal，也不会处理此前导出到用户指定位置的客户端配置。

## 安装脚本安全检查

ProxyForge 将下载内容写入受限临时文件，不使用内部 `curl | sh`，并检查 HTTPS 主机白名单、重定向、HTTP 状态、大小、shebang、NUL/文本格式和 `bash -n`。执行官方脚本时会实时转发输出，并保留最近 64 KiB 用于失败诊断。
