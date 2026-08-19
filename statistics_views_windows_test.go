//go:build windows

package main

import (
	"image"
	"image/color"
	"testing"
	"time"

	"github.com/raywangsmia-bit/CodexFloatBar/internal/codexdata"
)

func TestPresentSnapshotSupportsStatisticsMonthWeekAndCumulativeViews(t *testing.T) {
	snapshot := statisticsViewTestSnapshot()
	july := time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC)
	month := presentSnapshotWithStatistics(snapshot, statisticsSelection{
		View:  statisticsViewMonth,
		Month: july,
	})
	if month.StatisticsView != statisticsViewMonth {
		t.Fatalf("month view = %q", month.StatisticsView)
	}
	assertPresentationText(t, month, "statistics.month", "2026 年 7 月")
	if month.Cells["statistics.monthCells"][2] == 0 {
		t.Fatal("selected July data was not rendered in the month grid")
	}
	for _, index := range []int{0, 1, 33, 34, 35, 36, 37, 38, 39, 40, 41} {
		if month.Cells["statistics.monthCells"][index] != hiddenMonthCellLevel {
			t.Fatalf("July cell %d should be hidden", index)
		}
	}

	week := presentSnapshotWithStatistics(snapshot, statisticsSelection{View: statisticsViewWeek})
	if week.StatisticsView != statisticsViewWeek || len(week.ChartValues) != 3 {
		t.Fatalf("weekly presentation = %+v", week)
	}
	assertPresentationText(t, week, "statistics.month", "最近 3 周")
	assertPresentationText(t, week, "statistics.previousMonth", "")
	assertPresentationText(t, week, "statistics.nextMonth", "")

	cumulative := presentSnapshotWithStatistics(
		snapshot,
		statisticsSelection{View: statisticsViewCumulative},
	)
	if cumulative.StatisticsView != statisticsViewCumulative ||
		len(cumulative.ChartValues) != 3 || cumulative.ChartValues[2] != 900 {
		t.Fatalf("cumulative presentation = %+v", cumulative)
	}
	assertPresentationText(t, cumulative, "statistics.month", "近 3 月累计")
}

func TestStatusRuntimeStatisticsActionsClampMonthAndIgnoreHiddenNavigation(t *testing.T) {
	runtime := &statusRuntime{current: statisticsViewTestSnapshot()}
	if !runtime.applyStatisticsAction("statistics-previous-month") {
		t.Fatal("first previous-month action did not move to July")
	}
	if runtime.statistics.Month.Month() != time.July {
		t.Fatalf("selected month = %s, want July", runtime.statistics.Month)
	}
	if !runtime.applyStatisticsAction("statistics-previous-month") {
		t.Fatal("second previous-month action did not move to June")
	}
	if runtime.applyStatisticsAction("statistics-previous-month") {
		t.Fatal("previous-month action moved before the earliest active month")
	}
	if !runtime.applyStatisticsAction("statistics-view-week") {
		t.Fatal("weekly view action was ignored")
	}
	if runtime.applyStatisticsAction("statistics-next-month") {
		t.Fatal("month navigation was accepted outside the month view")
	}
	if !runtime.applyStatisticsAction("statistics-view-month") ||
		!runtime.applyStatisticsAction("statistics-next-month") {
		t.Fatal("returning to the month view did not restore navigation")
	}
	if !runtime.applyStatisticsAction("statistics-select-day-05") ||
		runtime.statistics.View != statisticsViewDetail || runtime.statistics.SelectedDay != 4 {
		t.Fatalf("date selection did not open detail: %+v", runtime.statistics)
	}
	if runtime.applyStatisticsAction("statistics-select-day-05") {
		t.Fatal("date selection was accepted outside the month view")
	}
	if runtime.applyStatisticsAction("statistics-unknown") {
		t.Fatal("unknown statistics action was accepted")
	}
}

func TestStatusCacheCheckpointIntervalAllowsExitFlushToOwnDurability(t *testing.T) {
	if statusCacheSaveInterval != time.Minute {
		t.Fatalf("cache save interval = %s, want 1m", statusCacheSaveInterval)
	}
}

func TestStatisticsDetailShowsSelectedDayAndMonthlyFallback(t *testing.T) {
	snapshot := statisticsViewTestSnapshot()
	snapshot.Runtime.Model = "gpt-5.6"
	snapshot.Statistics.DailyTokenBreakdowns = map[string]codexdata.TokenBreakdown{
		"2026-07-01": {
			InputTokens: 1000000, CachedInputTokens: 500000,
			OutputTokens: 100000, ReasoningOutputTokens: 25000,
			TotalTokens: 1100000, Complete: true,
		},
	}
	july := time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC)
	selected := presentSnapshotWithStatistics(snapshot, statisticsSelection{
		View: statisticsViewDetail, Month: july, SelectedDay: 1,
	})
	if selected.StatisticsView != statisticsViewDetail ||
		selected.Cells["statistics.monthCells"][2] != selectedMonthCellLevel {
		t.Fatalf("selected detail presentation = %+v", selected)
	}
	assertPresentationText(t, selected, "statistics.month", "2026-07-01")
	assertPresentationText(t, selected, "statistics.detailInput", "100万")
	assertPresentationText(t, selected, "statistics.detailCacheHit", "50%")
	assertPresentationText(t, selected, "statistics.detailCost", "$5.75")

	monthly := presentSnapshotWithStatistics(snapshot, statisticsSelection{
		View: statisticsViewDetail, Month: july,
	})
	assertPresentationText(t, monthly, "statistics.month", "2026 年 7 月")
	assertPresentationText(t, monthly, "statistics.detailTotal", "110万")
}

