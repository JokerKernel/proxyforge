# 构建、测试与发布

需要 Go 1.23 或更高版本。本仓库只管理原生 Xray/sing-box，不依赖 ProxyDock 或 Docker daemon。

## 构建

```bash
./scripts/build.sh
./proxyforge --version
```

`make build` 为快捷入口；`make cross` 构建 Linux amd64/arm64，输出到 `dist/`。构建脚本自动注入 Git 版本、提交号和 UTC 时间，可通过环境变量覆盖：

```bash
VERSION=v1.0.0 \
COMMIT=0123456789abcdef0123456789abcdef01234567 \
BUILD_DATE=2026-08-08T12:00:00Z \
OUTPUT=/tmp/proxyforge \
  ./scripts/build.sh
```

## 本地检查

```bash
go test -race ./...
go vet ./...
bash -n scripts/install.sh scripts/install_test.sh scripts/test-reality-sni.sh scripts/test-reality-sni_test.sh
bash scripts/install_test.sh
bash scripts/test-reality-sni_test.sh
```

修改配置序列化时可运行性能基准：

```bash
go test ./internal/provider/xray ./internal/provider/singbox \
  -run '^$' -bench '^BenchmarkMarshal' -benchmem
```

两个内核各自维护字段排序规则，共用 `jsonutil.MarshalOrdered`；未列入排序规则的手工字段仍会保留。

## 菜单导航

所有菜单的 `0/q`（含大写 `Q`）返回直接上一级；确认页取消后回到对应选项页，配置、状态或操作结果显示后的回车也回到该选项页。只有顶层内核选择页的 `0/q` 退出程序。SNI 候选页使用 `m` 手动输入，`0/q` 保持返回含义。

导航回归测试使用模拟终端逐字输入，检查返回层级、结果暂停次数、父菜单剩余输入以及取消后配置不变：

```bash
go test ./internal/cli -run 'TestMenusReturn|TestClientMenuReturns|TestAccessMenuReturns'
```

## 原生集成测试

指定官方二进制后运行配置验收，覆盖 REALITY/TLS、客户端、中转、访问控制和设置组合：

```bash
SING_BOX_BIN=/path/to/sing-box XRAY_BIN=/path/to/xray MIHOMO_BIN=/path/to/mihomo \
  go test -v ./internal/integration
```

连接验收需要额外启用对应场景：

| 环境变量 | 场景 | 测试筛选 |
| --- | --- | --- |
| `PROXYFORGE_TLS_CONNECT_TEST=1` | TLS 客户端连接及错误指纹拒绝 | `TestRealTLSClientsConnectAndRejectWrongPins` |
| `PROXYFORGE_ACCESS_CONNECT_TEST=1` | 域名、IP/CIDR、目标端口、名单优先级和空白名单 | `TestRealAccessPolicy` |
| `PROXYFORGE_FALLBACK_CONNECT_TEST=1` | 回落域名、子域名及 Host/SNI 边界 | `TestRealFallbackDomainBoundaries` |

例如：

```bash
PROXYFORGE_TLS_CONNECT_TEST=1 \
SING_BOX_BIN=/path/to/sing-box XRAY_BIN=/path/to/xray MIHOMO_BIN=/path/to/mihomo \
  go test -v ./internal/integration -run TestRealTLSClientsConnectAndRejectWrongPins
```

缺少所需二进制或启用参数时，相应用例会跳过；测试包通过不代表真实内核验收已执行。连接测试使用临时进程和本机端口，退出时清理；仅测试配置按需放行 loopback，不改变生产模板。

`scripts/smoke-systemd.sh` 会真实安装内核、生成 443/8443 节点并检查服务，只应在一次性 systemd VM 运行。外部黑盒检测见 [REALITY SNI 检测指南](reality-sni-check.md)。

## CI 与发布

CI 在 push、Pull Request 和手动触发时运行只读 Go 格式检查、race、vet、Shell 检查及脚本测试，并构建 Linux amd64/arm64。

推送 `v` 开头的标签触发 Release：

```bash
git tag -a v1.0.0 -m "ProxyForge v1.0.0"
git push origin v1.0.0
```

发布产物为 `proxyforge_linux_amd64_<标签>`、`proxyforge_linux_arm64_<标签>`、`version` 和 `SHA256SUMS`。`version` 包含标签和换行，并参与校验。发布说明自动汇总提交与 GitHub Release Notes；可手动重新发布已有标签。
