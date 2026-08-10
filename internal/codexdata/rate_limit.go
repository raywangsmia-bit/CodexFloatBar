package codexdata

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	rateLogTailBytes     int64 = 8 * 1024 * 1024
	rateSessionTailBytes int64 = 2 * 1024 * 1024
	rateEventMarker            = `"type":"codex.rate_limits"`
	maxRateCandidates          = 1024
	maxRateObjectBytes         = 4 * 1024 * 1024
	maxRateBraceAttempts       = 256
)

type rateLimitCandidate struct {
	json        string
	observedAt  *int64
	pathIndex   int
	markerIndex int
}

func readLatestRateLimit(
	ctx context.Context,
	sessionsPath string,
	logPaths []string,
	location *time.Location,
) RateLimitSummary {
	if candidate := findLatestSessionRateLimit(ctx, sessionsPath); candidate != nil {
		return summarizeRateLimit(*candidate, location)
	}

	var latest *rateLimitCandidate
	for pathIndex, path := range logPaths {
		if err := ctx.Err(); err != nil {
			return unavailableRateLimit("等待 Codex 用量记录")
		}
		for _, candidate := range findLogRateLimitCandidates(
			ctx,
			path,
			pathIndex,
		) {
			if newerRateLimitCandidate(candidate, latest) {
				copy := candidate
				latest = &copy
			}
		}
	}
	if latest == nil {
		return unavailableRateLimit("等待 Codex 用量记录")
	}
	return summarizeRateLimit(*latest, location)
}

func findLatestSessionRateLimit(
	ctx context.Context,
	sessionsPath string,
) *rateLimitCandidate {
	files, err := newestSessionFiles(ctx, sessionsPath, 16)
	if err != nil {
		return nil
	}
	var latest *rateLimitCandidate
	for fileIndex, file := range files {
		if err := ctx.Err(); err != nil {
			return nil
		}
		candidate := findSessionRateLimit(ctx, file.path, fileIndex)
		if candidate != nil && newerRateLimitCandidate(*candidate, latest) {
			latest = candidate
		}
	}
	return latest
}

func findSessionRateLimit(
	ctx context.Context,
	path string,
	fileIndex int,
) *rateLimitCandidate {
	text, err := readTail(path, rateSessionTailBytes)
	if err != nil || strings.TrimSpace(text) == "" {
		return nil
	}
	lines := strings.Split(text, "\n")
	for index := len(lines) - 1; index >= 0; index-- {
		if index&255 == 0 && ctx.Err() != nil {
			return nil
		}
		line := strings.TrimSuffix(lines[index], "\r")
		if !strings.Contains(line, `"rate_limits"`) {
			continue
		}
		root, ok := decodeObject([]byte(line))
		if !ok {
			continue
		}
		if _, _, ok := getRateLimits(root); !ok {
			continue
		}
		return &rateLimitCandidate{
			json:        line,
			observedAt:  observedAt(root),
			pathIndex:   fileIndex,
			markerIndex: index,
		}
	}
	return nil
}

