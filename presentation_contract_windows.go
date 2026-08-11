//go:build windows

package main

import (
	"maps"
	"slices"
	"strings"
	"time"
)

type quotaTone string

const (
	quotaToneGood    quotaTone = "good"
	quotaToneWarn    quotaTone = "warn"
	quotaToneDanger  quotaTone = "danger"
	quotaToneOffline quotaTone = "offline"
)

const (
	hiddenMonthCellLevel   = -1
	selectedMonthCellLevel = 5
)

type uiPresentation struct {
	Text           map[string]string
	Progress       map[string]int
	Cells          map[string][]int
	Tone           quotaTone
	StatisticsView statisticsView
	ChartValues    []int64
}

type statisticsView string

const (
	statisticsViewMonth      statisticsView = "month"
	statisticsViewWeek       statisticsView = "week"
	statisticsViewCumulative statisticsView = "cumulative"
	statisticsViewDetail     statisticsView = "detail"
)

type statisticsSelection struct {
	View        statisticsView
	Month       time.Time
	SelectedDay int
}

func sameUIPresentation(left uiPresentation, right uiPresentation) bool {
	sameCells := maps.EqualFunc(
		left.Cells,
		right.Cells,
		func(leftLevels []int, rightLevels []int) bool {
			return slices.Equal(leftLevels, rightLevels)
		},
	)
	return left.Tone == right.Tone &&
		left.StatisticsView == right.StatisticsView &&
		maps.Equal(left.Text, right.Text) &&
		maps.Equal(left.Progress, right.Progress) &&
		sameCells && slices.Equal(left.ChartValues, right.ChartValues)
}

func validStatisticsView(view statisticsView) bool {
	switch view {
	case statisticsViewMonth, statisticsViewWeek, statisticsViewCumulative,
		statisticsViewDetail:
		return true
	default:
		return false
	}
}

func displayOr(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
