//go:build windows

package main

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/raywangsmia-bit/CodexFloatBar/internal/codexdata"
)

func presentSnapshot(snapshot codexdata.AppSnapshot) uiPresentation {
	return presentSnapshotWithStatistics(snapshot, statisticsSelection{})
}

func presentSnapshotWithStatistics(
	snapshot codexdata.AppSnapshot,
	selection statisticsSelection,
) uiPresentation {
	selection = normalizeStatisticsSelection(snapshot, selection)
	detail := statisticsDetail(snapshot, selection)
	presentation := uiPresentation{
		Text: map[string]string{
			"runtime.model":             displayOr(snapshot.Runtime.Model, "未配置模型"),
			"runtime.effort":            formatEffort(snapshot.Runtime.ReasoningEffort),
			"runtime.speed":             formatSpeed(snapshot.Runtime.SpeedTier),
			"quota.plan":                strings.TrimSpace(snapshot.RateLimit.PlanType),
			"statistics.total":          formatTokens(snapshot.Statistics.TotalTokens),
			"statistics.peak":           formatTokens(snapshot.Statistics.PeakSessionTokens),
			"statistics.duration":       formatDuration(snapshot.Statistics.LongestActiveSeconds),
			"statistics.currentStreak":  strconv.Itoa(snapshot.Statistics.CurrentStreakDays) + " 天",
			"statistics.longestStreak":  strconv.Itoa(snapshot.Statistics.LongestStreakDays) + " 天",
			"statistics.month":          statisticsPeriodText(snapshot, selection),
			"statistics.previousMonth":  statisticsPreviousMonthText(snapshot, selection),
			"statistics.nextMonth":      statisticsNextMonthText(snapshot, selection),
			"statistics.viewMonth":      "月热图",
			"statistics.viewWeek":       "每周",
			"statistics.viewCumulative": "累计",
			"statistics.viewDetail":     "详细",
			"statistics.detailInput":    formatDetailTokens(detail.InputTokens, detail.Complete),
			"statistics.detailOutput":   formatDetailTokens(detail.OutputTokens, detail.Complete),
			"statistics.detailTotal":    formatTokens(detail.TotalTokens),
			"statistics.detailCached":   formatDetailTokens(detail.CachedInputTokens, detail.Complete),
			"statistics.detailReasoning": formatDetailTokens(
				detail.ReasoningOutputTokens,
				detail.Complete,
			),
			"statistics.detailCacheHit": formatCacheHit(detail),
			"statistics.detailCost":     estimateEquivalentCost(snapshot.Runtime.Model, detail),
			"statistics.labelInput":     "输入 Token",
			"statistics.labelOutput":    "输出 Token",
			"statistics.labelTotal":     "总 Token",
			"statistics.labelCached":    "缓存 Token",
			"statistics.labelReasoning": "推理 Token",
			"statistics.labelCacheHit":  "Cache Hit",
			"statistics.labelCost":      "等价成本",
		},
		Progress: map[string]int{"quota.progress": 0},
		Cells: map[string][]int{
			"statistics.monthCells": monthCellLevelsAt(
				snapshot,
				selection.Month,
				selection.SelectedDay,
			),
		},
		Tone:           quotaToneOffline,
		StatisticsView: selection.View,
		ChartValues:    statisticsChartValues(snapshot, selection.View),
	}
	window := codexdata.SelectedWeeklyWindow(snapshot.RateLimit)
	if snapshot.RateLimit.State != codexdata.SourceAvailable || window == nil {
		presentation.Text["quota.remaining"] = "--"
		presentation.Text["quota.reset"] = "等待用量记录"
		presentation.Text["toast.title"] = "等待额度记录"
		presentation.Text["toast.message"] = "Codex 产生用量记录后将在这里显示。"
		return presentation
	}

	remaining := max(0, min(100, window.RemainingPercent))
	presentation.Progress["quota.progress"] = remaining
	presentation.Text["quota.remaining"] = strconv.Itoa(remaining) + "%"
	location := snapshot.RefreshedAt.Location()
	presentation.Text["quota.reset"] = time.Unix(window.ResetAt, 0).
		In(location).
		Format("1/2 15:04") + " 重置"
	presentation.Tone = toneForRemaining(remaining)
	switch presentation.Tone {
	case quotaToneDanger:
		presentation.Text["toast.title"] = "一周额度快用完了"
		presentation.Text["toast.message"] = fmt.Sprintf(
			"当前剩余 %d%%，建议放慢使用或等待重置。",
			remaining,
		)
	case quotaToneWarn:
		presentation.Text["toast.title"] = "一周额度不高于 60%"
		presentation.Text["toast.message"] = fmt.Sprintf(
			"当前剩余 %d%%，用量进入提醒区间。",
			remaining,
		)
	default:
		presentation.Text["toast.title"] = "一周额度状态良好"
		presentation.Text["toast.message"] = fmt.Sprintf(
			"当前剩余 %d%%，暂无额度压力。",
			remaining,
		)
	}
	return presentation
}

