# Go 原生壳 P0 验证报告

状态：**按修订后的验收范围通过。** WPF 仍为正式版本，原生候选可并行验证。

验证日期：2026-08-09
应用 ID / 互斥体：`CodexFloatingBar.Native.P0`
Go：`go1.26.4 windows/amd64`
系统：Windows 10 Home China 22H2，Build 19045
CGO：正式构建关闭
正式运行时浏览器：无

按用户要求，最终运行界面不显示更新时间、BUILD 或 `C` 图标。开发工作台仍作为
Web 一级入口在页眉显示页面版本；这些字段不会进入导出的四个正式 surface 画面。

## 功能验证

| 项目 | 结果 | 证据 |
| --- | --- | --- |
| 静态工作台 | PASS | 纯 HTML/CSS/JS；主条、统计、提醒和 4 个状态场景可直接调整 |
| 静态资源版本 | PASS | CSS/JS URL 均含 `?v=codexfloatingbar-43fb8bd423dd` |
| 安全导出 | PASS | loopback、Origin、随机 CSRF、JSON 类型、HTML 活动/外部内容限制和大小预算 |
| 原子 UI 包 | PASS | 4 个 surface × 4 档 DPI；新 generation 完成后最后切换 manifest |
| 原生渲染 | PASS | 单个 Go 进程使用 `CreateDIBSection` + `UpdateLayeredWindow` 显示透明 PNG |
| DPI | PASS | 100/125/150/200% 源资源；其他 DPI 精确重采样到目标物理尺寸 |
| 托盘与单实例 | PASS | 精确类名+主窗标题唤醒；v4 与 legacy 托盘通知分流 |
| 位置与收起 | PASS | 负坐标、屏幕移除后 fit、位置保存、8px 边缘收起和动画 |
| 双屏接缝 | PASS | 内侧接缝不会把窗口收进相邻显示器，改选最近外侧边缘 |
| 三窗口 | PASS | 主窗、统计窗、提醒窗共用消息泵；辅助窗隐藏后复用同一 HWND |
| 热加载 | PASS | 同一 manifest 预加载全部已创建 surface，提交失败回滚旧画面 |
| 正常模式网络 | PASS | 不监听 TCP/UDP，不产生浏览器子进程 |

最终便携包做了一次非循环三窗口功能冒烟：

- 主窗 `0x48064e`、统计窗 `0x450dca`、提醒窗 `0x4d0b40` 均成功创建。
- 统计窗收到关闭消息后隐藏，再次打开仍为 `0x450dca`。
- 统计窗与提醒窗贴靠后重叠像素为 0。
- 测试退出后原生进程和 `9315` 工作台端口均为 0。

代码复审还修复了隐藏窗口仍在后台收起、第二实例只唤醒 8px 条、双屏内侧边缘
收起、`PostMessageW` 结果未检查及 `SetTimer` 失败状态未回滚等问题。

## 页面与 UI 包版本

- 工作台页面：`更新 2026-08-09`
- 工作台 BUILD：`BUILD 2026-08-09 17:18:51`
- 静态指纹：`codexfloatingbar-43fb8bd423dd`
- 当前 generation：`assets/d90a2713cc548bcd`
- Manifest：4 个 surface、16 个引用资源
- CSS：`/assets/styles.css?v=codexfloatingbar-43fb8bd423dd`
- JavaScript：`/assets/app.js?v=codexfloatingbar-43fb8bd423dd`

本轮因导出可靠性修复重启了原生工作台服务；没有启动 Wails 或 Vite。页面返回
HTTP 200，浏览器控制台无错误，真实导出显示“4 个 surface，16 个资源”。工作台
只有一个 Web 一级入口，因此跨 Web 页面版本一致性不适用；四个原生 surface 均来自
同一 manifest 版本。验证完成后工作台服务已停止。

最终视觉复核曾发现无头 Edge 的小高度窗口只绘制了页面顶部。导出器现为截图窗口
预留固定高度，再裁剪到 manifest 目标尺寸；集成测试同时校验实际颜色像素，不再只看
PNG 宽高。资源 generation 由最终 PNG 内容计算，因此栅格器输出变化必然生成新目录。
横/竖主条、统计窗和提醒窗的 100%/125%/150% 代表资源已逐图检查，文字、按钮、
进度条和提醒卡均完整，且不含版本文字或 `C` 图标。

## 测试与审查

- `gofmt`：无差异。
- `node --check ui/workbench/app.js`：通过。
- `go test -count=1 ./...`：通过。
- `go vet ./...`：通过。
- Edge 栅格器集成测试：通过。
- Windows 结构尺寸、bundle 限制、精确 DPI、负坐标、辅助贴靠、窗口 role、
  原子导出失败回滚和静态指纹测试：通过。
- 独立代码复审：未发现 P0 级崩溃、错误退出或确定的 GDI 泄漏。

用户明确取消继续执行窗口压力测试和空闲性能回归。因此最后的多窗口构建没有再次
运行 200/50 循环或 5 分钟采样。较早的单主窗参考值为工作集约 16 MiB、私有内存
约 49 MiB、空闲 CPU 远低于 1%、监听端口 0；它们仅作架构参考，不声明为最终构建
的性能回归结果。

## 发布验证

| 产物 | 大小 | SHA-256 |
| --- | ---: | --- |
| `CodexFloatingBar.Native.P0.exe` | 7,090,688 B | `b05bc038b7496693ae423e07ce0f7e39cff353bfa6c7968ad15cffbf7e5d962a` |
| `CodexFloatingBar.Native.P0-win-x64.zip` | 3,218,324 B | `b4c6162f3c07fba8ffd993d10228a2bcc89f7c2b38154e01ed5ad334c5aa455a` |
| `CodexFloatingBar.Native.P0-Setup.exe` | 3,220,657 B | `be447774339355632a16d0a976871daefab44a6308daeabe410b718c22b0ea6e` |

- 便携程序：主 HWND 有效、可见且 PID 匹配，退出码 0。
- NSIS 3.12：静默安装退出码 0。
- 已安装程序：主 HWND 有效、可见且 PID 匹配，退出码 0。
- 静默卸载退出码 0；安装目录、注册表、开始菜单和进程残留均已清理。
- 最终包仅包含 manifest 引用的 16 个 PNG，无旧 generation 或兼容顶层 PNG。

## 后续边界

P0 使用模拟数据，不读取 `.codex`、认证文件或旧 WPF 设置。报告完成时，Go 数据服务、
动态文字/进度渲染、Codex 进程联动、开机启动和正式设置迁移尚待 P2/P3；这些项目现已
完成。P4 正式切换前继续保留 WPF 下载、源码和回滚路径。
