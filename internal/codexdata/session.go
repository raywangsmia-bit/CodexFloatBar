package codexdata

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
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
}

func readLatestSessionStatus(
	ctx context.Context,
	sessionsPath string,
	logPaths []string,
) RuntimeStatus {
	if status, ok := readLatestSessionContext(ctx, sessionsPath); ok {
		return status
	}

	var latest RuntimeStatus
	for _, path := range logPaths {
		if err := ctx.Err(); err != nil {
			return RuntimeStatus{}
		}
		text, err := readTail(path, sessionTailBytes)
		if err != nil || strings.TrimSpace(text) == "" {
			continue
		}
		if status, ok := findLatestLogStatus(text); ok {
			latest = status
		}
	}
	return latest
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
	files := []sessionFile{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) || os.IsPermission(walkErr) {
				return fs.SkipDir
			}
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(path), ".jsonl") {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		files = append(files, sessionFile{
			path:    path,
			modTime: info.ModTime().UTC().UnixNano(),
		})
		return nil
	})
	if err != nil {
		return []sessionFile{}, err
	}
	slices.SortFunc(files, func(left sessionFile, right sessionFile) int {
		if left.modTime != right.modTime {
			if left.modTime > right.modTime {
				return -1
			}
			return 1
		}
		return strings.Compare(strings.ToLower(left.path), strings.ToLower(right.path))
	})
	if limit >= 0 && len(files) > limit {
		files = files[:limit]
	}
	return files, nil
}

func findLatestSessionContext(path string) (RuntimeStatus, bool) {
	text, err := readTail(path, sessionTailBytes)
	if err != nil || strings.TrimSpace(text) == "" {
		return RuntimeStatus{}, false
	}

	var latest RuntimeStatus
	found := false
	for _, line := range bytes.Split([]byte(text), []byte{'\n'}) {
		if status, ok := parseTurnContext(line); ok {
			latest = status
			found = true
		}
	}
	speedTier := findLatestSpeedTier(text)
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
