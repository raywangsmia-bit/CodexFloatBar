package codexdata

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"strings"
)

const sessionTailBytes int64 = 2 * 1024 * 1024

var (
	feedbackTagsPattern = regexp.MustCompile(
		`(?s)feedback_tags.*?model="([^"]+)".*?effort=Some\(([^)]+)\)`,
	)
	turnLogPattern = regexp.MustCompile(
		`(?s)turn\{.*?model=([^\s}]+).*?codex\.turn\.reasoning_effort=([a-zA-Z_-]+)`,
	)
	jsonServiceTierPattern = regexp.MustCompile(
		`(?i)"service_tier"\s*:\s*(?:"([^"]+)"|(null))`,
	)
	logServiceTierPattern = regexp.MustCompile(
		`(?i)service_tier\s*[=:]\s*(?:Some\(([^)]+)\)|([^\s},]+))`,
	)
)

type sessionFile struct {
	path    string
	modTime int64
	size    int64
	mode    fs.FileMode
	info    fs.FileInfo
}

func readLatestSessionStatus(
	ctx context.Context,
	sessionsPath string,
	logPaths []string,
) RuntimeStatus {
	if status, ok := readLatestSessionContext(ctx, sessionsPath); ok {
		return status
	}

	return readLatestLogSessionStatus(ctx, logPaths)
}

func readLatestLogSessionStatus(
	ctx context.Context,
	logPaths []string,
) RuntimeStatus {
	status, _ := readLatestLogSessionStatusWithMetrics(ctx, logPaths, nil)
	return status
}

func readLatestLogSessionStatusWithMetrics(
	ctx context.Context,
	logPaths []string,
	metrics *ReadMetrics,
) (RuntimeStatus, error) {
	var latest RuntimeStatus
	for _, path := range logPaths {
		if err := ctx.Err(); err != nil {
			return RuntimeStatus{}, err
		}
		text, err := readTailContext(ctx, path, sessionTailBytes, metrics)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return RuntimeStatus{}, ctxErr
			}
			if os.IsNotExist(err) {
				continue
			}
			return RuntimeStatus{}, err
		}
		if strings.TrimSpace(text) == "" {
			continue
		}
		if status, ok := findLatestLogStatus(text); ok {
			latest = status
		}
		if err := ctx.Err(); err != nil {
			return RuntimeStatus{}, err
		}
	}
	if err := ctx.Err(); err != nil {
		return RuntimeStatus{}, err
	}
	return latest, nil
}

func readLatestSessionContext(ctx context.Context, root string) (RuntimeStatus, bool) {
	files, err := newestSessionFiles(ctx, root, 4)
	if err != nil {
		return RuntimeStatus{}, false
	}
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return RuntimeStatus{}, false
		}
		if status, ok := findLatestSessionContext(file.path); ok {
			return status, true
		}
	}
	return RuntimeStatus{}, false
}

func newestSessionFiles(
	ctx context.Context,
	root string,
	limit int,
) ([]sessionFile, error) {
	files, err := collectSessionInventory(ctx, root, nil)
	if err != nil {
		return []sessionFile{}, err
	}
	if limit >= 0 && len(files) > limit {
		files = files[:limit]
	}
	return files, nil
}

func findLatestSessionContext(path string) (RuntimeStatus, bool) {
	return findLatestSessionContextContext(context.Background(), path, nil)
}

func findLatestSessionContextContext(
	ctx context.Context,
	path string,
	metrics *ReadMetrics,
) (RuntimeStatus, bool) {
	var latest RuntimeStatus
	found := false
	var speedTier string
	err := visitTailBytesContext(ctx, path, sessionTailBytes, metrics, func(text []byte) {
		if len(bytes.TrimSpace(text)) == 0 {
			return
		}
		visitLinesReverseBytes(text, func(line []byte, _ int) bool {
			status, ok := parseTurnContext(line)
			if !ok {
				return true
			}
			latest = status
			found = true
			return false
		})
		speedTier = findLatestSpeedTierBytes(text)
	})
	if err != nil {
		return RuntimeStatus{}, false
	}
	if !found {
		return buildRuntimeStatus("", "", speedTier)
	}
	if latest.SpeedTier == "" {
		latest.SpeedTier = speedTier
	}
	return latest, true
}

func parseTurnContext(line []byte) (RuntimeStatus, bool) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(line, &root); err != nil {
		return RuntimeStatus{}, false
	}
	if readStringish(root, "type") != "turn_context" {
		return RuntimeStatus{}, false
	}

	payload, ok := readObject(root, "payload")
	if !ok {
		return RuntimeStatus{}, false
	}
	model, _ := readOptionalStringish(payload, "model")
	effort, _ := firstOptionalStringish(
		payload,
		"reasoning_effort",
		"model_reasoning_effort",
	)
	speedTier, _ := firstOptionalStringish(payload, "service_tier", "speed_tier")

	if collaboration, ok := readObject(payload, "collaboration_mode"); ok {
		if settings, ok := readObject(collaboration, "settings"); ok {
			if settingsModel, present := readOptionalStringish(settings, "model"); present {
				model = settingsModel
			}
			if settingsEffort, present := readOptionalStringish(
				settings,
				"reasoning_effort",
			); present {
				effort = settingsEffort
			}
			if settingsSpeed, present := firstOptionalStringish(
				settings,
				"service_tier",
				"speed_tier",
			); present {
				speedTier = settingsSpeed
			}
		}
	}
	return buildRuntimeStatus(model, effort, speedTier)
}

