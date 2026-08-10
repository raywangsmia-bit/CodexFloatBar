package codexdata

import (
	"path/filepath"
	"time"

	"github.com/raywangsmia-bit/CodexFloatBar/internal/appidentity"
)

const (
	defaultPollInterval = 1500 * time.Millisecond
	cacheVersion        = 3
)

// Paths identifies every Codex source read by the data service.
type Paths struct {
	Config   string
	Auth     string
	Sessions string
	Logs     []string
	Cache    string
}

// DefaultPaths returns paths that do not overlap the WPF statistics cache.
func DefaultPaths(userHome string, localAppData string) Paths {
	codexHome := filepath.Join(userHome, ".codex")
	return Paths{
		Config:   filepath.Join(codexHome, "config.toml"),
		Auth:     filepath.Join(codexHome, "auth.json"),
		Sessions: filepath.Join(codexHome, "sessions"),
		Logs: []string{
			filepath.Join(codexHome, "logs_2.sqlite"),
			filepath.Join(codexHome, "logs_2.sqlite-wal"),
		},
		Cache: filepath.Join(
			localAppData,
			appidentity.DataDirectory,
			"usage-statistics-cache.json",
		),
	}
}

// SourceState describes whether a source was available and readable.
type SourceState string

const (
	SourceAvailable SourceState = "available"
	SourceMissing   SourceState = "missing"
	SourceFailed    SourceState = "failed"
)

// ConfigSummary is the safe subset of config.toml used by the UI.
type ConfigSummary struct {
	State           SourceState `json:"state"`
	Model           string      `json:"model"`
	ReasoningEffort string      `json:"reasoningEffort"`
	SpeedTier       string      `json:"speedTier"`
	Message         string      `json:"message"`
}

// AccountSummary intentionally excludes JWTs and raw claims.
type AccountSummary struct {
	AuthMode    string `json:"authMode"`
	DisplayText string `json:"displayText"`
}

// RuntimeStatus is the selected status after applying session-over-config precedence.
type RuntimeStatus struct {
	Model           string `json:"model"`
	ReasoningEffort string `json:"reasoningEffort"`
	SpeedTier       string `json:"speedTier"`
}

// RateLimitWindow is one Codex quota window.
type RateLimitWindow struct {
	UsedPercent      int   `json:"usedPercent"`
	RemainingPercent int   `json:"remainingPercent"`
	WindowMinutes    int   `json:"windowMinutes"`
	ResetAt          int64 `json:"resetAt"`
}

// RateLimitSummary is safe to expose to the renderer.
type RateLimitSummary struct {
	State     SourceState      `json:"state"`
	Message   string           `json:"message"`
	PlanType  string           `json:"planType"`
	Primary   *RateLimitWindow `json:"primary"`
	Secondary *RateLimitWindow `json:"secondary"`
}

// WeeklyTokenUsage contains one Monday-based week.
type WeeklyTokenUsage struct {
	StartDate string `json:"startDate"`
	Tokens    int64  `json:"tokens"`
}

// MonthlyCumulativeUsage contains cumulative usage through a calendar month.
type MonthlyCumulativeUsage struct {
	Month            string `json:"month"`
	CumulativeTokens int64  `json:"cumulativeTokens"`
}

// TokenBreakdown contains one aggregate token counter set.
type TokenBreakdown struct {
	InputTokens           int64 `json:"inputTokens"`
	CachedInputTokens     int64 `json:"cachedInputTokens"`
	CacheWriteInputTokens int64 `json:"cacheWriteInputTokens"`
	OutputTokens          int64 `json:"outputTokens"`
	ReasoningOutputTokens int64 `json:"reasoningOutputTokens"`
	TotalTokens           int64 `json:"totalTokens"`
	Complete              bool  `json:"complete"`
}

// StatisticsSnapshot contains aggregate data only, never raw session content.
type StatisticsSnapshot struct {
	TotalTokens          int64                     `json:"totalTokens"`
	PeakSessionTokens    int64                     `json:"peakSessionTokens"`
	LongestActiveSeconds int64                     `json:"longestActiveSeconds"`
	CurrentStreakDays    int                       `json:"currentStreakDays"`
	LongestStreakDays    int                       `json:"longestStreakDays"`
	DailyTokens          map[string]int64          `json:"dailyTokens"`
	TokenTotals          TokenBreakdown            `json:"-"`
	DailyTokenBreakdowns map[string]TokenBreakdown `json:"-"`
	Weekly               []WeeklyTokenUsage        `json:"weekly"`
	Monthly              []MonthlyCumulativeUsage  `json:"monthly"`
	EarliestActiveDate   string                    `json:"earliestActiveDate"`
	RefreshedAt          time.Time                 `json:"refreshedAt"`
}

// AppSnapshot is the single immutable result consumed by the native UI.
type AppSnapshot struct {
	Account     AccountSummary     `json:"account"`
	Config      ConfigSummary      `json:"config"`
	Runtime     RuntimeStatus      `json:"runtime"`
	RateLimit   RateLimitSummary   `json:"rateLimit"`
	Statistics  StatisticsSnapshot `json:"statistics"`
	RefreshedAt time.Time          `json:"refreshedAt"`
}