func normalizeStatisticsSelection(
	snapshot codexdata.AppSnapshot,
	selection statisticsSelection,
) statisticsSelection {
	if !validStatisticsView(selection.View) {
		selection.View = statisticsViewMonth
	}
	current := currentStatisticsMonth(snapshot)
	if current.IsZero() {
		selection.Month = time.Time{}
		return selection
	}
	month := selection.Month
	if month.IsZero() {
		month = current
	} else {
		month = time.Date(month.Year(), month.Month(), 1, 0, 0, 0, 0, current.Location())
	}
	earliest := earliestStatisticsMonth(snapshot, current)
	if month.Before(earliest) {
		month = earliest
	}
	if month.After(current) {
		month = current
	}
	selection.Month = month
	lastDay := month.AddDate(0, 1, -1).Day()
	if selection.SelectedDay < 1 || selection.SelectedDay > lastDay {
		selection.SelectedDay = 0
	}
	refreshed := snapshot.Statistics.RefreshedAt.In(current.Location())
	if month.Equal(current) && selection.SelectedDay > refreshed.Day() {
		selection.SelectedDay = 0
	}
	return selection
}

func currentStatisticsMonth(snapshot codexdata.AppSnapshot) time.Time {
	reference := snapshot.Statistics.RefreshedAt
	if reference.IsZero() {
		reference = snapshot.RefreshedAt
	}
	if reference.IsZero() {
		return time.Time{}
	}
	return time.Date(
		reference.Year(),
		reference.Month(),
		1,
		0,
		0,
		0,
		0,
		reference.Location(),
	)
}

func earliestStatisticsMonth(
	snapshot codexdata.AppSnapshot,
	fallback time.Time,
) time.Time {
	if fallback.IsZero() {
		return time.Time{}
	}
	earliest, err := time.ParseInLocation(
		"2006-01-02",
		snapshot.Statistics.EarliestActiveDate,
		fallback.Location(),
	)
	if err != nil {
		return fallback
	}
	earliest = time.Date(
		earliest.Year(),
		earliest.Month(),
		1,
		0,
		0,
		0,
		0,
		fallback.Location(),
	)
	if earliest.After(fallback) {
		return fallback
	}
	return earliest
}

func statisticsPeriodText(
	snapshot codexdata.AppSnapshot,
	selection statisticsSelection,
) string {
	switch selection.View {
	case statisticsViewWeek:
		return fmt.Sprintf("最近 %d 周", len(snapshot.Statistics.Weekly))
	case statisticsViewCumulative:
		return fmt.Sprintf("近 %d 月累计", len(snapshot.Statistics.Monthly))
	case statisticsViewDetail:
		if selection.SelectedDay > 0 {
			return fmt.Sprintf(
				"%d-%02d-%02d",
				selection.Month.Year(),
				selection.Month.Month(),
				selection.SelectedDay,
			)
		}
		return formatStatisticsMonth(selection.Month)
	default:
		return formatStatisticsMonth(selection.Month)
	}
}

func statisticsPreviousMonthText(
	snapshot codexdata.AppSnapshot,
	selection statisticsSelection,
) string {
	if !statisticsMonthNavigationVisible(selection) || selection.Month.IsZero() {
		return ""
	}
	current := currentStatisticsMonth(snapshot)
	if !selection.Month.After(earliestStatisticsMonth(snapshot, current)) {
		return ""
	}
	return "‹"
}

func statisticsNextMonthText(
	snapshot codexdata.AppSnapshot,
	selection statisticsSelection,
) string {
	if !statisticsMonthNavigationVisible(selection) || selection.Month.IsZero() {
		return ""
	}
	if !selection.Month.Before(currentStatisticsMonth(snapshot)) {
		return ""
	}
	return "›"
}

func statisticsMonthNavigationVisible(selection statisticsSelection) bool {
	return selection.View == statisticsViewMonth || selection.View == statisticsViewDetail
}