func readObject(
	object map[string]json.RawMessage,
	name string,
) (map[string]json.RawMessage, bool) {
	raw, ok := object[name]
	if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return map[string]json.RawMessage{}, false
	}
	value := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &value); err != nil {
		return map[string]json.RawMessage{}, false
	}
	return value, true
}

func readStringish(object map[string]json.RawMessage, name string) string {
	value, _ := readOptionalStringish(object, name)
	return value
}

func readOptionalStringish(
	object map[string]json.RawMessage,
	name string,
) (string, bool) {
	raw, ok := object[name]
	if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", false
	}
	var value string
	if err := json.Unmarshal(raw, &value); err == nil {
		return value, true
	}
	var scalar any
	if err := json.Unmarshal(raw, &scalar); err != nil {
		return "", false
	}
	return fmt.Sprint(scalar), true
}

func firstOptionalStringish(
	object map[string]json.RawMessage,
	names ...string,
) (string, bool) {
	for _, name := range names {
		if value, present := readOptionalStringish(object, name); present {
			return value, true
		}
	}
	return "", false
}

func findLatestLogStatus(text string) (RuntimeStatus, bool) {
	feedbackIndex, feedback := lastTwoGroupMatch(feedbackTagsPattern, text)
	turnIndex, turn := lastTwoGroupMatch(turnLogPattern, text)
	match := feedback
	if turnIndex > feedbackIndex {
		match = turn
	}
	if len(match) != 2 {
		return RuntimeStatus{}, false
	}
	model := strings.Trim(strings.TrimSpace(match[0]), `"`)
	return buildRuntimeStatus(model, match[1], findLatestSpeedTier(text))
}

func lastTwoGroupMatch(pattern *regexp.Regexp, text string) (int, []string) {
	matches := pattern.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		return -1, []string{}
	}
	match := matches[len(matches)-1]
	if len(match) < 6 || match[2] < 0 || match[4] < 0 {
		return -1, []string{}
	}
	return match[0], []string{text[match[2]:match[3]], text[match[4]:match[5]]}
}

func buildRuntimeStatus(model string, effort string, speedTier string) (RuntimeStatus, bool) {
	status := RuntimeStatus{
		Model:           strings.TrimSpace(model),
		ReasoningEffort: normalizeEffort(effort),
		SpeedTier:       normalizeSpeedTier(speedTier),
	}
	found := status.Model != "" || status.ReasoningEffort != "" || status.SpeedTier != ""
	return status, found
}

func normalizeEffort(value string) string {
	return strings.ToLower(strings.Trim(strings.TrimSpace(value), `"`))
}

func findLatestSpeedTier(text string) string {
	jsonIndex, jsonTier := lastTierMatch(jsonServiceTierPattern, text)
	logIndex, logTier := lastTierMatch(logServiceTierPattern, text)
	if jsonIndex < 0 && logIndex < 0 {
		return ""
	}
	if logIndex > jsonIndex {
		return normalizeSpeedTier(logTier)
	}
	return normalizeSpeedTier(jsonTier)
}

func findLatestSpeedTierBytes(text []byte) string {
	jsonIndex, jsonTier := lastTierMatchBytes(jsonServiceTierPattern, text)
	logIndex, logTier := lastTierMatchBytes(logServiceTierPattern, text)
	if jsonIndex < 0 && logIndex < 0 {
		return ""
	}
	if logIndex > jsonIndex {
		return normalizeSpeedTier(logTier)
	}
	return normalizeSpeedTier(jsonTier)
}

func lastTierMatch(pattern *regexp.Regexp, text string) (int, string) {
	matches := pattern.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		return -1, ""
	}
	match := matches[len(matches)-1]
	for group := 2; group+1 < len(match); group += 2 {
		if match[group] >= 0 {
			return match[0], text[match[group]:match[group+1]]
		}
	}
	return match[0], ""
}

func lastTierMatchBytes(pattern *regexp.Regexp, text []byte) (int, string) {
	matches := pattern.FindAllSubmatchIndex(text, -1)
	if len(matches) == 0 {
		return -1, ""
	}
	match := matches[len(matches)-1]
	for group := 2; group+1 < len(match); group += 2 {
		if match[group] >= 0 {
			return match[0], string(text[match[group]:match[group+1]])
		}
	}
	return match[0], ""
}

func normalizeSpeedTier(value string) string {
	normalized := strings.ToLower(strings.Trim(strings.TrimSpace(value), `"`))
	switch normalized {
	case "":
		return ""
	case "null", "none", "default", "standard", "auto":
		return "standard"
	case "priority", "fast":
		return "fast"
	default:
		return normalized
	}
}
