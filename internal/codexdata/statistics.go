package codexdata

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	activeGap                     = 30 * time.Minute
	maxStatisticsLineBytes        = 4 * 1024 * 1024
	maxStatisticsCacheBytes int64 = 16 * 1024 * 1024
)

type statisticsCache struct {
	Version  int          `json:"Version"`
	TimeZone string       `json:"TimeZone"`
	Files    []cachedFile `json:"Files"`
}

type cachedFile struct {
	Path              string         `json:"Path"`
	Length            int64          `json:"Length"`
	LastWriteUTCTicks int64          `json:"LastWriteUtcTicks"`
	ParsedLength      int64          `json:"ParsedLength"`
	LastUsage         TokenBreakdown `json:"LastUsage"`
	HasLastUsage      bool           `json:"HasLastUsage"`
	SegmentStart      time.Time      `json:"SegmentStart"`
	LastActivity      time.Time      `json:"LastActivity"`
	Summary           sessionSummary `json:"Summary"`
}

type sessionSummary struct {
	TotalTokens        int64                     `json:"TotalTokens"`
	LongestActiveTicks int64                     `json:"LongestActiveTicks"`
	DailyTokens        map[string]int64          `json:"DailyTokens"`
	TokenTotals        TokenBreakdown            `json:"TokenTotals"`
	DailyBreakdowns    map[string]TokenBreakdown `json:"DailyBreakdowns"`
}

type tokenEvent struct {
	Timestamp string `json:"timestamp"`
	Type      string `json:"type"`
	Payload   *struct {
		Type string `json:"type"`
		Info *struct {
			TotalTokenUsage *struct {
				InputTokens           *int64 `json:"input_tokens"`
				CachedInputTokens     *int64 `json:"cached_input_tokens"`
				CacheWriteInputTokens *int64 `json:"cache_write_input_tokens"`
				OutputTokens          *int64 `json:"output_tokens"`
				ReasoningOutputTokens *int64 `json:"reasoning_output_tokens"`
				TotalTokens           *int64 `json:"total_tokens"`
			} `json:"total_token_usage"`
		} `json:"info"`
	} `json:"payload"`
}

func readStatistics(
	ctx context.Context,
	sessionsPath string,
	cachePath string,
	now time.Time,
	location *time.Location,
) (StatisticsSnapshot, error) {
	timeZone := statisticsTimeZoneKey(location, now)
	oldCache := readStatisticsCache(cachePath, timeZone)
	oldFiles := make(map[string]cachedFile, len(oldCache.Files))
	for _, file := range oldCache.Files {
		if !validCachedFile(file) {
			continue
		}
		oldFiles[cachePathKey(file.Path)] = file
	}
	workingFiles := make(map[string]cachedFile, len(oldFiles))
	for key, file := range oldFiles {
		workingFiles[key] = file
	}

	paths, err := allSessionPaths(ctx, sessionsPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return StatisticsSnapshot{}, err
	}
	nextFiles := make([]cachedFile, 0, len(paths))
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return StatisticsSnapshot{}, err
		}
		fullPath, err := filepath.Abs(path)
		if err != nil {
			continue
		}
		previous, hadPrevious := oldFiles[cachePathKey(fullPath)]
		info, err := os.Stat(fullPath)
		if err != nil {
			if hadPrevious {
				nextFiles = append(nextFiles, previous)
			}
			continue
		}
		lastWriteTicks := dotNetTicks(info.ModTime().UTC())
		if hadPrevious && previous.Length == info.Size() &&
			previous.LastWriteUTCTicks == lastWriteTicks {
			nextFiles = append(nextFiles, previous)
			continue
		}
		start := cachedFile{Path: fullPath}
		canResume := hadPrevious && previous.ParsedLength >= 0 &&
			previous.ParsedLength <= previous.Length && previous.Length < info.Size()
		if canResume {
			start = previous
		}
		parsed, err := parseStatisticsFileIncremental(
			ctx,
			fullPath,
			location,
			start,
		)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return StatisticsSnapshot{}, err
			}
			if hadPrevious {
				nextFiles = append(nextFiles, previous)
			}
			continue
		}
		parsed.Path = fullPath
		parsed.Length = info.Size()
		parsed.LastWriteUTCTicks = lastWriteTicks
		nextFiles = append(nextFiles, parsed)
		workingFiles[cachePathKey(fullPath)] = parsed
		_ = saveStatisticsCache(cachePath, statisticsCache{
			Version:  cacheVersion,
			TimeZone: timeZone,
			Files:    sortedCachedFiles(workingFiles),
		})
	}
	slices.SortFunc(nextFiles, func(left cachedFile, right cachedFile) int {
		return strings.Compare(cachePathKey(left.Path), cachePathKey(right.Path))
	})
	if err := ctx.Err(); err != nil {
		return StatisticsSnapshot{}, err
	}
	nextCache := statisticsCache{
		Version:  cacheVersion,
		TimeZone: timeZone,
		Files:    nextFiles,
	}
	_ = saveStatisticsCache(cachePath, nextCache)
	summaries := make([]sessionSummary, 0, len(nextFiles))
	for _, file := range nextFiles {
		summaries = append(summaries, file.Summary)
	}
	return buildStatisticsSnapshot(summaries, now, location), nil
}