func TestDrawStatisticsChartRendersWeeklyBarsAndCumulativeLine(t *testing.T) {
	slot := statisticsChartTestSlot()
	accent := color.NRGBA{R: 68, G: 68, B: 68, A: 255}
	for _, test := range []struct {
		name string
		view statisticsView
	}{
		{name: "weekly bars", view: statisticsViewWeek},
		{name: "cumulative line", view: statisticsViewCumulative},
	} {
		t.Run(test.name, func(t *testing.T) {
			destination := image.NewNRGBA(image.Rect(0, 0, 80, 50))
			presentation := uiPresentation{
				StatisticsView: test.view,
				ChartValues:    []int64{10, 35, 20, 60, 45},
			}
			if err := drawStatisticsChart(destination, slot, 1, presentation); err != nil {
				t.Fatal(err)
			}
			if countExactColor(destination, accent) == 0 {
				t.Fatalf("%s did not render the accent series", test.name)
			}
		})
	}
}

func TestDrawStatisticsDetailCardsMatchesSurfaceTheme(t *testing.T) {
	text := []textSlot{
		{Bind: "statistics.detailInput", Rect: slotRect{X: 10, Y: 20, Width: 30, Height: 10}},
		{Bind: "statistics.labelInput", Rect: slotRect{X: 10, Y: 32, Width: 30, Height: 8}},
	}
	for _, test := range []struct {
		name string
		id   string
		want color.NRGBA
	}{
		{name: "dark", id: "statistics", want: color.NRGBA{R: 0x20, G: 0x24, B: 0x28, A: 0xff}},
		{name: "light", id: "statistics-light", want: color.NRGBA{R: 0xf1, G: 0xf4, B: 0xf6, A: 0xff}},
	} {
		t.Run(test.name, func(t *testing.T) {
			destination := image.NewNRGBA(image.Rect(0, 0, 60, 60))
			surface := bundleSurface{ID: test.id, Dynamic: dynamicSlots{Text: text}}
			if err := drawStatisticsDetailCards(destination, surface, 1); err != nil {
				t.Fatal(err)
			}
			if got := destination.NRGBAAt(25, 15); got != test.want {
				t.Fatalf("card color = %+v, want %+v", got, test.want)
			}
			if got := destination.NRGBAAt(5, 15); got.A != 0 {
				t.Fatalf("outside card was painted: %+v", got)
			}
		})
	}
}

func statisticsViewTestSnapshot() codexdata.AppSnapshot {
	refreshedAt := time.Date(2026, time.August, 9, 12, 0, 0, 0, time.UTC)
	return codexdata.AppSnapshot{
		RefreshedAt: refreshedAt,
		Statistics: codexdata.StatisticsSnapshot{
			DailyTokens: map[string]int64{
				"2026-06-02": 50,
				"2026-07-01": 100,
				"2026-08-03": 200,
			},
			Weekly: []codexdata.WeeklyTokenUsage{
				{StartDate: "2026-07-20", Tokens: 100},
				{StartDate: "2026-07-27", Tokens: 250},
				{StartDate: "2026-08-03", Tokens: 400},
			},
			Monthly: []codexdata.MonthlyCumulativeUsage{
				{Month: "2026-06", CumulativeTokens: 100},
				{Month: "2026-07", CumulativeTokens: 450},
				{Month: "2026-08", CumulativeTokens: 900},
			},
			EarliestActiveDate: "2026-06-02",
			RefreshedAt:        refreshedAt,
		},
	}
}

func statisticsChartTestSlot() cellSlot {
	rects := make([]slotRect, 0, 42)
	for row := range 6 {
		for column := range 7 {
			rects = append(rects, slotRect{
				X:      float64(4 + column*10),
				Y:      float64(4 + row*7),
				Width:  8,
				Height: 5,
			})
		}
	}
	return cellSlot{
		Bind:   "statistics.monthCells",
		Rects:  rects,
		Colors: []string{"#000000", "#111111", "#222222", "#333333", "#444444"},
	}
}

func countExactColor(value *image.NRGBA, want color.NRGBA) int {
	count := 0
	for y := value.Bounds().Min.Y; y < value.Bounds().Max.Y; y++ {
		for x := value.Bounds().Min.X; x < value.Bounds().Max.X; x++ {
			if value.NRGBAAt(x, y) == want {
				count++
			}
		}
	}
	return count
}
