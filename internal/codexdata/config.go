package codexdata

import (
	"bufio"
	"context"
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
	result, _ := readConfigContext(context.Background(), path, nil)
	return result
}

func readConfigContext(
	ctx context.Context,
	path string,
	metrics *ReadMetrics,
) (ConfigSummary, error) {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return missingConfigSummary(path), err
		}
		return failedConfigSummary(err), err
	}
	defer file.Close()

	result := ConfigSummary{State: SourceAvailable}
	reader := &contextChunkReader{
		ctx:     ctx,
		reader:  file,
		metrics: metrics,
		kind:    sourceReadConfig,
	}
	scanner := bufio.NewScanner(reader)
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
		failed := failedConfigSummary(err)
		if reader.readError != nil {
			return failed, reader.readError
		}
		return failed, nil
	}
	return result, nil
}

func missingConfigSummary(path string) ConfigSummary {
	return ConfigSummary{
		State:   SourceMissing,
		Message: fmt.Sprintf("未找到配置文件: %s", path),
	}
}

func failedConfigSummary(err error) ConfigSummary {
	return ConfigSummary{
		State:   SourceFailed,
		Message: fmt.Sprintf("读取配置文件失败: %v", err),
	}
}

func firstMatch(pattern *regexp.Regexp, value string) string {
	match := pattern.FindStringSubmatch(value)
	if len(match) < 2 {
		return ""
	}
	return match[1]
}
