# CodexFloatingBar Native P2 数据服务报告

## 结论

P2 的 Go 数据服务、C#/Go fixture 对照和无 WebView 动态渲染链路已经实现。
正式运行时仍是单个 Go 进程：HTML/CSS 只在设计时导出透明 PNG 和受限动态槽，
运行时不会解析 HTML，也不会启动浏览器、HTTP 服务、Node、.NET 或 WebView2。

按用户明确要求，本阶段不再运行窗口压力测试或空闲性能回归；最终多窗口构建没有
重跑 200/50 次循环或 5 分钟空闲采样，这些项目不作为本轮门槛。WPF 继续作为正式
版本，原生程序仍使用独立身份和独立缓存目录。

## 模块与数据流

| 模块 | 功能 | 正式运行开销 | 可否剪除 |
| --- | --- | --- | --- |
| `internal/codexdata/config.go` | 读取模型、推理强度和速率配置 | 启动/变更时读取一个小文件 | 可剪；会失去 session 缺字段时的回退 |
| `account.go` | 读取脱敏账户显示名 | 启动/变更时读取一个小文件 | 可剪；当前 UI 未显示账户，不影响额度和统计 |
| `session.go` | 从最近 session/log 选择运行模型状态 | 最多读取每个候选文件 2 MiB 尾部 | 不建议；主条动态模型状态依赖它 |
| `rate_limit.go` | 选择额度窗口、重置时间和计划 | 最多读取 8 MiB 日志尾部 | 不建议；主条额度与提醒依赖它 |
| `statistics.go` | 日/周/月 Token 聚合与缓存 | 首次扫描 session，之后按 mtime 复用缓存 | 可剪；只会移除统计窗口动态数据 |
| `monitor.go` | 1.5 秒 mtime 检查、合并刷新和取消旧任务 | 一个 ticker、按变化刷新 | 可剪；改为手动刷新后数据不会自动更新 |
| `presentation.go` | 将安全 snapshot 格式化为文字、进度和 42 格热图 | 每次变更生成小型 map/slice | 不可剪；它是数据与 UI 包之间的稳定契约 |
| `surface_compositor_windows.go` | 把动态层合成到干净 PNG 底图 | 仅数据或 UI 变化时执行 | 不可剪；删除后只能显示模拟静态数据 |
| `text_mask_windows.go` | 用 GDI 栅格化 Unicode 文字 | 变更时创建短生命周期 GDI 对象 | 不可剪；除非把文字预渲染为固定图片 |
| `status_runtime_windows.go` | 后台 Monitor 到 Win32 UI 线程的桥接 | 两个等待型 goroutine，无网络 | 不可剪；删除后数据服务无法驱动窗口 |

```text
~/.codex/config.toml、auth.json、sessions、logs/WAL
  -> mtime 指纹变化
  -> Go Service 生成不含 JWT/原始日志的 AppSnapshot
  -> wmNativeStatusChanged 投递到 Win32 UI 线程
  -> presentation + compositor 从干净底图重绘
  -> 主条、统计窗、提醒窗使用同一快照更新
```

## 数据与隐私边界

- React/JavaScript 不接触任何本机 Codex 数据；正式程序没有 JavaScript 引擎。
- JWT、`auth.json` 原文、session 行和日志正文不会进入动态槽或窗口 manifest。
- 统计缓存写入 `%LOCALAPPDATA%/CodexFloatingBar.Next/`，不覆盖 WPF 缓存。
- auth、config、日志、manifest 和 PNG 均设有文件/像素上限；损坏内容降级为空状态。
- 主窗口、统计窗口和提醒窗口每次都从同一 generation 的干净 PNG 重新合成，避免
  文字叠加或跨窗口数据版本不一致。

## C# 行为契约

迁移前使用一次性 `net8.0` oracle 直接链接五个 WPF 数据服务源码，并以同一套
脱敏 fixture 和固定时钟生成 canonical golden。迁移后只保留冻结的 golden、来源
指纹和 fixture 指纹；普通 Go 测试不再依赖 WPF 源码或 .NET SDK。

已对照配置读取、JWT 身份解码、单 session 状态、额度候选/摘要、Token 聚合以及
最终 `AppSnapshot`。由于现有 WPF 服务把用户目录写在静态字段中，oracle 对账户、
session 和额度复用原私有解析方法，但不验证硬编码路径发现；路径选择由独立 Go
测试覆盖。

为避免损坏 WAL 造成无界时间或内存开销，Go 版有意增加以下安全上限，因此不承诺
在恶意构造输入上逐字节等价：最多保留末尾 1024 个额度候选、单 JSON 对象最多
4 MiB、单 marker 最多回溯 256 个左花括号。正常 fixture 与 WPF 输出一致。

## 验证

- `go test`：常规主包、动态槽、compositor、数据服务和 C# contract 全部通过。
- `go vet ./...`：通过。
- `go build ./...`：通过。
- `node --check ui/workbench/app.js`：通过。
- C# oracle golden SHA-256：
  `83031ab8b824d64d55097a1f6605c4e144cd62710cb84fab112bca0ae4c194ea`。
- 未运行窗口压力、空闲性能或重复显隐循环；最终构建也未运行原生 self-test。

## 页面版本与静态资源

本次工作台服务启动版本：

- `更新 2026-08-09`
- `BUILD 2026-08-09 19:58:01`
- 静态资源指纹：`codexfloatingbar-4d5a9b6f7d18`
- UI generation：`67875f0ef1300d7a`
- 导出资源：14 个 surface × 4 档 DPI，共 56 张 PNG

开发工作台是唯一一级 Web 入口，CSS/JavaScript URL 使用同一
`?v=codexfloatingbar-4d5a9b6f7d18`。正式三个原生窗口按用户要求不显示更新、
BUILD 或 `C` 图标；版本只保存在工作台页眉和 UI bundle 元数据中。

## 后续阶段

P3 的 Codex 进程联动、完整托盘命令、剪贴板/外部链接、外观设置、统计三视图与旧
WPF 设置只读迁移已经完成，并通过本机普通三窗口冒烟。P4 已生成 Next Beta 便携包
和 NSIS 安装包，并在 Windows 10 22H2 通过两版本覆盖升级、运行占用退出码 32、
卸载与 WPF 回滚启动；签名、SmartScreen、独立干净环境复核与正式应用 ID 切换仍待
外部 Beta 验收。
