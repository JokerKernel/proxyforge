# ProxyForge 首轮优化实施报告

本轮采用最小必要变更，集中修复配置恢复、错误处理和文件操作中已确认的问题。修改保留在工作区，未创建提交，未操作生产服务或安装内核。

## 实际修改文件

下列路径均相对于仓库根目录。

| 分类 | 文件 |
| --- | --- |
| 配置恢复与错误处理 | `internal/app/recovery.go`（新增）、`internal/app/config_io.go`、`internal/app/generate.go`、`internal/app/service_user.go` |
| 恢复与文件校验回归测试 | `internal/app/recovery_test.go`（新增）、`internal/app/config_io_test.go`、`internal/app/service_user_test.go` |
| DNS 依赖与测试 | `internal/app/app.go`、`internal/app/config_status.go`、`internal/app/config_status_test.go`、`internal/system/dns.go`、`internal/system/dns_test.go` |
| 共享日志对象解析 | `internal/provider/jsonutil/jsonutil.go`、`internal/provider/jsonutil/jsonutil_test.go`（新增）、`internal/provider/xray/log.go`、`internal/provider/singbox/log.go` |
| 原子写入与备份 | `internal/system/files.go`、`internal/system/files_test.go`（新增） |
| 无效测试代码清理 | `internal/app/helpers_test.go`、`internal/app/generate_test.go` |
| CI 与文档 | `.github/workflows/ci.yml`、`docs/development.md`、`docs/optimization-report.md`（新增） |

## 修复的问题及技术方案

- **回滚丢失原始错误、忽略服务恢复失败。** 使用 `%w` 和 `errors.Join` 保留操作失败与恢复失败，支持 `errors.Is/As`；恢复文件失败后不重启服务，恢复重启后检查 active 状态。只有恢复步骤成功才返回成功说明。
- **请求取消阻断恢复。** 恢复服务命令使用 `context.WithoutCancel` 保留上下文值并脱离原请求取消信号，再设置 30 秒超时并释放上下文。配置生成前临时停止服务的恢复，以及运行用户迁移的恢复，也使用这一机制。
- **空错误被 `%w` 包装。** 统一 active 状态检查，底层无错误但服务未 active 时创建明确的状态错误，消除 `%!w(<nil>)`。
- **服务用户 GID 转换错误被忽略。** 在任何权限修改前检查转换结果及有效范围，拒绝负值、溢出值与 Linux 无效 GID 哨兵值。
- **失败备份留下不完整文件。** 复制、同步或关闭失败后删除本次不完整备份；仅清理本次新建的失败目录，保留已有目录。失败不触发旧备份裁剪，复制失败后跳过无用同步，清理失败与主错误一并返回。
- **文件操作错误缺少上下文、临时文件清理错误被忽略。** 为原子替换、备份和临时校验文件添加操作及路径信息，并报告清理失败。保留文件关闭和同目录原子替换顺序。
- **两个内核重复解析日志 JSON。** 在既有 `jsonutil` 包提取可选对象解析，保留各 provider 的日志级别、默认值和字段映射，以及手工配置字段。
- **测试依赖可变全局状态。** DNS 查询通过 `App.ResolverAddresses` 实例字段注入，nil 时使用系统查询；系统 DNS 路径改为常量，测试通过私有函数参数传入临时路径。
- **无操作测试代码和格式检查缺口。** 删除空的 `fakeRunner.close` 及调用，CI 增加只读 `gofmt` 检查，沿用现有日志入口和模块分层，不新增通用事务框架。

## 执行的测试及结果

| 检查 | 结果 |
| --- | --- |
| `go test ./...` | 通过 |
| `go test -race ./...` | 通过，无 race 报告 |
| `go test -race ./internal/system` | 最后调整备份失败同步路径后复测通过 |
| `go vet ./...` | 通过 |
| Go 格式检查、`git diff --check` | 通过 |
| 四个安装/SNI Shell 脚本的 `bash -n` | 通过 |
| `bash scripts/install_test.sh` | 通过，输出 `install_test: ok` |
| `bash scripts/test-reality-sni_test.sh` | 通过，输出 `test-reality-sni: ok` |
| CI 中缺失 PATH 的安装失败检查 | 通过，返回非零状态且包含预期提示 |
| Linux amd64/arm64 构建 | 通过，产物写入 `/tmp/proxyforge-check-linux-{amd64,arm64}`，经 `file` 确认架构及静态链接 |
| `go test -v ./internal/integration` | 测试包通过，但真实内核及连接场景均因缺少二进制或启用参数跳过 |

新增回归覆盖两个内核的配置生成和设置修改，验证原始与恢复错误可追踪、空错误状态提示、取消后的恢复、恢复文件失败时禁止重启、恢复重启失败及未 active 状态；另覆盖临时文件清理失败、非法 GID 不改变元数据、备份失败保留旧备份、JSON 边界及并行 DNS 实例隔离。原有 provider 测试继续验证修改日志级别时保留其他配置。

基线测试在沙箱内曾因本机端口限制失败，后续在沙箱外复测通过；部分修改后的检查也需要沙箱外可写的 Go 构建缓存。未降低或跳过相应用例来规避限制。

GitHub Actions 工作流尚未在远端触发；以上结果来自本地执行。

## 兼容性与回滚

- CLI 参数、状态 schema、生成配置字段顺序、内核日志默认值及安装信任规则保持兼容，无需数据迁移或升级依赖。
- 失败提示会包含更多上下文，并保留错误链；非法 GID 和清理失败会明确报错。依赖精确错误字符串的外部脚本需要检查这些失败提示的变化。
- 新增接口仅位于 `internal`：`App.ResolverAddresses` 使用 nil 默认回退，`jsonutil.ParseOptionalObject` 供两个 provider 共用。
- 原子写入成功 rename 后仍视为已提交，目录同步保持尽力执行，避免调用方将已提交写入误判为尚未替换。
- 本轮修改不改变持久化格式，可以回退本次补丁恢复旧实现；验证构建没有覆盖仓库内的现有二进制。

## 未解决的问题与后续优化

- 未执行真实 systemd 主机冒烟测试，以及 Xray、sing-box、Mihomo 的配置和连接验收；后续需在一次性 systemd VM 中提供二进制及相应启用参数后运行。
- 本轮不提供跨进程配置写入锁，也不解决多文件更新中进程崩溃或断电造成的不一致。目录同步仍为尽力执行，不能据此宣称完整的断电持久性。
- 保留现有部署路径兼容策略，没有全面禁止符号链接，也没有完成整条路径的防替换竞态改造。
- 备份清理测试实际触发复制失败；磁盘同步、关闭失败和掉电场景尚无独立故障注入验收。
- 性能方面仅跳过失败复制后的无用同步，未做性能基准，不宣称吞吐或延迟提升。后续性能重构应先测量配置生成、序列化和探测流程。
- 这是局部错误规范收敛，未对整个仓库所有历史错误及日志格式进行重写。

后续有必要优先补齐真实内核/systemd 验收，再根据多进程使用需求推进写入锁和崩溃恢复；其他架构及性能改动以具体缺陷和测量结果为依据。
