# CPU、读取缓存、工作台导出与动效优化评估

评估日期：2026-08-19

## 结论

本轮保留现有 Go + Win32 分层窗口架构，不引入 WebView、前端运行时或通用动画系统。
实际瓶颈不是统计聚合算法本身，而是活跃 session 每次追加后重复固定尾读、同一刷新内
重复遍历文件树，以及隐藏辅助窗仍参与合成。P0 已按下列边界落地：

- 数据服务每轮只建立一次 source inventory，运行状态、额度和统计共享结果。
- 最近 session 的 Runtime/RateLimit 共用增量尾索引，稳定追加只读取新增字节。
- config、auth、sessions、logs 分字段失效；备用日志按字段和 generation 独立缓存。
- 工作台先做 preflight，冷路径按 4 档 DPI 各渲染一张 atlas，再裁成 56 张资产。
- 工作台提供 `--export-once`，用自有 Edge Job 完成无人值守导出并以退出码报告结果。
- Usage Toast 使用复用已合成位图的窗口级 fade-slide；统计内容不做逐元素动画。
- 隐藏辅助窗延迟合成，隐藏主窗停止自动收起 timer，正式运行默认关闭 manifest 热轮询。

## 优化前证据

本机现有开发进程运行约 20.27 小时，累计 CPU 约 0.739 小时；按 16 个逻辑核归一化
约 0.228%，单核口径约 3.64%。一次 15.1 秒活跃采样中，归一化 CPU 为 0.839%，读取
约 60.3 MiB。

另一段 10.2 秒关联采样中，session JSONL 只追加约 94 KiB，进程却读取 39.76 MiB，
读取放大约 433 倍。本机当时有 346 个 session、总计约 639.46 MiB；统计缓存只有约
327 KiB 且已覆盖这些文件，说明历史统计增量缓存有效，主要热源是活跃 session 的
Runtime/RateLimit 固定尾读和刷新触发链。

代码路径确认了以下重复工作：旧 Monitor 先 Walk/Stat，Service 再次发现并排序 session，
Statistics 又逐文件 Stat；一次活跃 session 变化还会让 Runtime 和 RateLimit 分别读取
固定尾部。已创建但隐藏的统计窗和 Toast 也会随状态刷新重新合成。

以上是关联采样和代码证据，不等同于长期受控能耗测试。

## 读取缓存实现与结果

新的 `sourceInventory` 保存 config/auth/log/session 元数据和文件 identity。Monitor 将同一
inventory 直接传给 Snapshot，Statistics 不再逐文件补 Stat。一次 refresh 只对实际变化
的字段失效；session 主源已提供 Runtime 或 RateLimit 时，日志变化不会触发无用尾读。

每个最近 session 保存安全偏移、partial-line 状态和已解析的 renderer-safe DTO，不保留
原始行。append 只读增量；truncate、同路径原子替换、identity 变化和不安全 partial 会
回退重读。同 stamp 原子替换还会显式失效对应统计 summary。读取按 64 KiB 检查 context，
过期刷新可及时取消。

瞬时 inventory、Open、Read 或 Stat 错误不会提交空结果或污染 cache generation；Monitor
保留上一份已发布快照，并在下一 poll 对同 inventory 节流重试。损坏 JSON 等既有业务
容错仍保持降级语义。

`ReadMetrics` 只记录聚合计数，不记录路径或内容。350 文件、16 个大于 2 MiB 的活跃
session、7 轮约 100 KiB 追加的综合回归结果：

| 指标 | 结果 |
| --- | ---: |
| 实际追加 | 100,170 B |
| 新实现 TailBytes | 100,170 B |
| WalkFiles | 2,450（350 × 7） |
| started / published / canceled | 7 / 7 / 0 |
| 旧固定尾读估算 | 249,561,088 B |
| 尾读降幅 | 99.96% |

