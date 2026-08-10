# 构建、测试与发布

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

提供官方二进制后，可以复跑四份配置的原生验收：

```bash
SING_BOX_BIN=/path/to/sing-box XRAY_BIN=/path/to/xray \
  go test -v ./internal/integration
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

可以在 GitHub Actions 的 Release 工作流中手动重新发布已有标签。发布任务只在最后阶段获得 `contents: write` 权限，普通 CI 和构建任务保持只读。
