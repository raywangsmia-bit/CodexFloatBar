# CodexFloatingBar Native P3 功能对等报告

## 结论

P3 的本机实现与普通验证已经完成。Next Beta 继续使用独立应用身份、互斥体、配置、
缓存、启动项和可执行文件，可与 WPF 正式版并存；本阶段没有覆盖或删除 WPF 源码、
设置或构建产物。

按用户明确要求，最终构建不再运行窗口压力循环或 5 分钟空闲性能回归，也没有运行
原生 self-test。验收仅保留普通单元测试、静态检查、生产构建、一次三窗口功能冒烟、
便携启动、两版本安装升级/占用/卸载验证和一次 WPF 回滚启动验证。

## 功能对等

- Codex 进程检测：启动时检测、退出连续确认后隐藏、再次启动时恢复。
- 三窗口：主条、统计窗和额度提醒窗均为原生分层窗口，懒创建、隐藏复用、同一 UI
  线程和同一 bundle generation。
- 主条：主题、横竖布局、统计窗、额度提醒、自动收起和隐藏动作。
- 统计窗：月热图、最近 13 周、近 12 月累计、上一月和下一月导航。
- 托盘：显式显示/隐藏、刷新、复制、主题、布局、缩放、自动收起、开机启动、配置与
  外部链接、退出。
- 持久化：主题、布局、缩放、自动收起、主窗位置和分离后的统计窗位置。
- 迁移：首次无原生设置时只读旧 WPF appearance/placement；之后只使用
  `%LOCALAPPDATA%/CodexFloatingBar.Next/`。
- 隐私：React/JavaScript 不读取 Codex 数据；正式程序不包含 JavaScript 引擎，动态
  层只接收最小化 snapshot。

## 最终 UI bundle

- schema：2
- surface：14
- DPI variant：56
- generation：`67875f0ef1300d7a`
- `更新 2026-08-09`
- `BUILD 2026-08-09 19:58:01`
- 静态资源指纹：`codexfloatingbar-4d5a9b6f7d18`

人工浏览器验证通过 `http://127.0.0.1:9315/` 访问工作台，版本两行、主题、横竖布局、
三种统计视图、月份导航和异常场景均可操作，控制台没有错误。CSS/JavaScript URL 使用
`?v=codexfloatingbar-4d5a9b6f7d18`。主窗、统计窗和提醒窗按用户要求不显示更新、
BUILD 或 `C` 图标，但运行日志确认三窗从同一 manifest、BUILD、指纹和 generation
完成原子重载。

最终导出后没有页面文件变更。导出进程树清理属于开发服务器生命周期修复，已由单
资产 Edge 集成测试覆盖；没有为此改变页面资源或重新生成 BUILD。验证结束后工作台
服务已停止，端口 9315 当前不再监听。

## 普通验证

- `go test -count=1 ./...`：通过。
- `go vet ./...`：通过。
- `node --check ui/workbench/app.js`：通过。
- 生产构建脚本内的全量 `go test ./...`：通过。
- 人工三窗口冒烟：主窗、统计窗、提醒窗各创建一次并观察为可见；日志报告
  `windows=3`，版本元数据一致。
- 人工便携成品冒烟：主 HWND 可见且 PID 匹配，观察到监听端口数为 0，退出码 0。
- Edge 单资产导出：通过；测试前后没有新增 `codexfloatingbar-export-*` 临时目录。
- Edge 以 `CREATE_SUSPENDED` 创建，在恢复前通过真实进程 handle 加入
  `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE` Job；不再按 PID/PPID 扫描或终止进程。
- 历史导出临时目录已精确清理；只结束了带该导出 profile 的 10 个 Edge 进程，没有
  按进程名结束用户浏览器。最终 exporter 临时目录数为 0。

## P4 交接

`0.1.0-beta.1` 便携包、ZIP 和 NSIS 安装包已经生成。本机 Windows 10 22H2 已通过
`0.0.9-beta.1` 到最终 Beta 的覆盖升级、旧 UI 清理、启动项路径更新、运行占用时安装
和卸载返回 32、正常卸载与残留清理；报告位于
`runtime-data/installer-upgrade-verification-20260809-213617-25428/verification.json`。

卸载 Next 后，WPF v0.1.3 也已创建有效可见窗口并通过 PID 核对，用户数据和 WPF
启动项保持不变；报告位于
`runtime-data/wpf-rollback-verification-20260809-212953-8680/verification.json`。
仍未闭环的是代码签名、签名后的 SmartScreen 信誉、独立干净系统复核和 Beta 验收后
正式应用身份切换。WPF 在这些外部门槛通过前继续作为正式版本。

另已生成不依赖开发环境的 72,060,767 B 干净机验收 ZIP，SHA-256 为
`7b48deeff200c7415ba477d2582372dd51212c938f42e325534ad9244034782d`。该包在本机
Windows 10 Build 19045 的临时干净用户状态下完整通过；独立机器/VM 复核仍属于外部
Beta 门槛。