新实现明确通过 `≤ 8 MiB / 10 s` 的稳定追加门槛。仍保留每 1.5 秒一次文件树 inventory，
用于可靠发现新增或删除的 session；若后续 CPU 采样证明 Walk 本身成为主热源，再单独
评估目录通知或较慢的全量重扫，不在本轮引入额外状态机。

## 工作台导出

导出前先验证当前 manifest、静态指纹和全部 14 × 4 资产。完全命中时不再构造 56 份
HTML，也不启动 Edge。冷路径按 1、1.25、1.5、2 四档 DPI 生成最多 4 张透明 atlas，
裁图后逐资产验证精确尺寸、强 alpha 和跨 DPI 覆盖；单档 atlas 失败才显式退回逐图
渲染。

最终仓库资产通过 `--export-once` 冷导出：12.1 秒、4 次 Edge atlas 调用、56 个资源
重新渲染、0 fallback；随后 warm preflight 为 2.5 秒、0 个重渲染、56 个复用资源。
56 张裁图全部通过尺寸、中心颜色、alpha 和覆盖验证。每资源固定等待已替换为
10–160 ms 解码退避。若某一档 atlas 在 Edge 瞬态下验证失败，只对该档显式回退逐图
渲染，最终资产仍必须通过同一组验证。

客户端 page version 在入口校验，manifest 切换前再次计算静态 fingerprint；导出过程中
源码变化会拒绝提交，不能把旧 DOM 标成新版本。generation 内容哈希确定排序；已存在
generation 必须逐文件匹配。manifest 写入前后均完整验证，失败恢复旧 manifest；成功后
保留 current + previous 两代资产。上一份 manifest 不持久化，因此这是失败事务回滚，
不是跨重启的一键版本回滚。

服务端从已安装的 Edge 文件读取四段版本、路径、大小和 mtime，并把渲染器指纹写入
每个 64 位 `sourceHash`。浏览器 Client Hints 的 Edge 版本必须与服务端文件版本精确
一致；Edge 升级会让 preflight miss，导出过程中升级会拒绝提交。

`--export-once` 只在显式 CLI 模式下注入自动导出标记。成功回报必须通过 loopback、
Origin 和随机 token 校验，并逐字段匹配服务端已经验证的 preflight/export 结果。Edge
使用独立临时 profile 和 `KILL_ON_JOB_CLOSE` Job；成功、客户端失败、Edge 提前退出或
80 秒超时都会关闭进程树、释放端口并返回明确退出码。普通工作台不注册结果回报端点。

## 动效结论

Usage Toast 的正式动效为 180 ms/6 逻辑像素 fade-slide 显示和 130 ms fade 隐藏，最多
12 帧，4 秒计时从显示完成后开始。动画只更新 HWND 位置和 `ConstantAlpha`，不逐帧
重绘内容；showing/hiding 阶段点击穿透。Windows 关闭动画或远程会话时直接显示终态。

真实不可见 layered HWND 的 100 帧有限基准为 0.772 ms/帧、296 B、13 allocs；像素准备
为 0.372 ms/帧、0 allocs。工作台可预览辅助窗 fade-slide 和统计整窗 crossfade，但导出
强制 freeze 动画并等待双帧稳定。统计热图、折线图和文字逐元素动画本轮仍不采用。

## 最终资产与验证

最终 UI manifest：

| 项目 | 结果 |
| --- | --- |
| schema / project | `2` / `codexfloatingbar` |
| 资产版本 | `更新 2026-08-19`，`BUILD 2026-08-19 15:56:31` |
| 静态指纹 | `codexfloatingbar-d5908c973410` |
| surface / variant / 文件 | 14 / 56 / 56，缺失 0 |
| 当前 generation | `79078914f9c3e9c8` |
| 保留 generation | `431ecec88a3dc850`、`79078914f9c3e9c8` |
| manifest SHA-256 | `5bb22b27f77ebb9e7f8b30b8319654bdf3b7f451410694d7e50920286bb9718a` |

