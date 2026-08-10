//go:build windows

package main

import (
	"image"
	"image/color"
	"image/draw"
	"testing"
	"time"

	"github.com/raywangsmia-bit/CodexFloatBar/internal/codexdata"
)

func TestPresentSnapshotQuotaToneAndText(t *testing.T) {
	location := time.FixedZone("test-utc+8", 8*60*60)
	refreshedAt := time.Date(2026, time.August, 9, 12, 0, 0, 0, location)
	resetAt := time.Date(2026, time.August, 16, 12, 0, 0, 0, location).Unix()
	tests := []struct {
		name          string
		state         codexdata.SourceState
		remaining     int
		wantTone      quotaTone
		wantProgress  int
		wantRemaining string
		wantReset     string
		wantTitle     string
		wantMessage   string
	}{
		{
			name:          "offline",
			state:         codexdata.SourceMissing,
			wantTone:      quotaToneOffline,
			wantRemaining: "--",
			wantReset:     "等待用量记录",
			wantTitle:     "等待额度记录",
			wantMessage:   "Codex 产生用量记录后将在这里显示。",
		},
		{
			name:          "good boundary",
			state:         codexdata.SourceAvailable,
			remaining:     61,
			wantTone:      quotaToneGood,
			wantProgress:  61,
			wantRemaining: "61%",
			wantReset:     "8/16 12:00 重置",
			wantTitle:     "一周额度状态良好",
			wantMessage:   "当前剩余 61%，暂无额度压力。",
		},
		{
			name:          "warn upper boundary",
			state:         codexdata.SourceAvailable,
			remaining:     60,
			wantTone:      quotaToneWarn,
			wantProgress:  60,
			wantRemaining: "60%",
			wantReset:     "8/16 12:00 重置",
			wantTitle:     "一周额度不高于 60%",
			wantMessage:   "当前剩余 60%，用量进入提醒区间。",
		},
		{
			name:          "warn lower boundary",
			state:         codexdata.SourceAvailable,
			remaining:     11,
			wantTone:      quotaToneWarn,
			wantProgress:  11,
			wantRemaining: "11%",
			wantReset:     "8/16 12:00 重置",
			wantTitle:     "一周额度不高于 60%",
			wantMessage:   "当前剩余 11%，用量进入提醒区间。",
		},
		{
			name:          "danger boundary",
			state:         codexdata.SourceAvailable,
			remaining:     10,
			wantTone:      quotaToneDanger,
			wantProgress:  10,
			wantRemaining: "10%",
			wantReset:     "8/16 12:00 重置",
			wantTitle:     "一周额度快用完了",
			wantMessage:   "当前剩余 10%，建议放慢使用或等待重置。",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := codexdata.AppSnapshot{
				RefreshedAt: refreshedAt,
				RateLimit: codexdata.RateLimitSummary{
					State: test.state,
					Secondary: &codexdata.RateLimitWindow{
						RemainingPercent: test.remaining,
						WindowMinutes:    10080,
						ResetAt:          resetAt,
					},
				},
				Statistics: codexdata.StatisticsSnapshot{
					DailyTokens: map[string]int64{},
					RefreshedAt: refreshedAt,
				},
			}
			presentation := presentSnapshot(snapshot)
			if presentation.Tone != test.wantTone {
				t.Fatalf("tone = %q, want %q", presentation.Tone, test.wantTone)
			}
			if got := presentation.Progress["quota.progress"]; got != test.wantProgress {
				t.Fatalf("quota progress = %d, want %d", got, test.wantProgress)
			}
			assertPresentationText(t, presentation, "quota.remaining", test.wantRemaining)
			assertPresentationText(t, presentation, "quota.reset", test.wantReset)
			assertPresentationText(t, presentation, "toast.title", test.wantTitle)
			assertPresentationText(t, presentation, "toast.message", test.wantMessage)
		})
	}
}