func statisticsChartValues(
	snapshot codexdata.AppSnapshot,
	view statisticsView,
) []int64 {
	values := []int64{}
	switch view {
	case statisticsViewWeek:
		for _, point := range snapshot.Statistics.Weekly {
			values = append(values, max(int64(0), point.Tokens))
		}
	case statisticsViewCumulative:
		for _, point := range snapshot.Statistics.Monthly {
			values = append(values, max(int64(0), point.CumulativeTokens))
		}
	}
	return values
}

func statisticsDetail(
	snapshot codexdata.AppSnapshot,
	selection statisticsSelection,
) codexdata.TokenBreakdown {
	if selection.Month.IsZero() {
		return codexdata.TokenBreakdown{Complete: true}
	}
	if selection.SelectedDay > 0 {
		key := time.Date(
			selection.Month.Year(),
			selection.Month.Month(),
			selection.SelectedDay,
			0,
			0,
			0,
			0,
			time.UTC,
		).Format("2006-01-02")
		if detail, ok := snapshot.Statistics.DailyTokenBreakdowns[key]; ok {
			return detail
		}
		return codexdata.TokenBreakdown{
			TotalTokens: snapshot.Statistics.DailyTokens[key],
			Complete:    snapshot.Statistics.DailyTokens[key] == 0,
		}
	}
	result := codexdata.TokenBreakdown{Complete: true}
	monthStart := time.Date(
		selection.Month.Year(),
		selection.Month.Month(),
		1,
		0,
		0,
		0,
		0,
		time.UTC,
	)
	monthEnd := monthStart.AddDate(0, 1, 0)
	for date, tokens := range snapshot.Statistics.DailyTokens {
		parsed, err := time.Parse("2006-01-02", date)
		if err != nil || parsed.Before(monthStart) || !parsed.Before(monthEnd) {
			continue
		}
		detail, ok := snapshot.Statistics.DailyTokenBreakdowns[date]
		if !ok {
			detail = codexdata.TokenBreakdown{TotalTokens: tokens}
		}
		result = addPresentationBreakdown(result, detail)
	}
	return result
}

func addPresentationBreakdown(
	left codexdata.TokenBreakdown,
	right codexdata.TokenBreakdown,
) codexdata.TokenBreakdown {
	return codexdata.TokenBreakdown{
		InputTokens:           left.InputTokens + right.InputTokens,
		CachedInputTokens:     left.CachedInputTokens + right.CachedInputTokens,
		CacheWriteInputTokens: left.CacheWriteInputTokens + right.CacheWriteInputTokens,
		OutputTokens:          left.OutputTokens + right.OutputTokens,
		ReasoningOutputTokens: left.ReasoningOutputTokens + right.ReasoningOutputTokens,
		TotalTokens:           left.TotalTokens + right.TotalTokens,
		Complete:              left.Complete && right.Complete,
	}
}

func formatDetailTokens(value int64, complete bool) string {
	if !complete {
		return "--"
	}
	return formatTokens(value)
}

func formatCacheHit(detail codexdata.TokenBreakdown) string {
	if !detail.Complete || detail.InputTokens <= 0 {
		return "--"
	}
	percent := 100 * float64(detail.CachedInputTokens) / float64(detail.InputTokens)
	return strconv.Itoa(int(math.Round(percent))) + "%"
}

type apiTokenPrice struct {
	Input       float64
	CachedInput float64
	CacheWrite  float64
	Output      float64
}

func estimateEquivalentCost(model string, detail codexdata.TokenBreakdown) string {
	if !detail.Complete {
		return "--"
	}
	prices := map[string]apiTokenPrice{
		"gpt-5.6":       {Input: 5, CachedInput: 0.5, CacheWrite: 6.25, Output: 30},
		"gpt-5.6-sol":   {Input: 5, CachedInput: 0.5, CacheWrite: 6.25, Output: 30},
		"gpt-5.6-terra": {Input: 2, CachedInput: 0.2, CacheWrite: 2.5, Output: 12},
		"gpt-5.6-luna":  {Input: 0.2, CachedInput: 0.02, CacheWrite: 0.25, Output: 1.2},
	}
	price, ok := prices[strings.ToLower(strings.TrimSpace(model))]
	if !ok {
		return "--"
	}
	uncached := max(
		int64(0),
		detail.InputTokens-detail.CachedInputTokens-detail.CacheWriteInputTokens,
	)
	cost := float64(uncached)*price.Input/1_000_000 +
		float64(detail.CachedInputTokens)*price.CachedInput/1_000_000 +
		float64(detail.CacheWriteInputTokens)*price.CacheWrite/1_000_000 +
		float64(detail.OutputTokens)*price.Output/1_000_000
	return "$" + strconv.FormatFloat(cost, 'f', 2, 64)
}