最终正常工作台进程的页面显示 `更新 2026-08-19`、
`BUILD 2026-08-19 15:59:18`；CSS、JavaScript 和页面均使用
`codexfloatingbar-d5908c973410`。HTTP 返回 200，浏览器控制台无错误；开启动效预览并
切换到每周视图后临时 crossfade 层归零，页面未残留 `export-freeze`。

实际执行并通过：

- `go test -count=1 ./...`
- `go test -race -count=1 ./...`
- `go vet ./...`
- `go test -count=1 -run TestIncrementalReadRegression -v ./internal/codexdata`
- `scripts/build-workbench.ps1 -TestOnly`（包含已导出资产验证）
- `node --check ui/workbench/app.js`
- `gofmt -l` 全部 Go 源文件与 `git diff --check`
- 真实 `--export-once` 冷路径、warm preflight 和 loopback 限制
- 工作台浏览器页面、资源指纹、统计切换动效和控制台冒烟
- Windows GUI 验证构建及 PE Header 检查

GUI 验证产物为 `.cache/verify/CodexFloatingBar.exe`，大小 6,245,888 B，PE32+，
Subsystem 为 `2 (Windows GUI)`，SHA-256 为
`d4548696e6f9bd482b3822806f8fbb4e33ff49ba945bca757149856180a789a7`。工作台开发 EXE
大小 10,688,000 B，SHA-256 为
`d4f09b0c3e670c1da95ea5ceb3e31726a42d813e26d2a117d04644e8888a4855`。

## 已知边界与回滚

- session inventory 仍每 1.5 秒执行一次 O(N) 的 `WalkDir + Info`；本轮解决的是内容
  尾读放大，不把 `TailBytes` 误称为总磁盘 I/O。
- 公开 `Service.Snapshot` 与 Monitor 混合并发调用时，理论上仍可能把锁外收集的旧
  inventory 晚于新 inventory 提交；生产路径当前由单个 Monitor 串行调用。
- Monitor 取消后不 join 正在结束的 refresh goroutine；分块 context 读取会限制晚退时间。
- 同一文件若内容被原地改写后恢复完全相同的 size、mtime、mode 和 identity，单进程
  inventory 无法发现；统计缓存跨进程也不能识别同路径、同长度、同 mtime 的替换。
- 导出失败会原子恢复旧 manifest；成功后只有上一代资产仍在目录中。完整版本回滚应从
  版本控制或已保存构建产物同时恢复旧 manifest 和它引用的 generation。

优化评估阶段没有生成便携包、安装包或签名产物；评估冻结后已在后续发布阶段生成
`0.1.0-beta.4` 的未签名便携包和安装包，结果记录在 `docs/native-beta-release.md`。
没有签名证书或独立兼容性机器也不描述为已完成。现有 Go + Win32 架构通过门槛，
因此淘汰了 WebView/浏览器运行时进入正式程序、
通用 CSS 动画解释器和统计图表逐元素动画。目录通知也暂缓：只有后续受控采样证明 O(N)
metadata 扫描成为主热源时才值得引入新的生命周期状态机。

## 未执行与后续门槛

- 未执行窗口压力测试或长期空闲/能耗回归。
- 未在独立机器复核多 GPU、远程桌面和全部 Windows 10/11 组合。
- 未给统计内容、跟随 Codex 显示或主窗手动显示增加新动画。
- 未运行包含窗口压力循环的完整 native self-test。
- 发布包与安装包不属于评估阶段产出，随后已在 beta.4 发布阶段生成并验证；签名、真实
  安装、覆盖升级和卸载回滚仍未执行。

后续最小行动是在下一次真实使用采样中复测 10–15 秒活跃追加的进程 I/O 和归一化 CPU；
只有单次 inventory Walk 仍占主导时，才进入目录级变更通知 P1。任何统计切换动效都应先
复用旧/新已合成整窗位图做一次性 crossfade，不建立通用逐元素动画系统。
