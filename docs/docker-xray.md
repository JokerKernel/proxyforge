# Xray（Docker）使用指南

ProxyForge 通过独立的 `internal/docker` 模块管理一个自行创建的 Xray Compose 实例。主界面第 3 项进入 Docker 管理，第 4 项为全局残留清理。原生 Xray 与 sing-box 的配置和命令入口保持独立。

Docker 版不依赖宿主机安装的 Xray，也不要求存在 `xray.service`。使用官方镜像 `ghcr.io/xtls/xray-core`，具体版本拉取成功后固定为 digest；普通启动和配置修改不会自动更新镜像。

## 环境要求

- Linux amd64 或 arm64，root 权限。
- 本机 rootful Docker Engine 和 Docker Compose v2 或更高版本插件（含 v5），daemon 已运行。
- 首版不支持远程 Docker、rootless/userns-remap、Docker Desktop、桥接网络、多实例和接管任意已有容器。

ProxyForge 只检测 Docker/Compose 是否可用，不会自动安装 Docker，也不会执行 Docker 安装脚本。Docker 版不使用原生 app/system/provider/domain 业务代码。

首次准备、升级需要访问官方镜像仓库；选择最新稳定版还需要访问 GitHub API。提供 `--version` 可以直接选择具体版本，但首次仍需要拉取镜像。

## 首次部署

交互模式：进入主界面「Xray（Docker）」后选择「部署/升级」，填写镜像版本、对外地址、监听端口和 REALITY SNI/target。TLS 模式自动生成自签证书和 SNI。

也可使用命令行：

```bash
# 使用自动解析的最新正式版本；地址和域名需替换为实际值
sudo proxyforge docker deploy \
  --server YOUR_SERVER_IP \
  --port 8443 \
  --sni YOUR_REALITY_SNI \
  --target YOUR_REALITY_TARGET:443

# 指定官方镜像版本（VERSION 替换为实际发行版本号）
sudo proxyforge docker deploy --version VERSION \
  --security tls --server YOUR_SERVER_IP --port 8443
```

host 网络共享宿主机端口，因此原生内核、Docker 实例和其他服务必须使用不同的监听端口。REALITY 默认启用专用回落防护；`--standard-config` 可选择普通配置，`--fallback-port` 可指定内部回落端口。

只准备镜像和 Compose：

```bash
sudo proxyforge docker prepare --version VERSION
```

该操作不启动空代理实例。随后通过 Docker 菜单生成配置，或执行 `proxyforge docker config generate`；首次生成有效节点会自动启动。已有停止实例修改配置后保持停止，使用服务入口显式启动。

## 配置与凭据

```bash
sudo proxyforge docker config show
sudo proxyforge docker config validate
sudo proxyforge docker config edit

sudo proxyforge docker config set dns public-cloudflare
sudo proxyforge docker config set log warning
sudo proxyforge docker config set outbound-ip prefer-ipv6
sudo proxyforge docker config set fallback-ip ipv4-only

# 完整生成会覆盖手动配置；默认保留凭据和线路
sudo proxyforge docker config generate --port 8443 --force

# 重置 UUID；REALITY 同时轮换密钥和 short ID
sudo proxyforge docker config reset --yes

# TLS 仅轮换证书；若需同时轮换 UUID，再加 --credentials
sudo proxyforge docker config reset --rotate-certificate --yes
```

DNS 支持 `system`、`public-cloudflare`、`public-google`、`doh-cloudflare`、`doh-google`；地址族策略支持 `prefer-ipv4`、`prefer-ipv6`、`ipv4-only`、`ipv6-only`、`unset`。

编辑器打开候选副本，校验通过后才提交正式文件。受管 UUID、密钥、端口、SNI 与证书引用通过专用入口修改，手动编辑破坏这些字段时会拒绝提交。其他无关 JSON 字段可保留。

切换已有主节点协议且有线路时，需要先移除线路或显式使用 `--drop-links`。`--rotate-credentials` 与 `--rotate-certificate` 可用于生成过程中主动轮换。

## 访问控制与线路