func findLogRateLimitCandidates(
	ctx context.Context,
	path string,
	pathIndex int,
) []rateLimitCandidate {
	text, err := readTail(path, rateLogTailBytes)
	if err != nil || strings.TrimSpace(text) == "" {
		return []rateLimitCandidate{}
	}

	type objectScan struct {
		end        int
		json       string
		observedAt *int64
		valid      bool
	}
	bracePositions := make([]int, 0, strings.Count(text, "{"))
	markerPositions := []int{}
	for index := 0; index < len(text); index++ {
		if index&0xffff == 0 && ctx.Err() != nil {
			return []rateLimitCandidate{}
		}
		if text[index] == '{' {
			bracePositions = append(bracePositions, index)
		}
		if strings.HasPrefix(text[index:], rateEventMarker) {
			markerPositions = append(markerPositions, index)
			index += len(rateEventMarker) - 1
		}
	}
	scans := make(map[int]objectScan, min(len(bracePositions), maxRateCandidates*2))
	candidates := []rateLimitCandidate{}
	ringIndex := 0
	ringWrapped := false
	for _, markerIndex := range markerPositions {
		if ctx.Err() != nil {
			return []rateLimitCandidate{}
		}
		braceIndex := sort.SearchInts(bracePositions, markerIndex) - 1
		for attempt := 0; braceIndex >= 0 && attempt < maxRateBraceAttempts; attempt++ {
			start := bracePositions[braceIndex]
			braceIndex--
			scan, cached := scans[start]
			if !cached {
				candidateJSON, end, ok := extractJSONObjectBounded(
					ctx,
					text,
					start,
					maxRateObjectBytes,
				)
				scan = objectScan{end: end, json: candidateJSON}
				if ok {
					root, decoded := decodeObject([]byte(candidateJSON))
					scan.valid = decoded && isRateLimitEvent(root)
					if scan.valid {
						scan.observedAt = observedAt(root)
					}
				}
				scans[start] = scan
			}
			markerEnd := markerIndex + len(rateEventMarker)
			if !scan.valid || scan.end < markerEnd {
				continue
			}
			candidate := rateLimitCandidate{
				json:        scan.json,
				observedAt:  scan.observedAt,
				pathIndex:   pathIndex,
				markerIndex: markerIndex,
			}
			candidates, ringIndex, ringWrapped = appendRateCandidate(
				candidates,
				candidate,
				ringIndex,
				ringWrapped,
			)
			break
		}
	}
	if ringWrapped {
		ordered := make([]rateLimitCandidate, 0, len(candidates))
		ordered = append(ordered, candidates[ringIndex:]...)
		ordered = append(ordered, candidates[:ringIndex]...)
		return ordered
	}
	return candidates
}

func appendRateCandidate(
	candidates []rateLimitCandidate,
	candidate rateLimitCandidate,
	ringIndex int,
	ringWrapped bool,
) ([]rateLimitCandidate, int, bool) {
	if len(candidates) < maxRateCandidates {
		return append(candidates, candidate), ringIndex, ringWrapped
	}
	candidates[ringIndex] = candidate
	ringIndex = (ringIndex + 1) % maxRateCandidates
	return candidates, ringIndex, true
}

func extractJSONObjectBounded(
	ctx context.Context,
	text string,
	start int,
	maxBytes int,
) (string, int, bool) {
	if start < 0 || start >= len(text) || text[start] != '{' {
		return "", -1, false
	}
	endLimit := min(len(text), start+maxBytes)
	depth := 0
	inString := false
	escaped := false
	for index := start; index < endLimit; index++ {
		if index&0xffff == 0 && ctx.Err() != nil {
			return "", -1, false
		}
		character := text[index]
		if inString {
			switch {
			case escaped:
				escaped = false
			case character == '\\':
				escaped = true
			case character == '"':
				inString = false
			}
			continue
		}
		switch character {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return text[start : index+1], index + 1, true
			}
		}
	}
	return "", -1, false
}

func summarizeRateLimit(
	candidate rateLimitCandidate,
	location *time.Location,
) RateLimitSummary {
	root, ok := decodeObject([]byte(candidate.json))
	if !ok {
		return unavailableRateLimit("等待有效用量记录")
	}
	limits, planType, ok := getRateLimits(root)
	if !ok {
		return unavailableRateLimit("等待有效用量记录")
	}
	primary := readRateLimitWindow(limits, "primary")
	secondary := readRateLimitWindow(limits, "secondary")
	if primary == nil && secondary == nil {
		return unavailableRateLimit("等待额度窗口记录")
	}

	parts := []string{}
	if primary != nil {
		parts = append(parts, formatRateLimitWindow(primary, location))
	}
	if secondary != nil {
		parts = append(parts, formatRateLimitWindow(secondary, location))
	}
	formattedPlan := ""
	if strings.TrimSpace(planType) != "" {
		formattedPlan = formatPlanType(planType)
	}
	message := strings.Join(parts, " | ")
	if formattedPlan != "" {
		message += " | " + formattedPlan
	}
	return RateLimitSummary{
		State:     SourceAvailable,
		Message:   message,
		PlanType:  formattedPlan,
		Primary:   primary,
		Secondary: secondary,
	}
}