func toneForRemaining(remaining int) quotaTone {
	switch {
	case remaining > 60:
		return quotaToneGood
	case remaining >= 11:
		return quotaToneWarn
	default:
		return quotaToneDanger
	}
}

func formatEffort(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "none":
		return "无"
	case "minimal":
		return "极低"
	case "low":
		return "低"
	case "medium":
		return "中"
	case "high":
		return "高"
	case "xhigh":
		return "超高"
	case "":
		return "读取中"
	default:
		return value
	}
}

func formatSpeed(value string) string {
	if strings.TrimSpace(value) == "" {
		return "未读取到"
	}
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "fast", "priority":
		return "快速"
	default:
		return "标准"
	}
}

func formatTokens(value int64) string {
	switch {
	case value >= 100_000_000:
		return compactDecimal(float64(value)/100_000_000) + "亿"
	case value >= 10_000:
		return compactDecimal(float64(value)/10_000) + "万"
	default:
		return groupedInteger(value)
	}
}

func compactDecimal(value float64) string {
	return strings.TrimSuffix(strconv.FormatFloat(value, 'f', 1, 64), ".0")
}

func groupedInteger(value int64) string {
	digits := strconv.FormatInt(value, 10)
	start := 0
	if strings.HasPrefix(digits, "-") {
		start = 1
	}
	for index := len(digits) - 3; index > start; index -= 3 {
		digits = digits[:index] + "," + digits[index:]
	}
	return digits
}

func formatDuration(seconds int64) string {
	duration := time.Duration(max(int64(0), seconds)) * time.Second
	switch {
	case duration >= 24*time.Hour:
		return fmt.Sprintf("%d天 %d小时", int(duration/(24*time.Hour)), int(duration/time.Hour)%24)
	case duration >= time.Hour:
		return fmt.Sprintf("%d小时 %d分", int(duration/time.Hour), int(duration/time.Minute)%60)
	case duration >= time.Minute:
		return fmt.Sprintf("%d分", int(duration/time.Minute))
	default:
		return "<1分"
	}
}

func formatStatisticsMonth(refreshedAt time.Time) string {
	if refreshedAt.IsZero() {
		return "暂无统计"
	}
	return fmt.Sprintf("%d 年 %d 月", refreshedAt.Year(), refreshedAt.Month())
}

func monthCellLevels(snapshot codexdata.AppSnapshot) []int {
	selection := normalizeStatisticsSelection(snapshot, statisticsSelection{})
	return monthCellLevelsAt(snapshot, selection.Month, selection.SelectedDay)
}

func monthCellLevelsAt(
	snapshot codexdata.AppSnapshot,
	selectedMonth time.Time,
	selectedDays ...int,
) []int {
	levels := make([]int, 42)
	for index := range levels {
		levels[index] = hiddenMonthCellLevel
	}
	if selectedMonth.IsZero() {
		return levels
	}
	year := selectedMonth.Year()
	month := selectedMonth.Month()
	monthStart := time.Date(year, month, 1, 0, 0, 0, 0, time.UTC)
	monthEnd := monthStart.AddDate(0, 1, 0)
	values := []int64{}
	for date, tokens := range snapshot.Statistics.DailyTokens {
		parsed, err := time.Parse("2006-01-02", date)
		if err == nil && !parsed.Before(monthStart) && parsed.Before(monthEnd) && tokens > 0 {
			values = append(values, tokens)
		}
	}
	sort.Slice(values, func(left int, right int) bool { return values[left] < values[right] })
	firstOffset := (int(monthStart.Weekday()) + 6) % 7
	for day := 1; day <= monthEnd.AddDate(0, 0, -1).Day(); day++ {
		key := time.Date(year, month, day, 0, 0, 0, 0, time.UTC).Format("2006-01-02")
		levels[firstOffset+day-1] = heatLevel(snapshot.Statistics.DailyTokens[key], values)
	}
	if len(selectedDays) > 0 && selectedDays[0] > 0 {
		index := firstOffset + selectedDays[0] - 1
		if index >= 0 && index < len(levels) && levels[index] >= 0 {
			levels[index] = selectedMonthCellLevel
		}
	}
	return levels
}

func heatLevel(value int64, sortedNonZero []int64) int {
	if value <= 0 || len(sortedNonZero) == 0 {
		return 0
	}
	below := sort.Search(len(sortedNonZero), func(index int) bool {
		return sortedNonZero[index] >= value
	})
	return min(4, below*4/len(sortedNonZero)+1)
}
