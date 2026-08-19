//go:build windows

package main

import (
	"bytes"
	"image"
	"image/color"
	"os"
	"testing"
	"time"
	"unsafe"
)

type genericImage struct {
	image.Image
}

func TestCopyPremultipliedBGRAFastPathMatchesGeneric(t *testing.T) {
	source := image.NewNRGBA(image.Rect(-2, -1, 2, 2))
	values := []color.NRGBA{
		{R: 255, G: 128, B: 1, A: 255},
		{R: 255, G: 128, B: 1, A: 128},
		{R: 19, G: 201, B: 77, A: 37},
		{R: 1, G: 2, B: 3, A: 0},
	}
	for y := source.Bounds().Min.Y; y < source.Bounds().Max.Y; y++ {
		for x := source.Bounds().Min.X; x < source.Bounds().Max.X; x++ {
			index := (y-source.Bounds().Min.Y)*source.Bounds().Dx() +
				x - source.Bounds().Min.X
			source.SetNRGBA(x, y, values[index%len(values)])
		}
	}

	fast := make([]byte, source.Bounds().Dx()*source.Bounds().Dy()*4)
	reference := make([]byte, len(fast))
	copyPremultipliedBGRA(fast, source)
	copyPremultipliedBGRA(reference, genericImage{Image: source})
	if !bytes.Equal(fast, reference) {
		t.Fatalf("fast BGRA conversion differs:\nfast %v\nwant %v", fast, reference)
	}
}

func TestLayeredBlendUsesRequestedConstantAlpha(t *testing.T) {
	for _, alpha := range []byte{0, 1, 127, 255} {
		blend := layeredBlendFunction(alpha)
		if blend.Operation != acSrcOver || blend.AlphaFormat != acSrcAlpha ||
			blend.ConstantAlpha != alpha {
			t.Fatalf("alpha %d produced %+v", alpha, blend)
		}
	}
}

func TestUsageToastVisibilityStateMachineIncludesReversal(t *testing.T) {
	state := auxiliaryHidden
	state = transitionToastVisibility(state, toastShowRequested)
	if state != auxiliaryShowing {
		t.Fatalf("show request from hidden = %d", state)
	}
	state = transitionToastVisibility(state, toastHideRequested)
	if state != auxiliaryHiding {
		t.Fatalf("hide reversal from showing = %d", state)
	}
	state = transitionToastVisibility(state, toastShowRequested)
	if state != auxiliaryShowing {
		t.Fatalf("show reversal from hiding = %d", state)
	}
	state = transitionToastVisibility(state, toastAnimationCompleted)
	if state != auxiliaryVisible {
		t.Fatalf("show completion = %d", state)
	}
	state = transitionToastVisibility(state, toastHideRequested)
	state = transitionToastVisibility(state, toastAnimationCompleted)
	if state != auxiliaryHidden {
		t.Fatalf("hide completion = %d", state)
	}
}

func TestMainWindowAnimationUsesElapsedTimeAndReversesContinuously(t *testing.T) {
	startedAt := time.Date(2026, time.August, 19, 1, 0, 0, 0, time.UTC)
	start := geometryPoint{X: -400, Y: 20}
	target := geometryPoint{X: 0, Y: 20}
	animation := windowAnimation{
		active:    true,
		start:     start,
		target:    target,
		startedAt: startedAt,
		duration:  mainWindowAnimationDuration,
	}
	middle, complete := windowAnimationFrame(
		animation,
		startedAt.Add(mainWindowAnimationDuration/2),
	)
	if complete || middle == start || middle == target {
		t.Fatalf("middle frame = %+v complete=%t", middle, complete)
	}
	if repeated, _ := windowAnimationFrame(
		animation,
		startedAt.Add(mainWindowAnimationDuration/2),
	); repeated != middle {
		t.Fatalf("same timestamp moved from %+v to %+v", middle, repeated)
	}

	reversed := windowAnimation{
		active:    true,
		start:     middle,
		target:    start,
		startedAt: startedAt.Add(mainWindowAnimationDuration / 2),
		duration: scaledWindowAnimationDuration(
			middle,
			start,
			target,
			start,
			mainWindowAnimationDuration,
		),
	}
	if reversed.duration != 140*time.Millisecond {
		t.Fatalf("reverse duration = %s, want 140ms", reversed.duration)
	}
	fullDistance := windowAnimationDistance(target, start)
	remainingDistance := windowAnimationDistance(middle, start)
	if int64(remainingDistance)*int64(mainWindowAnimationDuration) !=
		int64(fullDistance)*int64(reversed.duration) {
		t.Fatal("reverse changed average movement speed")
	}
	if first, _ := windowAnimationFrame(reversed, reversed.startedAt); first != middle {
		t.Fatalf("reverse jumped from %+v to %+v", middle, first)
	}
	if final, done := windowAnimationFrame(
		reversed,
		reversed.startedAt.Add(reversed.duration),
	); !done || final != start {
		t.Fatalf("reverse final = %+v complete=%t", final, done)
	}
}