func sortedCachedFiles(files map[string]cachedFile) []cachedFile {
	result := make([]cachedFile, 0, len(files))
	for _, file := range files {
		result = append(result, file)
	}
	slices.SortFunc(result, func(left cachedFile, right cachedFile) int {
		return strings.Compare(cachePathKey(left.Path), cachePathKey(right.Path))
	})
	return result
}

func allSessionPaths(ctx context.Context, root string) ([]string, error) {
	paths := []string{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if !entry.IsDir() && strings.EqualFold(filepath.Ext(path), ".jsonl") {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return []string{}, err
	}
	slices.SortFunc(paths, func(left string, right string) int {
		return strings.Compare(cachePathKey(left), cachePathKey(right))
	})
	return paths, nil
}

func parseStatisticsFile(
	ctx context.Context,
	path string,
	location *time.Location,
) (sessionSummary, error) {
	parsed, err := parseStatisticsFileIncremental(
		ctx,
		path,
		location,
		cachedFile{Path: path},
	)
	return parsed.Summary, err
}

func parseStatisticsFileIncremental(
	ctx context.Context,
	path string,
	location *time.Location,
	state cachedFile,
) (cachedFile, error) {
	file, err := os.Open(path)
	if err != nil {
		return cachedFile{}, err
	}
	defer file.Close()

	if state.ParsedLength < 0 {
		state = cachedFile{Path: path}
	}
	if _, err := file.Seek(state.ParsedLength, io.SeekStart); err != nil {
		return cachedFile{}, err
	}
	initializeSessionSummary(&state.Summary)
	reader := bufio.NewReaderSize(file, 1024*1024)
	lineNumber := 0
	for {
		lineStart := state.ParsedLength
		line, tooLarge, terminated, readErr := readStatisticsLine(
			ctx,
			reader,
			maxStatisticsLineBytes,
		)
		if len(line) > 0 || tooLarge {
			lineNumber++
			if lineNumber&255 == 0 {
				if err := ctx.Err(); err != nil {
					return cachedFile{}, err
				}
			}
			var timestamp time.Time
			var current TokenBreakdown
			ok := false
			if !tooLarge {
				timestamp, current, ok = parseTokenEvent(line, location)
			}
			if ok {
				delta := tokenUsageDelta(state, current)
				state.LastUsage = current
				state.HasLastUsage = true
				if delta.TotalTokens > 0 {
					state.Summary.TotalTokens = addClamped(
						state.Summary.TotalTokens,
						delta.TotalTokens,
					)
					state.Summary.TokenTotals = addTokenBreakdown(
						state.Summary.TokenTotals,
						delta,
					)
					dateKey := timestamp.In(location).Format("2006-01-02")
					state.Summary.DailyTokens[dateKey] = addClamped(
						state.Summary.DailyTokens[dateKey],
						delta.TotalTokens,
					)
					day := state.Summary.DailyBreakdowns[dateKey]
					if day == (TokenBreakdown{}) {
						day.Complete = true
					}
					state.Summary.DailyBreakdowns[dateKey] = addTokenBreakdown(
						day,
						delta,
					)
					startsSegment := state.LastActivity.IsZero() ||
						timestamp.Before(state.LastActivity) ||
						timestamp.Sub(state.LastActivity) > activeGap
					if startsSegment {
						state.SegmentStart = timestamp
					} else if !state.SegmentStart.IsZero() {
						duration := timestamp.Sub(state.SegmentStart)
						state.Summary.LongestActiveTicks = max(
							state.Summary.LongestActiveTicks,
							duration.Nanoseconds()/100,
						)
					}
					state.LastActivity = timestamp
				}
			}
		}
		position, offsetErr := file.Seek(0, io.SeekCurrent)
		if offsetErr != nil {
			return cachedFile{}, offsetErr
		}
		state.ParsedLength = position - int64(reader.Buffered())
		if !terminated && line != "" && !tooLarge &&
			!json.Valid([]byte(line)) {
			state.ParsedLength = lineStart
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return cachedFile{}, readErr
		}
	}
	if err := ctx.Err(); err != nil {
		return cachedFile{}, err
	}
	return state, nil
}

func initializeSessionSummary(summary *sessionSummary) {
	if summary.DailyTokens == nil {
		summary.DailyTokens = map[string]int64{}
	}
	if summary.DailyBreakdowns == nil {
		summary.DailyBreakdowns = map[string]TokenBreakdown{}
	}
	if summary.TokenTotals == (TokenBreakdown{}) {
		summary.TokenTotals.Complete = true
	}
}

func readStatisticsLine(
	ctx context.Context,
	reader *bufio.Reader,
	limit int,
) (string, bool, bool, error) {
	var line strings.Builder
	tooLarge := false
	for {
		if err := ctx.Err(); err != nil {
			return "", tooLarge, false, err
		}
		fragment, err := reader.ReadSlice('\n')
		terminated := err == nil
		if terminated {
			fragment = bytes.TrimSuffix(fragment, []byte{'\n'})
			fragment = bytes.TrimSuffix(fragment, []byte{'\r'})
		}
		if !tooLarge && line.Len()+len(fragment) <= limit {
			line.Write(fragment)
		} else if len(fragment) > 0 {
			tooLarge = true
		}
		switch {
		case terminated:
			return line.String(), tooLarge, true, nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		default:
			return line.String(), tooLarge, false, err
		}
	}
}

func parseTokenEvent(
	line string,
	location *time.Location,
) (time.Time, TokenBreakdown, bool) {
	containsEvent := strings.Contains(line, `"type":"event_msg"`)
	containsCount := strings.Contains(line, `"type":"token_count"`)
	if !containsEvent || !containsCount {
		return time.Time{}, TokenBreakdown{}, false
	}
	var event tokenEvent
	if err := json.Unmarshal([]byte(line), &event); err != nil {
		return time.Time{}, TokenBreakdown{}, false
	}
	if event.Type != "event_msg" || event.Payload == nil ||
		event.Payload.Type != "token_count" || event.Payload.Info == nil ||
		event.Payload.Info.TotalTokenUsage == nil ||
		event.Payload.Info.TotalTokenUsage.TotalTokens == nil {
		return time.Time{}, TokenBreakdown{}, false
	}
	usage := event.Payload.Info.TotalTokenUsage
	if *usage.TotalTokens < 0 {
		return time.Time{}, TokenBreakdown{}, false
	}
	timestamp, ok := parseTimestamp(event.Timestamp, location)
	if !ok {
		return time.Time{}, TokenBreakdown{}, false
	}
	result := TokenBreakdown{TotalTokens: *usage.TotalTokens}
	detailed := usage.InputTokens != nil && usage.CachedInputTokens != nil &&
		usage.OutputTokens != nil &&
		usage.ReasoningOutputTokens != nil
	if !detailed {
		return timestamp, result, true
	}
	values := []*int64{
		usage.InputTokens,
		usage.CachedInputTokens,
		usage.OutputTokens,
		usage.ReasoningOutputTokens,
	}
	if usage.CacheWriteInputTokens != nil {
		values = append(values, usage.CacheWriteInputTokens)
	}
	for _, value := range values {
		if *value < 0 {
			return timestamp, result, true
		}
	}
	result.InputTokens = *usage.InputTokens
	result.CachedInputTokens = *usage.CachedInputTokens
	if usage.CacheWriteInputTokens != nil {
		result.CacheWriteInputTokens = *usage.CacheWriteInputTokens
	}
	result.OutputTokens = *usage.OutputTokens
	result.ReasoningOutputTokens = *usage.ReasoningOutputTokens
	result.Complete = result.TotalTokens == addClamped(
		result.InputTokens,
		result.OutputTokens,
	) && result.CachedInputTokens <= result.InputTokens &&
		result.ReasoningOutputTokens <= result.OutputTokens
	return timestamp, result, true
}

func tokenUsageDelta(state cachedFile, current TokenBreakdown) TokenBreakdown {
	if !state.HasLastUsage {
		return current
	}
	previous := state.LastUsage
	if current.TotalTokens < previous.TotalTokens {
		return current
	}
	result := TokenBreakdown{
		TotalTokens: current.TotalTokens - previous.TotalTokens,
		Complete:    current.Complete && previous.Complete,
	}
	if !result.Complete {
		return result
	}
	monotonic := current.InputTokens >= previous.InputTokens &&
		current.CachedInputTokens >= previous.CachedInputTokens &&
		current.CacheWriteInputTokens >= previous.CacheWriteInputTokens &&
		current.OutputTokens >= previous.OutputTokens &&
		current.ReasoningOutputTokens >= previous.ReasoningOutputTokens
	if !monotonic {
		result.Complete = false
		return result
	}
	result.InputTokens = current.InputTokens - previous.InputTokens
	result.CachedInputTokens = current.CachedInputTokens - previous.CachedInputTokens
	result.CacheWriteInputTokens = current.CacheWriteInputTokens - previous.CacheWriteInputTokens
	result.OutputTokens = current.OutputTokens - previous.OutputTokens
	result.ReasoningOutputTokens = current.ReasoningOutputTokens - previous.ReasoningOutputTokens
	return result
}

func addTokenBreakdown(left TokenBreakdown, right TokenBreakdown) TokenBreakdown {
	return TokenBreakdown{
		InputTokens: addClamped(left.InputTokens, right.InputTokens),
		CachedInputTokens: addClamped(
			left.CachedInputTokens,
			right.CachedInputTokens,
		),
		CacheWriteInputTokens: addClamped(
			left.CacheWriteInputTokens,
			right.CacheWriteInputTokens,
		),
		OutputTokens: addClamped(left.OutputTokens, right.OutputTokens),
		ReasoningOutputTokens: addClamped(
			left.ReasoningOutputTokens,
			right.ReasoningOutputTokens,
		),
		TotalTokens: addClamped(left.TotalTokens, right.TotalTokens),
		Complete:    left.Complete && right.Complete,
	}
}

func buildStatisticsSnapshot(
	sessions []sessionSummary,
	now time.Time,
	location *time.Location,
) StatisticsSnapshot {
	daily := map[string]int64{}
	dailyBreakdowns := map[string]TokenBreakdown{}
	tokenTotals := TokenBreakdown{Complete: true}
	var totalTokens int64
	var peakSession int64
	var longestActiveTicks int64
	for _, session := range sessions {
		initializeSessionSummary(&session)
		totalTokens = addClamped(totalTokens, session.TotalTokens)
		tokenTotals = addTokenBreakdown(tokenTotals, session.TokenTotals)
		peakSession = max(peakSession, session.TotalTokens)
		longestActiveTicks = max(longestActiveTicks, session.LongestActiveTicks)
		for date, tokens := range session.DailyTokens {
			if _, ok := parseDate(date); !ok {
				continue
			}
			daily[date] = addClamped(daily[date], tokens)
		}
		for date, breakdown := range session.DailyBreakdowns {
			if _, ok := parseDate(date); !ok {
				continue
			}
			current := dailyBreakdowns[date]
			if current == (TokenBreakdown{}) {
				current.Complete = true
			}
			dailyBreakdowns[date] = addTokenBreakdown(current, breakdown)
		}
	}
	activeDates := activeDateList(daily)
	todayLocal := now.In(location)
	today := calendarDate(todayLocal.Year(), todayLocal.Month(), todayLocal.Day())
	earliest := ""
	if len(activeDates) > 0 {
		earliest = formatDate(activeDates[0])
	}
	return StatisticsSnapshot{
		TotalTokens:          totalTokens,
		PeakSessionTokens:    peakSession,
		LongestActiveSeconds: longestActiveTicks / int64(time.Second/(100*time.Nanosecond)),
		CurrentStreakDays:    currentStreak(activeDates, today),
		LongestStreakDays:    longestStreak(activeDates),
		DailyTokens:          daily,
		TokenTotals:          tokenTotals,
		DailyTokenBreakdowns: dailyBreakdowns,
		Weekly:               buildWeeklyUsage(daily, today),
		Monthly:              buildMonthlyUsage(daily, today),
		EarliestActiveDate:   earliest,
		RefreshedAt:          now,
	}
}

func activeDateList(daily map[string]int64) []time.Time {
	dates := []time.Time{}
	for key, value := range daily {
		date, ok := parseDate(key)
		if ok && value > 0 {
			dates = append(dates, date)
		}
	}
	slices.SortFunc(dates, func(left time.Time, right time.Time) int {
		return left.Compare(right)
	})
	return dates
}

func currentStreak(dates []time.Time, today time.Time) int {
	if len(dates) == 0 || dates[len(dates)-1].Before(today.AddDate(0, 0, -1)) {
		return 0
	}
	set := make(map[string]struct{}, len(dates))
	for _, date := range dates {
		set[formatDate(date)] = struct{}{}
	}
	cursor := dates[len(dates)-1]
	streak := 0
	for {
		if _, ok := set[formatDate(cursor)]; !ok {
			return streak
		}
		streak++
		cursor = cursor.AddDate(0, 0, -1)
	}
}

func longestStreak(dates []time.Time) int {
	longest := 0
	current := 0
	var previous time.Time
	for _, date := range dates {
		if !previous.IsZero() && date.Equal(previous.AddDate(0, 0, 1)) {
			current++
		} else {
			current = 1
		}
		longest = max(longest, current)
		previous = date
	}
	return longest
}

func buildWeeklyUsage(daily map[string]int64, today time.Time) []WeeklyTokenUsage {
	daysSinceMonday := (int(today.Weekday()) + 6) % 7
	currentMonday := today.AddDate(0, 0, -daysSinceMonday)
	firstMonday := currentMonday.AddDate(0, 0, -7*12)
	result := make([]WeeklyTokenUsage, 0, 13)
	for week := range 13 {
		start := firstMonday.AddDate(0, 0, week*7)
		var tokens int64
		for day := range 7 {
			tokens = addClamped(tokens, daily[formatDate(start.AddDate(0, 0, day))])
		}
		result = append(result, WeeklyTokenUsage{
			StartDate: formatDate(start),
			Tokens:    tokens,
		})
	}
	return result
}

func buildMonthlyUsage(
	daily map[string]int64,
	today time.Time,
) []MonthlyCumulativeUsage {
	currentMonth := calendarDate(today.Year(), today.Month(), 1)
	firstMonth := currentMonth.AddDate(0, -11, 0)
	var cumulative int64
	for key, tokens := range daily {
		date, ok := parseDate(key)
		if ok && date.Before(firstMonth) {
			cumulative = addClamped(cumulative, tokens)
		}
	}
	result := make([]MonthlyCumulativeUsage, 0, 12)
	for index := range 12 {
		month := firstMonth.AddDate(0, index, 0)
		nextMonth := month.AddDate(0, 1, 0)
		for key, tokens := range daily {
			date, ok := parseDate(key)
			if ok && !date.Before(month) && date.Before(nextMonth) {
				cumulative = addClamped(cumulative, tokens)
			}
		}
		result = append(result, MonthlyCumulativeUsage{
			Month:            month.Format("2006-01"),
			CumulativeTokens: cumulative,
		})
	}
	return result
}

func readStatisticsCache(path string, timeZone string) statisticsCache {
	empty := statisticsCache{
		Version:  cacheVersion,
		TimeZone: timeZone,
		Files:    []cachedFile{},
	}
	data, err := readLimitedFile(path, maxStatisticsCacheBytes)
	if err != nil {
		return empty
	}
	var cache statisticsCache
	if err := json.Unmarshal(data, &cache); err != nil ||
		cache.Version != cacheVersion || cache.TimeZone != timeZone || cache.Files == nil {
		return empty
	}
	return cache
}

func saveStatisticsCache(path string, cache statisticsCache) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	data, err := json.Marshal(cache)
	if err != nil {
		return err
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".usage-statistics-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func validCachedFile(file cachedFile) bool {
	if strings.TrimSpace(file.Path) == "" || file.Length < 0 || file.ParsedLength < 0 ||
		file.ParsedLength > file.Length ||
		file.Summary.TotalTokens < 0 || file.Summary.LongestActiveTicks < 0 ||
		file.Summary.DailyTokens == nil || file.Summary.DailyBreakdowns == nil ||
		file.Summary.TokenTotals.TotalTokens != file.Summary.TotalTokens {
		return false
	}
	var dailyTotal int64
	for date, tokens := range file.Summary.DailyTokens {
		if _, ok := parseDate(date); !ok || tokens < 0 {
			return false
		}
		dailyTotal = addClamped(dailyTotal, tokens)
		breakdown, ok := file.Summary.DailyBreakdowns[date]
		if !ok || breakdown.TotalTokens != tokens || !validTokenBreakdown(breakdown) {
			return false
		}
	}
	return dailyTotal == file.Summary.TotalTokens &&
		validTokenBreakdown(file.Summary.TokenTotals)
}

func validTokenBreakdown(value TokenBreakdown) bool {
	fields := []int64{
		value.InputTokens,
		value.CachedInputTokens,
		value.CacheWriteInputTokens,
		value.OutputTokens,
		value.ReasoningOutputTokens,
		value.TotalTokens,
	}
	for _, field := range fields {
		if field < 0 {
			return false
		}
	}
	if !value.Complete {
		return true
	}
	return value.TotalTokens == addClamped(value.InputTokens, value.OutputTokens) &&
		value.CachedInputTokens <= value.InputTokens &&
		value.ReasoningOutputTokens <= value.OutputTokens
}

func cachePathKey(path string) string {
	return strings.ToLower(filepath.Clean(path))
}

func dotNetTicks(value time.Time) int64 {
	const unixEpochTicks int64 = 621355968000000000
	return unixEpochTicks + value.UnixNano()/100
}

func statisticsTimeZoneKey(location *time.Location, now time.Time) string {
	year := now.In(location).Year()
	parts := []string{location.String()}
	for _, sampleYear := range []int{year - 1, year} {
		for _, month := range []time.Month{time.January, time.April, time.July, time.October} {
			sample := time.Date(sampleYear, month, 1, 12, 0, 0, 0, location)
			name, offset := sample.Zone()
			parts = append(parts, name, strconv.Itoa(offset))
		}
	}
	return strings.Join(parts, "|")
}

func addClamped(left int64, right int64) int64 {
	if right > 0 && left > math.MaxInt64-right {
		return math.MaxInt64
	}
	return left + right
}

func parseDate(value string) (time.Time, bool) {
	date, err := time.Parse("2006-01-02", value)
	return date, err == nil
}

func calendarDate(year int, month time.Month, day int) time.Time {
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

func formatDate(value time.Time) string {
	return value.Format("2006-01-02")
}