func unavailableRateLimit(message string) RateLimitSummary {
	return RateLimitSummary{
		State:   SourceMissing,
		Message: message,
	}
}

func getRateLimits(
	root map[string]json.RawMessage,
) (map[string]json.RawMessage, string, bool) {
	if limits, ok := readObject(root, "rate_limits"); ok && isOverallCodexLimit(limits) {
		plan := firstOptionalStrictString(root, limits, "plan_type")
		return limits, plan, true
	}
	payload, ok := readObject(root, "payload")
	if !ok {
		return map[string]json.RawMessage{}, "", false
	}
	limits, ok := readObject(payload, "rate_limits")
	if !ok || !isOverallCodexLimit(limits) {
		return map[string]json.RawMessage{}, "", false
	}
	plan := firstOptionalStrictString(payload, limits, "plan_type")
	return limits, plan, true
}

func isOverallCodexLimit(limits map[string]json.RawMessage) bool {
	limitID := readStrictString(limits, "limit_id")
	return strings.TrimSpace(limitID) == "" || strings.EqualFold(limitID, "codex")
}

func readRateLimitWindow(
	limits map[string]json.RawMessage,
	name string,
) *RateLimitWindow {
	window, ok := readObject(limits, name)
	if !ok {
		return nil
	}
	usedPercent, ok := readRoundedInt(window, "used_percent")
	if !ok {
		return nil
	}
	windowMinutes, ok := readRoundedInt(window, "window_minutes")
	if !ok {
		return nil
	}
	resetAt, ok := readRoundedInt64(window, "reset_at")
	if !ok {
		resetAt, ok = readRoundedInt64(window, "resets_at")
	}
	if !ok {
		return nil
	}
	return &RateLimitWindow{
		UsedPercent:      usedPercent,
		RemainingPercent: max(0, min(100, 100-usedPercent)),
		WindowMinutes:    windowMinutes,
		ResetAt:          resetAt,
	}
}

func observedAt(root map[string]json.RawMessage) *int64 {
	if timestamp, ok := parseTimestamp(readStrictString(root, "timestamp"), time.UTC); ok {
		value := timestamp.Unix()
		return &value
	}
	limits, _, ok := getRateLimits(root)
	if !ok {
		return nil
	}
	var latest *int64
	for _, name := range []string{"primary", "secondary"} {
		value := windowObservedAt(limits, name)
		if value != nil && (latest == nil || *value > *latest) {
			latest = value
		}
	}
	return latest
}

func windowObservedAt(
	limits map[string]json.RawMessage,
	name string,
) *int64 {
	window, ok := readObject(limits, name)
	if !ok {
		return nil
	}
	resetAt, ok := readRoundedInt64(window, "reset_at")
	if !ok {
		resetAt, ok = readRoundedInt64(window, "resets_at")
	}
	if !ok {
		return nil
	}
	if resetAfter, ok := readRoundedInt64(window, "reset_after_seconds"); ok {
		resetAt -= resetAfter
	}
	return &resetAt
}

func newerRateLimitCandidate(
	candidate rateLimitCandidate,
	latest *rateLimitCandidate,
) bool {
	if latest == nil {
		return true
	}
	if candidate.observedAt != nil || latest.observedAt != nil {
		switch {
		case candidate.observedAt == nil:
			return false
		case latest.observedAt == nil:
			return true
		case *candidate.observedAt != *latest.observedAt:
			return *candidate.observedAt > *latest.observedAt
		}
	}
	if candidate.pathIndex != latest.pathIndex {
		return candidate.pathIndex > latest.pathIndex
	}
	return candidate.markerIndex > latest.markerIndex
}