func TestUsageToastMotionDurationsFramesAndAlpha(t *testing.T) {
	if usageToastShowDuration != 180*time.Millisecond {
		t.Fatalf("show duration = %s", usageToastShowDuration)
	}
	if usageToastHideDuration < 120*time.Millisecond ||
		usageToastHideDuration > 140*time.Millisecond {
		t.Fatalf("hide duration = %s", usageToastHideDuration)
	}
	if usageToastVisibleDuration != 4*time.Second {
		t.Fatalf("visible duration = %s", usageToastVisibleDuration)
	}
	showFrames := 1 +
		(usageToastShowDuration+usageToastAnimationTick-1)/usageToastAnimationTick
	if showFrames > 12 {
		t.Fatalf("show animation requires %d timer frames", showFrames)
	}

	startedAt := time.Date(2026, time.August, 19, 1, 0, 0, 0, time.UTC)
	animation := toastWindowAnimation{
		active:         true,
		startedAt:      startedAt,
		duration:       usageToastShowDuration,
		startPosition:  geometryPoint{X: 100, Y: 94},
		targetPosition: geometryPoint{X: 100, Y: 100},
		startAlpha:     0,
		targetAlpha:    255,
	}
	position, alpha, done := toastAnimationFrame(animation, startedAt)
	if done || position != animation.startPosition || alpha != 0 {
		t.Fatalf("first frame = %+v alpha=%d done=%t", position, alpha, done)
	}
	position, alpha, done = toastAnimationFrame(
		animation,
		startedAt.Add(usageToastShowDuration),
	)
	if !done || position != animation.targetPosition || alpha != 255 {
		t.Fatalf("final frame = %+v alpha=%d done=%t", position, alpha, done)
	}
}

func TestUsageToastReverseStartsAtCurrentProgress(t *testing.T) {
	startedAt := time.Date(2026, time.August, 19, 1, 0, 0, 0, time.UTC)
	hiding := toastWindowAnimation{
		active:         true,
		startedAt:      startedAt,
		duration:       usageToastHideDuration,
		startPosition:  geometryPoint{X: 100, Y: 100},
		targetPosition: geometryPoint{X: 100, Y: 100},
		startAlpha:     255,
		targetAlpha:    0,
	}
	now := startedAt.Add(usageToastHideDuration / 2)
	currentPosition, currentAlpha, _ := toastAnimationFrame(hiding, now)
	finalPosition, finalAlpha, done := toastAnimationFrame(
		hiding,
		startedAt.Add(usageToastHideDuration),
	)
	if !done || finalPosition != hiding.targetPosition || finalAlpha != 0 {
		t.Fatalf("hide final = %+v alpha=%d done=%t", finalPosition, finalAlpha, done)
	}
	reversed := toastWindowAnimation{
		active:         true,
		startedAt:      now,
		duration:       scaledAlphaDuration(usageToastShowDuration, currentAlpha, 255),
		startPosition:  currentPosition,
		targetPosition: currentPosition,
		startAlpha:     currentAlpha,
		targetAlpha:    255,
	}
	position, alpha, done := toastAnimationFrame(reversed, now)
	if done || position != currentPosition || alpha != currentAlpha {
		t.Fatalf("reverse first frame = %+v alpha=%d done=%t", position, alpha, done)
	}
	if reversed.duration >= usageToastShowDuration {
		t.Fatalf("reverse duration %s did not preserve current progress", reversed.duration)
	}
}

func TestToastShowOffsetFollowsDockDirection(t *testing.T) {
	anchor := windowGeometry{X: 100, Y: 100, Width: 400, Height: 80}
	toast := geometrySize{Width: 300, Height: 100}
	below := geometryPoint{X: 200, Y: 188}
	if start := toastShowStartPosition(below, anchor, toast, 6); start.Y != 182 {
		t.Fatalf("below start = %+v", start)
	}
	left := geometryPoint{X: -208, Y: 100}
	if start := toastShowStartPosition(left, anchor, toast, 6); start.X != -202 {
		t.Fatalf("left start = %+v", start)
	}
}

