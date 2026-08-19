# 原生动效 P0 评估与实现

评估更新：2026-08-19
状态：**Usage Toast 窗口级动效已通过 P0；统计内容逐元素动画仍不进入正式运行时。**

## 最终范围

正式程序继续消费多 DPI PNG、点击区域和受限动态槽，不解析 HTML/CSS，也不包含浏览器
运行时。本轮只对已经合成的窗口位图做短时窗口级动画：

- Usage Toast 显示：180 ms、6 逻辑像素 fade-slide，含初始帧最多 12 帧。
- Usage Toast 隐藏：130 ms fade-only；alpha 归零后才隐藏 HWND 和收拢窗口栈。
- 主窗自动收起/展开：160 ms 时间推进，允许中途反向；反向时长按剩余距离缩放。
- 统计窗、热图、折线图和文字：不做逐帧重绘。

Toast 使用 `hidden → showing → visible → hiding` 状态机。显示完成后才启动 4 秒可见
计时；动画中途反向从当前坐标和 alpha 继续。showing/hiding 阶段返回
`HTTRANSPARENT`，不会用透明窗口截获底层点击。DPI、主题、资源 reload 和窗口销毁会
把状态收敛到明确终态。

Windows 关闭 client-area animation 或处于远程会话时，程序直接显示最终帧。隐藏主窗
不保留 250 ms 自动收起 timer；正式运行也默认不启动 UI manifest 热加载轮询。

## 性能证据

环境：Windows amd64，短时微基准；未执行窗口压力测试或长期空闲回归。

| 路径 | 当前结果 | 判断 |
| --- | ---: | --- |
| Usage Toast 完整动态合成 | 0.314–0.475 ms/帧，约 181 KiB、5 allocs | 只在内容变化时合成 |
| Statistics 完整动态合成 | 1.616–2.212 ms/帧，约 321 KiB、15 allocs | 可用于离散刷新，不用于逐元素动画 |
| Toast layered 像素准备 | 0.372 ms/帧，0 allocs | 通过窗口级动画门槛 |
| 真实不可见 layered HWND `UpdateLayeredWindow` | 0.772 ms/帧，296 B、13 allocs | 通过 12 帧短动画门槛 |

真实 HWND 数据来自 100 帧有限基准。动画每帧复用已经合成的 surface，只更新位置与
`ConstantAlpha`，不会重新生成文字、热图或统计图。

## 工作台契约

工作台提供可选“动效预览”：辅助面板使用 180/130 ms fade-slide/fade，统计视图使用
120 ms 整窗 crossfade。它只用于设计评估，不会写入 manifest，也不会把 CSS keyframes
带入正式程序。

导出启用 `export-freeze`，强制关闭 animation、transition 和 smooth scrolling；状态
切换后等待双 `requestAnimationFrame` 再测量和序列化。`prefers-reduced-motion` 下工作
台也直接切换最终状态。

## 已验证

- Toast 四态、显示/隐藏反向、alpha、完成计时和账号到期内容恢复。
- showing/hiding 点击穿透，visible 后恢复动作。
- 主窗时间推进、双向反转、剩余距离时长和终点。
- Windows 动画策略、远程会话、DPI/reload/destroy 收敛。
- 统计非月视图不再保留隐形日期点击区，空白区域恢复拖动。
- 真实 layered HWND 100 帧有限基准。
- 普通测试、race 和 `go vet`。

未执行窗口压力测试、长期空闲性能回归、正式 GUI EXE 的完整 native self-test，也未给
统计图表、主窗手动显示或跟随 Codex 显示增加新动画。后续若扩展统计切换，只考虑对
旧/新已合成整窗位图做一次性 crossfade，不引入通用逐元素动画系统。
