# 构建、测试与发布

Docker 代理管理已分离为独立的 ProxyDock 仓库。本仓库只构建原生 Xray/sing-box 管理程序，不依赖 ProxyDock 源码、二进制或 Docker daemon。原生清理保留历史 Docker 数据的回归测试仍在本仓库维护。

## 构建

```bash
./scripts/build.sh
go test ./...
```

构建脚本从 Git 自动读取版本和提交号，并注入 UTC 构建时间；`make build` 是快捷入口。也可以显式覆盖元数据：

```bash
VERSION=v1.0.0 \
COMMIT=0123456789abcdef0123456789abcdef01234567 \
BUILD_DATE=2026-08-08T12:00:00Z \
  ./scripts/build.sh

./proxyforge --version
```

不使用构建脚本时，对应命令为：

```bash
VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
COMMIT="${COMMIT:-$(git rev-parse HEAD 2>/dev/null || echo unknown)}"
BUILD_DATE="${BUILD_DATE:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}"

go build -trimpath -ldflags="-s -w \
  -X proxyforge/internal/version.Version=${VERSION} \
  -X proxyforge/internal/version.Commit=${COMMIT} \
  -X proxyforge/internal/version.BuildDate=${BUILD_DATE}" \
  -o proxyforge ./cmd/proxyforge
```

## 测试

Golden、安全和事务回滚测试：

```bash
go test ./...
```

配置序列化的性能基准：

```bash
go test ./internal/provider/xray ./internal/provider/singbox \
  -run '^$' -bench '^BenchmarkMarshal' -benchmem
```

Xray 和 sing-box 各自维护字段排序规则，通过 `jsonutil.MarshalOrdered` 复用序列化机制。未列入排序规则的手工字段仍会保留；新增配置字段无需修改共享序列化代码。

提供官方二进制后，可以运行 REALITY、TLS、客户端和中转配置的原生验收：

```bash
SING_BOX_BIN=/path/to/sing-box XRAY_BIN=/path/to/xray \
  go test -v ./internal/integration
```

自签 TLS 本机连接验收覆盖 Xray/sing-box 两个服务端与 Xray/sing-box/Mihomo 三个客户端，并检查错误指纹无法连接：

```bash
PROXYFORGE_TLS_CONNECT_TEST=1 \
SING_BOX_BIN=/path/to/sing-box XRAY_BIN=/path/to/xray MIHOMO_BIN=/path/to/mihomo \
  go test -v ./internal/integration -run TestRealTLSClientsConnectAndRejectWrongPins
```

测试启动临时内核进程，使用随机本机端口，结束后停止进程。测试配置仅为访问本机测试 HTTPS 服务放行 loopback，不改变生成配置中的私网拦截策略。

访问控制本机验收覆盖域名与子域名、精确域名、单 IP、CIDR、白名单优先于黑名单/私网拦截、空白名单拒绝以及默认行为：

```bash
PROXYFORGE_ACCESS_CONNECT_TEST=1 \
SING_BOX_BIN=/path/to/sing-box XRAY_BIN=/path/to/xray \
  go test -v ./internal/integration -run TestRealAccessPolicy
```

测试使用临时自签 TLS 节点、本机 HTTP 服务和固定测试 DNS 映射，不访问外部网站。部分黑名单/空白名单用例在测试配置中放行 loopback，以单独检验名单规则；其他用例保留默认私网拦截，验证明确白名单例外。测试退出时停止临时内核并删除临时配置。

参数组合验收覆盖标准、回落防护、自签 TLS 和 sing-box 简化模式，并在访问控制、独立 TLS 落地及 REALITY/TLS 中转同时存在时检查五种 DNS 配置、五种出站 IP 策略、回落 IP 策略和日志级别：

```bash
SING_BOX_BIN=/path/to/sing-box XRAY_BIN=/path/to/xray MIHOMO_BIN=/path/to/mihomo \
  go test -v ./internal/integration -run 'TestRealConfigurationSettingsMatrix|TestRealMihomoClientConfigs'
```

回落域名边界验收直接连接生成的内部回落入站，检查完整域名、子域名以及夹带 SNI 的无关域名。仅使用本机 HTTP 测试服务，不依赖公网 REALITY 目标：

```bash
PROXYFORGE_FALLBACK_CONNECT_TEST=1 \
SING_BOX_BIN=/path/to/sing-box XRAY_BIN=/path/to/xray \
  go test -v ./internal/integration -run TestRealFallbackDomainBoundaries
```

一次性 Debian/Ubuntu 或 Rocky/AlmaLinux systemd VM 可以运行 `scripts/smoke-systemd.sh`。该脚本会真实安装两个内核、生成 443/8443 节点、验证客户端并确认服务 active，不应直接用于生产主机。

REALITY SNI 黑盒测试见 [REALITY SNI 检测指南](reality-sni-check.md)。

## CI

GitHub Actions 在分支 push、Pull Request 和手动触发时运行测试、`go vet` 和 Linux amd64/arm64 构建。Dependabot 每周一检查 Go Modules 和 GitHub Actions 更新。

## 发布

创建并推送 `v` 开头的 Git 标签：

```bash
git tag -a v1.0.0 -m "ProxyForge v1.0.0"
git push origin v1.0.0
```

Release 工作流发布：

```text
proxyforge_linux_amd64_v1.0.0
proxyforge_linux_arm64_v1.0.0
version
SHA256SUMS
```

`version` 只包含发布标签和换行，并与二进制一起纳入 `SHA256SUMS`。安装脚本支持当前的 `proxyforge_linux_amd64_v1.0.0` 格式，也兼容早期的 `proxyforge_v1.0.0_linux_amd64`。

发布说明采用组合生成：工作流自动查找当前版本之前的相邻版本标签，用 `git log --no-merges` 生成带提交链接的 Markdown 列表，再由 GitHub 原生 Release Notes 补充合并的 Pull Request、贡献者和完整变更链接。即使采用个人仓库直接推送模式，Release 页面也会包含具体改动；`--notes-start-tag` 保证原生说明使用同一个版本范围。

可以在 GitHub Actions 的 Release 工作流中手动重新发布已有标签。发布任务只在最后阶段获得 `contents: write` 权限，普通 CI 和构建任务保持只读。