```bash
sudo proxyforge docker config access add white example.com 10.0.0.0/8
sudo proxyforge docker config access add black blocked.example.com --exact
sudo proxyforge docker config access mode whitelist
sudo proxyforge docker config access switch off
sudo proxyforge docker config access list
sudo proxyforge docker config access clear white

# 在落地服务器创建接入并导出不含私钥的 peer 文件
sudo proxyforge docker config landing add upstream \
  --security tls --port 8444 --output /root/upstream-peer.json

# 安全复制 peer 文件到中转服务器，文件权限保持 0600
sudo proxyforge docker config relay add relay-one \
  --peer-file /root/upstream-peer.json

sudo proxyforge docker config landing disable upstream
sudo proxyforge docker config relay enable relay-one
sudo proxyforge docker config relay remove relay-one
```

白名单优先于黑名单和默认私网阻断；`whitelist` 模式只允许白名单，空白名单拒绝代理请求。访问策略关闭时保留名单数据，之后可通过 `switch on` 恢复。规则作用于代理请求目标，REALITY 回落继续使用专用限制。

落地 `reality` 接入共享 REALITY 主入站；独立 TLS 接入使用自己的端口和证书。TLS 主节点新增落地接入时选择 `--security tls`。

## 客户端导出

```bash
sudo proxyforge docker config client --output /root/xray-client.json
sudo proxyforge docker config client --format clash --output /root/mihomo-client.yaml
sudo proxyforge docker config client --name relay-one --format clash
sudo proxyforge docker config landing peer upstream --output /root/upstream-peer.json
```

导出支持主节点、启用的落地接入和中转线路；文件默认以 `0600` 权限创建，覆盖已有导出文件需要 `--force`。服务端配置、客户端和 peer 信息包含凭据，应妥善保存。

## 服务、升级与恢复

```bash
sudo proxyforge docker status
sudo proxyforge docker service start
sudo proxyforge docker service stop
sudo proxyforge docker service restart
sudo proxyforge docker service logs
sudo proxyforge docker service logs --follow

sudo proxyforge docker upgrade --version VERSION
sudo proxyforge docker recover
```

升级先拉取镜像并校验当前配置，再重建容器，保留节点凭据、证书、名单和线路。运行中的实例重建后核验镜像、监听端口和持续运行情况；已停止实例升级后保持停止。

配置或升级失败时恢复旧文件和原运行状态。回滚无法完成会保留恢复记录并显示错误；下次写操作会尝试恢复，也可显式执行 `recover`。不要手动删除 `transaction.json` 或它引用的备份。

## 移除与清理

```bash
sudo proxyforge docker remove --yes
sudo proxyforge docker cleanup --yes
```

`remove` 只移除容器，保留配置、凭据、证书、备份和镜像；重新部署可沿用配置。`cleanup` 只有在确认本实例和临时容器均已移除后，才永久删除自有数据。

全局清理要求两个原生内核均已卸载、Docker 实例及临时容器已移除。Docker daemon 不可达且无法完成检查时拒绝清理。原生模块直接清理也会保留 `/var/lib/proxyforge/docker/`，防止跨模块误删。

不自动删除 Docker Engine、共享镜像或其他容器，不运行全局 prune。

## 文件位置

| 内容 | 路径 |
| --- | --- |
| Compose | `/opt/proxyforge/xray-docker/compose.yaml` |
| 服务配置 | `/opt/proxyforge/xray-docker/config/config.json` |
| 证书 | `/opt/proxyforge/xray-docker/tls/` |
| 自有状态和部署记录 | `/var/lib/proxyforge/docker/xray/` |
| 备份 | `/var/lib/proxyforge/docker/xray/backups/` |
| 事务记录 | `/var/lib/proxyforge/docker/xray/transaction.json` |
| 实例锁 | `/run/proxyforge/xray-docker.lock` |

目录归属由 `owner.json` 标记，部署和节点 JSON 也有独立的实例标识与 schema。管理目录为 `0700`，配置、证书和管理文件为 `0600`。容器以 UID/GID `0:0` 读取只读挂载，仅保留绑定低端口所需的 capability。

Compose 由程序生成并校验摘要；外部直接修改 Compose 后，程序会拒绝按未知定义操作。通过菜单和命令选择版本及修改代理配置。

## 当前验收范围

已提供模拟 Docker 的单元测试、依赖边界检查和原生回归测试。真实 Docker、官方镜像、端到端代理连接及 VM 重启验收需在已安装 Docker 的 Linux 环境完成；开发过程中未自动安装 Docker。

[开发设计](xray-docker-development.md) · [返回文档目录](README.md)