func isRateLimitEvent(root map[string]json.RawMessage) bool {
	if readStrictString(root, "type") != "codex.rate_limits" {
		return false
	}
	_, ok := readObject(root, "rate_limits")
	return ok
}

func decodeObject(data []byte) (map[string]json.RawMessage, bool) {
	if len(bytes.TrimSpace(data)) == 0 {
		return map[string]json.RawMessage{}, false
	}
	value := map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &value); err != nil {
		return map[string]json.RawMessage{}, false
	}
	return value, true
}

func readStrictString(object map[string]json.RawMessage, name string) string {
	value, _ := readOptionalStrictString(object, name)
	return value
}

func readOptionalStrictString(
	object map[string]json.RawMessage,
	name string,
) (string, bool) {
	raw, ok := object[name]
	if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", false
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false
	}
	return value, true
}

func firstOptionalStrictString(
	first map[string]json.RawMessage,
	second map[string]json.RawMessage,
	name string,
) string {
	if value, present := readOptionalStrictString(first, name); present {
		return value
	}
	value, _ := readOptionalStrictString(second, name)
	return value
}

func readRoundedInt(
	object map[string]json.RawMessage,
	name string,
) (int, bool) {
	value, ok := readRoundedInt64(object, name)
	if !ok || value < math.MinInt32 || value > math.MaxInt32 {
		return 0, false
	}
	return int(value), true
}

func readRoundedInt64(
	object map[string]json.RawMessage,
	name string,
) (int64, bool) {
	raw, ok := object[name]
	if !ok {
		return 0, false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value json.Number
	if err := decoder.Decode(&value); err != nil {
		return 0, false
	}
	if integer, err := strconv.ParseInt(value.String(), 10, 64); err == nil {
		return integer, true
	}
	decimal, err := strconv.ParseFloat(value.String(), 64)
	if err != nil || math.IsNaN(decimal) || math.IsInf(decimal, 0) {
		return 0, false
	}
	rounded := math.RoundToEven(decimal)
	if rounded < math.MinInt64 || rounded > math.MaxInt64 {
		return 0, false
	}
	return int64(rounded), true
}

func parseTimestamp(value string, location *time.Location) (time.Time, bool) {
	if strings.TrimSpace(value) == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{
		time.RFC3339Nano,
		"2006-01-02 15:04:05Z07:00",
		time.RFC1123Z,
		time.RFC1123,
		time.RFC850,
		time.RFC822Z,
		time.RFC822,
	} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed, true
		}
	}
	for _, layout := range []string{
		"2006-01-02T15:04:05.999999999",
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
		"01/02/2006 15:04:05",
		"01/02/2006 3:04:05 PM",
		"2006-01-02",
	} {
		if parsed, err := time.ParseInLocation(layout, value, location); err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}

func formatRateLimitWindow(window *RateLimitWindow, location *time.Location) string {
	reset := time.Unix(window.ResetAt, 0).In(location).Format("1/2 15:04")
	return formatWindowName(window.WindowMinutes) + " " +
		strconv.Itoa(window.RemainingPercent) + "% " + reset
}

func formatWindowName(minutes int) string {
	switch {
	case minutes == 300:
		return "5小时"
	case minutes == 10080:
		return "1周"
	case minutes%1440 == 0:
		return strconv.Itoa(minutes/1440) + "天"
	case minutes%60 == 0:
		return strconv.Itoa(minutes/60) + "小时"
	default:
		return strconv.Itoa(minutes) + "分钟"
	}
}

func formatPlanType(value string) string {
	switch value {
	case "prolite":
		return "Pro Lite"
	case "pro":
		return "Pro"
	case "plus":
		return "Plus"
	case "free":
		return "Free"
	default:
		return value
	}
}