func TestPresentSnapshotBuildsCalendarAlignedMonthCells(t *testing.T) {
	refreshedAt := time.Date(2026, time.August, 9, 12, 0, 0, 0, time.UTC)
	snapshot := codexdata.AppSnapshot{
		Statistics: codexdata.StatisticsSnapshot{
			DailyTokens: map[string]int64{
				"2026-08-01": 10,
				"2026-08-02": 20,
				"2026-08-03": 30,
				"2026-08-04": 40,
			},
			RefreshedAt: refreshedAt,
		},
	}
	presentation := presentSnapshot(snapshot)
	levels := presentation.Cells["statistics.monthCells"]
	if len(levels) != 42 {
		t.Fatalf("month cell count = %d, want 42", len(levels))
	}
	for index, want := range map[int]int{5: 1, 6: 2, 7: 3, 8: 4} {
		if levels[index] != want {
			t.Fatalf("cell %d level = %d, want %d", index, levels[index], want)
		}
	}
	for index, level := range levels {
		if level < hiddenMonthCellLevel || level > 4 {
			t.Fatalf("cell %d has invalid level %d", index, level)
		}
	}
	for _, index := range []int{0, 1, 2, 3, 4, 36, 37, 38, 39, 40, 41} {
		if levels[index] != hiddenMonthCellLevel {
			t.Fatalf("cell %d level = %d, want hidden", index, levels[index])
		}
	}
	assertPresentationText(t, presentation, "statistics.month", "2026 年 8 月")
}

func TestMonthCellLevelsFollowActualMonthLength(t *testing.T) {
	tests := []struct {
		name  string
		month time.Time
		days  int
	}{
		{name: "july", month: time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC), days: 31},
		{name: "leap-february", month: time.Date(2024, time.February, 1, 0, 0, 0, 0, time.UTC), days: 29},
		{name: "common-february", month: time.Date(2025, time.February, 1, 0, 0, 0, 0, time.UTC), days: 28},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			levels := monthCellLevelsAt(codexdata.AppSnapshot{}, test.month)
			visible := 0
			for _, level := range levels {
				if level != hiddenMonthCellLevel {
					visible++
				}
			}
			if visible != test.days {
				t.Fatalf("visible cell count = %d, want %d", visible, test.days)
			}
		})
	}
}