func TestAnimationPolicyHonorsReducedMotionAndRemoteSession(t *testing.T) {
	if animationPolicyAllows(false, false) {
		t.Fatal("reduced-motion setting still allowed animation")
	}
	if animationPolicyAllows(true, true) {
		t.Fatal("remote session still allowed animation")
	}
	if !animationPolicyAllows(true, false) {
		t.Fatal("local enabled animation was rejected")
	}
}

func BenchmarkComposeUsageToastFrame(b *testing.B) {
	benchmarkComposeSurface(b, "usage-toast")
}

func BenchmarkComposeStatisticsFrame(b *testing.B) {
	benchmarkComposeSurface(b, "statistics")
}

func BenchmarkPrepareUsageToastLayeredPixels(b *testing.B) {
	surface, err := loadRenderedSurfaceAtScale("ui/dist", "usage-toast", 1)
	if err != nil {
		b.Fatal(err)
	}
	bounds := surface.Image.Bounds()
	pixels := make([]byte, bounds.Dx()*bounds.Dy()*4)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		copyPremultipliedBGRA(pixels, surface.Image)
	}
}

func BenchmarkUpdateUsageToastLayeredWindow(b *testing.B) {
	if os.Getenv("CODEX_RUN_LAYERED_WINDOW_BENCHMARK") != "1" {
		b.Skip("set CODEX_RUN_LAYERED_WINDOW_BENCHMARK=1 for the real HWND benchmark")
	}
	surface, err := loadRenderedSurfaceAtScale("ui/dist", "usage-toast", 1)
	if err != nil {
		b.Fatal(err)
	}
	className := utf16Pointer("STATIC")
	window, _, lastErr := procCreateWindowExW.Call(
		wsExLayered|wsExToolWindow,
		uintptr(unsafe.Pointer(className)),
		0,
		wsPopup,
		0,
		0,
		signed(int32(surface.Variant.Width)),
		signed(int32(surface.Variant.Height)),
		0,
		0,
		0,
		0,
	)
	if window == 0 {
		b.Fatal(callError("CreateWindowExW(STATIC)", lastErr))
	}
	b.Cleanup(func() {
		procDestroyWindow.Call(window)
	})
	b.ReportAllocs()
	b.ResetTimer()
	for index := range b.N {
		alpha := byte(index % 256)
		if err := updateLayeredWindowWithAlpha(
			window,
			surface.Image,
			geometryPoint{},
			alpha,
		); err != nil {
			b.Fatal(err)
		}
	}
}

func benchmarkComposeSurface(b *testing.B, surfaceID string) {
	b.Helper()
	surface, err := loadRenderedSurfaceAtScale("ui/dist", surfaceID, 1)
	if err != nil {
		b.Fatal(err)
	}
	presentation := benchmarkMotionPresentation()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := composeRenderedSurfaceWithPresentation(
			surface,
			presentation,
		); err != nil {
			b.Fatal(err)
		}
	}
}

func benchmarkMotionPresentation() uiPresentation {
	return uiPresentation{
		Text: map[string]string{
			"runtime.model":             "gpt-5.6-codex",
			"runtime.effort":            "高",
			"runtime.speed":             "快速",
			"quota.remaining":           "34%",
			"quota.reset":               "8/16 04:33 重置",
			"statistics.total":          "18.4M",
			"statistics.peak":           "892K",
			"statistics.duration":       "4小时 26分",
			"statistics.currentStreak":  "12 天",
			"statistics.longestStreak":  "31 天",
			"statistics.month":          "2026 年 8 月",
			"statistics.previousMonth":  "‹",
			"statistics.nextMonth":      "›",
			"statistics.viewMonth":      "月热图",
			"statistics.viewWeek":       "每周",
			"statistics.viewCumulative": "累计",
			"toast.title":               "一周额度不高于 60%",
			"toast.message":             "当前剩余 34%，用量进入提醒区间。",
		},
		Progress: map[string]int{"quota.progress": 34},
		Cells: map[string][]int{
			"statistics.monthCells": {
				0, 1, 2, 3, 4, 0, 1,
				2, 3, 4, 0, 1, 2, 3,
				4, 0, 1, 2, 3, 4, 0,
				1, 2, 3, 4, 0, 1, 2,
				3, 4, 0, 1, 2, 3, 4,
				0, 1, 2, 3, 4, 0, 1,
			},
		},
		Tone:           quotaToneWarn,
		StatisticsView: statisticsViewMonth,
		ChartValues:    []int64{},
	}
}
