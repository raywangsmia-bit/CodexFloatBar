//go:build windows

package main

import (
	"bytes"
	"image"
	"image/color"
	"testing"
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
