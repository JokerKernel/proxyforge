# 安装、升级与卸载

运行环境要求见[项目首页](../README.md#运行环境)。除帮助和版本查询外，操作需要 root；安装代理内核需要 systemd。

## 安装 ProxyForge

```bash
curl -fsSL https://raw.githubusercontent.com/JokerKernel/proxyforge/main/scripts/install.sh | sudo bash
sudo proxyforge
```

安装脚本自动选择架构、校验 `SHA256SUMS`，将程序原子安装到 `/usr/local/sbin/proxyforge`。已有相同版本时退出，已有版本较新时跳过，避免自动降级。脚本只管理 ProxyForge 程序，不修改代理内核或节点配置。

安装指定版本时设置 `PROXYFORGE_VERSION`，将示例版本替换为所需 Release 标签：

```bash
curl -fsSL https://raw.githubusercontent.com/JokerKernel/proxyforge/main/scripts/install.sh | \
  sudo PROXYFORGE_VERSION=v0.0.4 bash
```

需要先审阅时，下载 `scripts/install.sh`，再运行 `sudo bash scripts/install.sh`。

## 安装或升级代理内核

```bash
sudo proxyforge install sing-box
sudo proxyforge install xray
sudo proxyforge install xray --beta
sudo proxyforge install xray --version VERSION
```

`install` 用于安装和升级，`upgrade` 为兼容别名；两个内核均支持 `--beta` 和 `--version`，两者不能同时使用。

- 交互安装默认稳定版，可选择开发版或指定版本。各内核只保存成功安装的稳定版/开发版渠道；取消、安装失败或指定版本安装不会覆盖渠道记录。
- 在终端中未指定版本参数时，会恢复已保存的渠道；非交互安装不读取偏好，未指定参数时使用稳定版。
- 显示的渠道来自保存的选择，不根据已安装版本推断。

首次执行官方内核脚本或脚本变化后，需要确认来源和 SHA-256。非交互安装必须显式固定脚本哈希，`--yes` 不能绕过此要求；将 `SHA256_HASH` 替换为确认过的 64 位哈希：

```bash
sudo proxyforge install sing-box --yes --trust-script-sha256 SHA256_HASH
```

安装 Xray 时使用 `HTTPS_PROXY`、`HTTP_PROXY` 和 `NO_PROXY`。通过 sudo 执行时，需按本机策略保留这些变量，例如使用允许的 `sudo -E`；日志会隐藏代理凭据。

## 更新 ProxyForge

```bash
sudo proxyforge update
sudo proxyforge update --yes
```

程序下载并检查仓库当前安装脚本，再交由脚本判断版本、校验和替换程序。版本发现优先使用 GitHub Releases API，失败时直连重试，仍失败则回退到 Release 的 `version` 文件。下载继承标准代理环境。

该命令只更新 ProxyForge；更新内核仍使用 `proxyforge install <内核>`。

## 卸载

| 命令 | 删除范围 |
| --- | --- |
| `sudo proxyforge uninstall` | 仅删除 ProxyForge 程序，保留内核、配置和管理数据 |
| `sudo proxyforge uninstall sing-box` | 卸载 sing-box，核验成功后清理其配置和运行数据 |
| `sudo proxyforge uninstall xray` | 卸载 Xray，核验成功后清理其配置和运行数据 |

也可运行 `sudo bash scripts/install.sh uninstall` 卸载主程序。

卸载内核前会备份配置。只有确认二进制、systemd unit、运行状态和开机启用状态均已清除后，才删除该内核的配置、日志文件、状态、信任记录、安装偏好、证书和备份；核验失败会保留数据。

卸载需要交互确认，自动化使用 `--yes`；Xray 非交互卸载还需固定官方脚本哈希：

```bash
sudo proxyforge uninstall sing-box --yes
sudo proxyforge uninstall xray --yes --trust-script-sha256 SHA256_HASH
```

ProxyForge 创建的 `xray` 专用账号仅在所有权和 UID/GID 核验通过时删除；已有账号或缺少所有权记录的账号会保留。

## 清理卸载残留

```bash
sudo proxyforge cleanup sing-box
sudo proxyforge cleanup all --yes
```

仅在目标内核完全卸载后执行；`all` 要求两个内核都已卸载，并会删除 `/var/lib/proxyforge/` 内的原生管理数据及自定义内容，但保留历史 `docker/` 目录，该目录由独立项目 ProxyDock 管理。单内核清理只删除对应内核的残留。清理不删除 systemd journal 或用户自行导出的客户端文件。

下载与脚本执行的检查范围见[文件与安全边界](security.md#下载与执行边界)。
