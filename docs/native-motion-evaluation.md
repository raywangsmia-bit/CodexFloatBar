# 原生动效 P0 评估

评估日期：2026-08-10
状态：**窗口级动效建议进入下一步最小原型；内容级通用动效暂不进入正式实现。**

## 当前架构边界

正式程序消费多 DPI PNG、点击区域和受限动态槽，不解析 HTML/CSS，也不包含浏览器
运行时。现有自动收起已用 `SetWindowPos` 完成 10 步、约 160 ms 的窗口位移动画；
动画结束后会停止 Win32 timer。

CSS `transition`、`animation` 和关键帧不会进入当前扁平 PNG/manifest。直接支持任意
CSS 动画需要引入浏览器内核、逐帧位图或完整场景图，都会破坏当前 P0 的内存、包体积
或维护成本边界，因此本轮淘汰。

## 短时帧成本

命令：

```powershell
go test -run '^$' -bench 'Benchmark(ComposeUsageToastFrame|ComposeStatisticsFrame|PrepareUsageToastLayeredPixels)$' -benchmem -benchtime=750ms -count=3 .
```

环境：Windows amd64，AMD Ryzen 7 4800U。该测试是有限次数的微基准，不是窗口压力
测试或长期空闲性能回归。

| 路径 | 结果 | 判断 |
| --- | ---: | --- |
| 提醒窗完整动态合成 | 11.80–12.74 ms/帧，约 186 KiB、47 allocs | 接近 60 FPS 上限，不宜作为常驻逐帧路径 |
| 统计窗完整动态合成 | 56.33–58.78 ms/帧，约 330 KiB、245 allocs | 不通过实时内容动画门槛 |
| 提醒窗像素准备（修复前） | 0.85–0.90 ms/帧，44,352 allocs | 分配不可接受 |
| 提醒窗像素准备（NRGBA 快路径） | 0.21–0.24 ms/帧，0 allocs | 通过窗口级短动画的算法前置门槛 |

## 推荐范围

第一阶段只实现窗口级 `fade-slide`：显示时透明度从 0 到 255，并从 6 个逻辑像素偏移
回目标位置；隐藏时反向。建议 160–200 ms、最多 12 帧，复用已经合成的 surface，
不得逐帧重新生成文字、热图或统计图。

manifest 只增加受限、可校验的可选契约，例如：

```json
{
  "motion": {
    "show": { "kind": "fade-slide", "durationMs": 180, "offsetY": 6 },
    "hide": { "kind": "fade", "durationMs": 140 }
  }
}
```

运行时必须限制动效类型、时长和偏移；动画 timer 完成、取消或窗口销毁时立即释放。
系统关闭 UI 动画、远程会话或用户选择“减少动态效果”时直接显示最终帧。

## 暂不支持

- 统计热图、折线图或全部文字的 60 FPS 重绘。
- 从工作台自动转换任意 CSS keyframes。
- 循环呼吸、闪烁或闲置时持续运行的动效。
- 在未完成真实 HWND 淡入淡出、多 DPI 切换和中途反向测试前默认开启动效。

下一步最小行动是制作一个只针对额度提醒窗的真实 `fade-slide` 原型，测量 12 帧的
实际 `UpdateLayeredWindow`/`SetWindowPos` 开销，并验证中途隐藏、DPI 变化和减少动态
效果开关。该原型通过后，再决定是否把同一契约扩展到主条显示和统计窗显示。
