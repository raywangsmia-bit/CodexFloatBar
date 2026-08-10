package codexdata

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
)

var (
	modelPattern = regexp.MustCompile(
		`(?i)^\s*model\s*=\s*['"]([^'"]+)['"]\s*$`,
	)
	effortPattern = regexp.MustCompile(
		`(?i)^\s*model_reasoning_effort\s*=\s*['"]([^'"]+)['"]\s*$`,
	)
	speedTierPattern = regexp.MustCompile(
		`(?i)^\s*(?:model_)?(?:service_tier|speed_tier)\s*=\s*['"]([^'"]+)['"]\s*$`,
	)
)

func readConfig(path string) ConfigSummary {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ConfigSummary{
				State:   SourceMissing,
				Message: fmt.Sprintf("未找到配置文件: %s", path),
			}
		}
		return ConfigSummary{
			State:   SourceFailed,
			Message: fmt.Sprintf("读取配置文件失败: %v", err),
		}
	}
	defer file.Close()

	result := ConfigSummary{State: SourceAvailable}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if result.Model == "" {
			result.Model = firstMatch(modelPattern, line)
			if result.Model != "" {
				continue
			}
		}
		if result.ReasoningEffort == "" {
			result.ReasoningEffort = firstMatch(effortPattern, line)
			if result.ReasoningEffort != "" {
				continue
			}
		}
		if result.SpeedTier == "" {
			result.SpeedTier = firstMatch(speedTierPattern, line)
		}
		if result.Model != "" && result.ReasoningEffort != "" && result.SpeedTier != "" {
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return ConfigSummary{
			State:   SourceFailed,
			Message: fmt.Sprintf("读取配置文件失败: %v", err),
		}
	}
	return result
}

func firstMatch(pattern *regexp.Regexp, value string) string {
	match := pattern.FindStringSubmatch(value)
	if len(match) < 2 {
		return ""
	}
	return match[1]
}