func TestComposeRenderedSurfacePreservesBaseAndDrawsProgressAndCells(t *testing.T) {
	baseColor := color.NRGBA{R: 7, G: 11, B: 13, A: 255}
	base := image.NewNRGBA(image.Rect(0, 0, 42, 3))
	draw.Draw(base, base.Bounds(), image.NewUniform(baseColor), image.Point{}, draw.Src)
	cellRects := make([]slotRect, 0, 42)
	for index := range 42 {
		cellRects = append(cellRects, slotRect{
			X:      float64(index),
			Y:      1,
			Width:  1,
			Height: 1,
		})
	}
	surface := &renderedSurface{
		Surface: bundleSurface{
			ID:            "dynamic-test",
			LogicalWidth:  42,
			LogicalHeight: 3,
			Dynamic: dynamicSlots{
				Progress: []progressSlot{
					{
						Bind:       "quota.progress",
						Rect:       slotRect{X: 0, Y: 0, Width: 10, Height: 1},
						Color:      "#0000ff",
						ToneColors: toneColors{Warn: "#ff0000"},
					},
				},
				Cells: []cellSlot{
					{
						Bind:            "statistics.monthCells",
						Rects:           cellRects,
						Colors:          []string{"#000000", "#111111", "#222222", "#333333", "#444444"},
						BackgroundColor: "#abcdef",
					},
				},
			},
		},
		Variant: bundleVariant{Scale: 1, Width: 42, Height: 3},
		Image:   base,
	}
	refreshedAt := time.Date(2026, time.August, 9, 12, 0, 0, 0, time.UTC)
	snapshot := codexdata.AppSnapshot{
		RefreshedAt: refreshedAt,
		RateLimit: codexdata.RateLimitSummary{
			State: codexdata.SourceAvailable,
			Secondary: &codexdata.RateLimitWindow{
				RemainingPercent: 50,
				WindowMinutes:    10080,
			},
		},
		Statistics: codexdata.StatisticsSnapshot{
			DailyTokens: map[string]int64{
				"2026-08-01": 10,
				"2026-08-02": 20,
				"2026-08-03": 30,
				"2026-08-04": 40,
			},
			RefreshedAt: refreshedAt,
		},
	}

	composed, err := composeRenderedSurface(surface, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if composed == surface {
		t.Fatal("dynamic compositor returned the input surface")
	}
	if got := base.NRGBAAt(0, 0); got != baseColor {
		t.Fatalf("base image was mutated: got %+v, want %+v", got, baseColor)
	}
	imageResult, ok := composed.Image.(*image.NRGBA)
	if !ok {
		t.Fatalf("composed image type = %T, want *image.NRGBA", composed.Image)
	}
	assertPixel(t, imageResult, 0, 0, color.NRGBA{R: 255, A: 255})
	assertPixel(t, imageResult, 4, 0, color.NRGBA{R: 255, A: 255})
	assertPixel(t, imageResult, 5, 0, baseColor)
	assertPixel(t, imageResult, 0, 1, color.NRGBA{R: 171, G: 205, B: 239, A: 255})
	assertPixel(t, imageResult, 5, 1, color.NRGBA{R: 17, G: 17, B: 17, A: 255})
	assertPixel(t, imageResult, 6, 1, color.NRGBA{R: 34, G: 34, B: 34, A: 255})
	assertPixel(t, imageResult, 7, 1, color.NRGBA{R: 51, G: 51, B: 51, A: 255})
	assertPixel(t, imageResult, 8, 1, color.NRGBA{R: 68, G: 68, B: 68, A: 255})
	assertPixel(t, imageResult, 0, 2, baseColor)
}

func TestComposeRenderedSurfaceWithoutDynamicSlotsReturnsBase(t *testing.T) {
	surface := &renderedSurface{
		Surface: bundleSurface{ID: "static-test"},
		Image:   image.NewNRGBA(image.Rect(0, 0, 2, 2)),
	}
	composed, err := composeRenderedSurface(surface, codexdata.AppSnapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if composed != surface {
		t.Fatal("static compositor copied an unchanged surface")
	}
}

func TestDrawTextMaskRendersUnicode(t *testing.T) {
	mask, err := drawTextMask(textMaskRequest{
		Value:        "模型额度提醒",
		Width:        240,
		Height:       48,
		FontFamilies: []string{"Microsoft YaHei UI", "Microsoft YaHei", "Segoe UI"},
		FontPixels:   22,
		FontWeight:   600,
		Align:        "left",
	})
	if err != nil {
		t.Fatal(err)
	}
	if mask.Bounds() != image.Rect(0, 0, 240, 48) {
		t.Fatalf("mask bounds = %v", mask.Bounds())
	}
	for _, coverage := range mask.Pix {
		if coverage != 0 {
			return
		}
	}
	t.Fatal("Unicode text mask is empty")
}

func TestDirectWriteTextMaskP0(t *testing.T) {
	for range 20 {
		mask, err := drawDirectWriteTextMask(textMaskRequest{
			Value:        "GPT-5.6 模型额度 72%",
			Width:        280,
			Height:       52,
			FontFamilies: []string{"Microsoft YaHei UI", "Microsoft YaHei", "Segoe UI"},
			FontPixels:   22,
			FontWeight:   600,
			Align:        "center",
		})
		if err != nil {
			t.Fatal(err)
		}
		var nonzero int
		var partial int
		for _, coverage := range mask.Pix {
			if coverage != 0 {
				nonzero++
			}
			if coverage > 0 && coverage < 255 {
				partial++
			}
		}
		if nonzero == 0 {
			t.Fatal("DirectWrite text mask is empty")
		}
		if partial == 0 {
			t.Fatal("DirectWrite text mask has no grayscale antialiasing")
		}
	}
}

func TestDirectWriteFontResolverSkipsUnavailableCandidate(t *testing.T) {
	factory, err := getSharedDWriteFactory()
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := resolveDirectWriteFontFamily(
		factory,
		[]string{"Codex Missing Font 019febd1", "Segoe UI"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != "Segoe UI" {
		t.Fatalf("resolved font = %q, want Segoe UI", resolved)
	}
}

func TestExpandedTextSlotRectAddsAlignmentAwareAllowance(t *testing.T) {
	bounds := image.Rect(0, 0, 100, 40)
	logical := slotRect{X: 20, Y: 5, Width: 24, Height: 17}
	tests := []struct {
		align string
		want  image.Rectangle
	}{
		{align: "left", want: image.Rect(20, 5, 47, 22)},
		{align: "center", want: image.Rect(19, 5, 46, 22)},
		{align: "right", want: image.Rect(17, 5, 44, 22)},
	}
	for _, test := range tests {
		t.Run(test.align, func(t *testing.T) {
			got := expandedTextSlotRect(logical, test.align, 1, bounds)
			if got != test.want {
				t.Fatalf("expanded text rect = %v, want %v", got, test.want)
			}
		})
	}
}

func assertPresentationText(
	t *testing.T,
	presentation uiPresentation,
	binding string,
	want string,
) {
	t.Helper()
	if got := presentation.Text[binding]; got != want {
		t.Fatalf("%s = %q, want %q", binding, got, want)
	}
}

func assertPixel(
	t *testing.T,
	imageValue *image.NRGBA,
	x int,
	y int,
	want color.NRGBA,
) {
	t.Helper()
	if got := imageValue.NRGBAAt(x, y); got != want {
		t.Fatalf("pixel (%d,%d) = %+v, want %+v", x, y, got, want)
	}
}
