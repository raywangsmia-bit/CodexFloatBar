package codexdata

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var (
	fixtureLocation = time.FixedZone("fixture-utc+8", 8*60*60)
	fixtureNow      = time.Date(2026, 8, 9, 12, 0, 0, 0, fixtureLocation)
)

func TestNormalFixtureSnapshotMatchesGolden(t *testing.T) {
	paths := materializeFixture(t, "normal")
	service := fixtureService(paths)

	snapshot, err := service.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	actual, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	actual = append(actual, '\n')
	expected, err := os.ReadFile(filepath.Join("testdata", "golden", "normal.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(actual) != string(expected) {
		t.Fatalf("snapshot differs from C# contract golden\nactual:\n%s", actual)
	}
}

func TestCorruptAndMissingInputsReturnSafeEmptySnapshot(t *testing.T) {
	paths := materializeFixture(t, "corrupt")
	paths.Config = filepath.Join(t.TempDir(), "missing-config.toml")
	paths.Logs = []string{
		filepath.Join(t.TempDir(), "missing.sqlite"),
		filepath.Join(t.TempDir(), "missing.sqlite-wal"),
	}
	paths.Cache = filepath.Join(paths.Sessions, "..", "usage-statistics-cache.json")

	snapshot, err := fixtureService(paths).Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Account.DisplayText != "Codex: ChatGPT" {
		t.Fatalf("unexpected account: %+v", snapshot.Account)
	}
	if snapshot.Config.State != SourceMissing || snapshot.Runtime != (RuntimeStatus{}) {
		t.Fatalf("unexpected config/runtime: %+v / %+v", snapshot.Config, snapshot.Runtime)
	}
	if snapshot.RateLimit.State != SourceMissing || snapshot.RateLimit.Primary != nil {
		t.Fatalf("unexpected rate limit: %+v", snapshot.RateLimit)
	}
	if snapshot.Statistics.TotalTokens != 0 || snapshot.Statistics.DailyTokens == nil {
		t.Fatalf("unexpected statistics: %+v", snapshot.Statistics)
	}
}

func TestStatisticsCacheInvalidatesAndCorruptCacheRebuilds(t *testing.T) {
	paths := materializeFixture(t, "normal")
	service := fixtureService(paths)
	first, err := service.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first.Statistics.TotalTokens != 830 {
		t.Fatalf("initial total = %d, want 830", first.Statistics.TotalTokens)
	}

	secondary := filepath.Join(paths.Sessions, "2026", "08", "01", "secondary.jsonl")
	file, err := os.OpenFile(secondary, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	line := `{"timestamp":"2026-08-09T12:00:00+08:00","type":"event_msg",` +
		`"payload":{"type":"token_count","info":{"total_token_usage":{"total_tokens":550}}}}` + "\n"
	if _, err := file.WriteString(line); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	touch := fixtureNow.Add(2 * time.Hour)
	if err := os.Chtimes(secondary, touch, touch); err != nil {
		t.Fatal(err)
	}

	second, err := service.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if second.Statistics.TotalTokens != 880 || second.Statistics.PeakSessionTokens != 550 {
		t.Fatalf("changed snapshot: %+v", second.Statistics)
	}
	if err := os.WriteFile(paths.Cache, []byte("{broken-cache"), 0o600); err != nil {
		t.Fatal(err)
	}
	third, err := service.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if third.Statistics.TotalTokens != 880 {
		t.Fatalf("rebuilt total = %d, want 880", third.Statistics.TotalTokens)
	}
}

func TestStatisticsCacheReusesCompletedDataAndParsesOnlyAppendedUsage(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "sessions", "usage.jsonl")
	cachePath := filepath.Join(root, "cache", "statistics.json")
	firstLine := `{"timestamp":"2026-08-09T01:00:00+08:00","type":"event_msg",` +
		`"payload":{"type":"token_count","info":{"total_token_usage":{` +
		`"input_tokens":100,"cached_input_tokens":60,"output_tokens":20,` +
		`"reasoning_output_tokens":5,"total_tokens":120}}}}` + "\n"
	mustWrite(t, path, firstLine)
	paths := Paths{
		Config:   filepath.Join(root, "missing-config.toml"),
		Auth:     filepath.Join(root, "missing-auth.json"),
		Sessions: filepath.Dir(path),
		Cache:    cachePath,
	}
	service := fixtureService(paths)

	first, err := service.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	wantFirst := TokenBreakdown{
		InputTokens:           100,
		CachedInputTokens:     60,
		OutputTokens:          20,
		ReasoningOutputTokens: 5,
		TotalTokens:           120,
		Complete:              true,
	}
	if first.Statistics.TokenTotals != wantFirst {
		t.Fatalf("initial breakdown = %+v, want %+v", first.Statistics.TokenTotals, wantFirst)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	// The cache key is length plus modification time. Replacing an already cached
	// prefix while restoring both proves an unchanged file is not scanned again.
	replacement := strings.Replace(firstLine, `"total_tokens":120`, `"total_tokens":999`, 1)
	if len(replacement) != len(firstLine) {
		t.Fatal("test replacement changed file length")
	}
	if err := os.WriteFile(path, []byte(replacement), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	reused, err := service.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if reused.Statistics.TotalTokens != 120 {
		t.Fatalf("unchanged cached file was rescanned: total = %d", reused.Statistics.TotalTokens)
	}

	secondLine := `{"timestamp":"2026-08-09T02:00:00+08:00","type":"event_msg",` +
		`"payload":{"type":"token_count","info":{"total_token_usage":{` +
		`"input_tokens":140,"cached_input_tokens":80,"output_tokens":30,` +
		`"reasoning_output_tokens":9,"total_tokens":170}}}}` + "\n"
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(secondLine); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	touch := info.ModTime().Add(time.Second)
	if err := os.Chtimes(path, touch, touch); err != nil {
		t.Fatal(err)
	}

	appended, err := service.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	wantAppended := TokenBreakdown{
		InputTokens:           140,
		CachedInputTokens:     80,
		OutputTokens:          30,
		ReasoningOutputTokens: 9,
		TotalTokens:           170,
		Complete:              true,
	}
	if appended.Statistics.TokenTotals != wantAppended {
		t.Fatalf("appended breakdown = %+v, want %+v", appended.Statistics.TokenTotals, wantAppended)
	}
	cache := readStatisticsCache(
		cachePath,
		statisticsTimeZoneKey(fixtureLocation, fixtureNow),
	)
	var entry cachedFile
	ok := false
	for _, cached := range cache.Files {
		if cached.Path == path {
			entry = cached
			ok = true
			break
		}
	}
	if !ok || entry.ParsedLength != int64(len(firstLine)+len(secondLine)) {
		t.Fatalf("incremental cache entry = %+v, found=%v", entry, ok)
	}

	thirdLine := `{"timestamp":"2026-08-09T03:00:00+08:00","type":"event_msg",` +
		`"payload":{"type":"token_count","info":{"total_token_usage":{` +
		`"input_tokens":160,"cached_input_tokens":90,"output_tokens":40,` +
		`"reasoning_output_tokens":11,"total_tokens":200}}}}` + "\n"
	cut := len(thirdLine) / 2
	file, err = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(thirdLine[:cut]); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	touch = touch.Add(time.Second)
	if err := os.Chtimes(path, touch, touch); err != nil {
		t.Fatal(err)
	}
	partial, err := service.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if partial.Statistics.TotalTokens != 170 {
		t.Fatalf("partial trailing record changed total to %d", partial.Statistics.TotalTokens)
	}
	partialCache := readStatisticsCache(
		cachePath,
		statisticsTimeZoneKey(fixtureLocation, fixtureNow),
	)
	if len(partialCache.Files) != 1 ||
		partialCache.Files[0].ParsedLength != int64(len(firstLine)+len(secondLine)) {
		t.Fatalf("partial record cache = %+v", partialCache.Files)
	}

	file, err = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(thirdLine[cut:]); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	touch = touch.Add(time.Second)
	if err := os.Chtimes(path, touch, touch); err != nil {
		t.Fatal(err)
	}
	completed, err := service.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if completed.Statistics.TotalTokens != 200 {
		t.Fatalf("completed trailing record total = %d, want 200", completed.Statistics.TotalTokens)
	}
}

func TestSessionFieldsOverrideConfigIndependently(t *testing.T) {
	root := t.TempDir()
	paths := Paths{
		Config:   filepath.Join(root, "config.toml"),
		Auth:     filepath.Join(root, "auth.json"),
		Sessions: filepath.Join(root, "sessions"),
		Logs:     []string{},
		Cache:    filepath.Join(root, "cache.json"),
	}
	mustWrite(t, paths.Config, "model='config-model'\nmodel_reasoning_effort='high'\nspeed_tier='priority'\n")
	mustWrite(
		t,
		filepath.Join(paths.Sessions, "status.jsonl"),
		`{"type":"turn_context","payload":{"model":"session-model"}}`+"\n",
	)
	snapshot, err := fixtureService(paths).Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := RuntimeStatus{
		Model:           "session-model",
		ReasoningEffort: "high",
		SpeedTier:       "priority",
	}
	if snapshot.Runtime != want {
		t.Fatalf("runtime = %+v, want %+v", snapshot.Runtime, want)
	}
}

func TestSessionNullablePrecedenceMatchesCSharp(t *testing.T) {
	root := t.TempDir()
	paths := Paths{
		Config:   filepath.Join(root, "config.toml"),
		Auth:     filepath.Join(root, "auth.json"),
		Sessions: filepath.Join(root, "sessions"),
		Logs:     []string{},
		Cache:    filepath.Join(root, "cache.json"),
	}
	mustWrite(t, paths.Config, "model='config-model'\n")
	mustWrite(
		t,
		filepath.Join(paths.Sessions, "status.jsonl"),
		`{"type":"turn_context","payload":{"model":"payload-model",`+
			`"reasoning_effort":"low","service_tier":"standard",`+
			`"collaboration_mode":{"settings":{"model":""}}}}`+"\n"+
			`{"type":"event_msg","service_tier":"priority"}`+"\n",
	)
	snapshot, err := fixtureService(paths).Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := RuntimeStatus{
		Model:           "config-model",
		ReasoningEffort: "low",
		SpeedTier:       "standard",
	}
	if snapshot.Runtime != want {
		t.Fatalf("runtime = %+v, want %+v", snapshot.Runtime, want)
	}
}

func TestSessionRateCandidatePreventsLogFallback(t *testing.T) {
	root := t.TempDir()
	paths := Paths{
		Config:   filepath.Join(root, "config.toml"),
		Auth:     filepath.Join(root, "auth.json"),
		Sessions: filepath.Join(root, "sessions"),
		Logs: []string{
			filepath.Join(root, "logs_2.sqlite"),
			filepath.Join(root, "logs_2.sqlite-wal"),
		},
		Cache: filepath.Join(root, "cache.json"),
	}
	mustWrite(
		t,
		filepath.Join(paths.Sessions, "incomplete.jsonl"),
		`{"timestamp":"2026-08-09T00:00:00Z","rate_limits":{"limit_id":"codex",`+
			`"primary":{"used_percent":20}}}`+"\n",
	)
	mustWrite(
		t,
		paths.Logs[0],
		`{"type":"codex.rate_limits","rate_limits":{"primary":`+
			`{"used_percent":10,"window_minutes":300,"reset_at":1786251600}}}`,
	)
	snapshot, err := fixtureService(paths).Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.RateLimit.Message != "等待额度窗口记录" || snapshot.RateLimit.Primary != nil {
		t.Fatalf("session candidate did not block fallback: %+v", snapshot.RateLimit)
	}
}

func TestRateLimitRoundsMidpointsToEvenAndWALWinsTie(t *testing.T) {
	root := t.TempDir()
	paths := Paths{
		Config:   filepath.Join(root, "config.toml"),
		Auth:     filepath.Join(root, "auth.json"),
		Sessions: filepath.Join(root, "missing-sessions"),
		Logs: []string{
			filepath.Join(root, "logs_2.sqlite"),
			filepath.Join(root, "logs_2.sqlite-wal"),
		},
		Cache: filepath.Join(root, "cache.json"),
	}
	mainEvent := `{"type":"codex.rate_limits","timestamp":"2026-08-09T00:00:00Z",` +
		`"rate_limits":{"primary":{"used_percent":70,"window_minutes":300,"reset_at":1786251600}}}`
	walEvent := `{"type":"codex.rate_limits","timestamp":"2026-08-09T00:00:00Z",` +
		`"rate_limits":{"primary":{"used_percent":10.5,"window_minutes":300,"reset_at":1786251600}}}`
	mustWrite(t, paths.Logs[0], mainEvent)
	mustWrite(t, paths.Logs[1], walEvent)
	snapshot, err := fixtureService(paths).Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	window := snapshot.RateLimit.Primary
	if window == nil || window.UsedPercent != 10 || window.RemainingPercent != 90 {
		t.Fatalf("unexpected rounded WAL window: %+v", window)
	}
}

func TestRateLimitRawLimitIDAndPlanPrecedence(t *testing.T) {
	root := t.TempDir()
	invalid := `{"rate_limits":{"limit_id":" codex ","primary":` +
		`{"used_percent":10,"window_minutes":300,"reset_at":1786251600}}}`
	if candidate := findSessionRateLimit(
		context.Background(),
		writeOneLine(t, root, "invalid.jsonl", invalid),
		0,
	); candidate != nil {
		t.Fatalf("spaced limit_id was accepted: %+v", candidate)
	}

	valid := `{"plan_type":"","rate_limits":{"plan_type":"plus","primary":` +
		`{"used_percent":10,"window_minutes":300,"reset_at":1786251600}}}`
	candidate := findSessionRateLimit(
		context.Background(),
		writeOneLine(t, root, "valid.jsonl", valid),
		0,
	)
	if candidate == nil {
		t.Fatal("valid rate candidate was not found")
	}
	summary := summarizeRateLimit(*candidate, fixtureLocation)
	if summary.PlanType != "" || strings.HasSuffix(summary.Message, "Plus") {
		t.Fatalf("empty root plan did not block nested plan: %+v", summary)
	}
	nullPlan := `{"plan_type":null,"rate_limits":{"plan_type":"plus","primary":` +
		`{"used_percent":10,"window_minutes":300,"reset_at":1786251600}}}`
	candidate = findSessionRateLimit(
		context.Background(),
		writeOneLine(t, root, "null-plan.jsonl", nullPlan),
		0,
	)
	if candidate == nil || summarizeRateLimit(*candidate, fixtureLocation).PlanType != "Plus" {
		t.Fatal("null root plan did not fall back to nested plan")
	}
	blankPlan := `{"plan_type":"   ","rate_limits":{"plan_type":"plus","primary":` +
		`{"used_percent":10,"window_minutes":300,"reset_at":1786251600}}}`
	candidate = findSessionRateLimit(
		context.Background(),
		writeOneLine(t, root, "blank-plan.jsonl", blankPlan),
		0,
	)
	summary = summarizeRateLimit(*candidate, fixtureLocation)
	if summary.PlanType != "" || strings.Contains(summary.Message, " |    ") {
		t.Fatalf("whitespace plan should be unavailable: %+v", summary)
	}
}

func TestConfigAndAccountPreserveSignificantWhitespace(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "config.toml")
	mustWrite(t, configPath, "model='  spaced model  '\n")
	config := readConfig(configPath)
	if config.Model != "  spaced model  " {
		t.Fatalf("config model = %q", config.Model)
	}
	paths := Paths{
		Config:   configPath,
		Auth:     filepath.Join(root, "missing-auth.json"),
		Sessions: filepath.Join(root, "missing-sessions"),
		Logs:     []string{},
		Cache:    filepath.Join(root, "cache.json"),
	}
	snapshot, err := fixtureService(paths).Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Runtime.Model != "  spaced model  " {
		t.Fatalf("runtime model = %q", snapshot.Runtime.Model)
	}
	authPath := filepath.Join(root, "auth.json")
	mustWrite(t, authPath, `{"auth_mode":"","tokens":{}}`)
	account := readAccount(authPath)
	if account.DisplayText != "Codex: " {
		t.Fatalf("empty auth mode display = %q", account.DisplayText)
	}
	payload := base64.RawURLEncoding.EncodeToString(
		[]byte(`{"name":123,"email":"fixture@example.invalid"}`),
	)
	mustWrite(
		t,
		authPath,
		`{"auth_mode":123,"tokens":{"id_token":"fixture.`+payload+`.signature"}}`,
	)
	account = readAccount(authPath)
	if account.DisplayText != "Codex: fixture@example.invalid" {
		t.Fatalf("independent claim parsing display = %q", account.DisplayText)
	}
}

func TestTimestampDefaultZoneMatchesEachCSharpCallSite(t *testing.T) {
	value := "2026-08-09 03:00:00"
	rateTime, ok := parseTimestamp(value, time.UTC)
	if !ok || rateTime.Format(time.RFC3339) != "2026-08-09T03:00:00Z" {
		t.Fatalf("rate timestamp = %v, %v", rateTime, ok)
	}
	usageTime, ok := parseTimestamp(value, fixtureLocation)
	if !ok || usageTime.Format(time.RFC3339) != "2026-08-09T03:00:00+08:00" {
		t.Fatalf("usage timestamp = %v, %v", usageTime, ok)
	}
}

func TestTailBoundsAndCancellation(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "large.jsonl")
	prefix := `{"type":"turn_context","payload":{"model":"outside-tail"}}` + "\n"
	padding := strings.Repeat("x", int(sessionTailBytes)+1024)
	last := `{"type":"turn_context","payload":{"model":"inside-tail",` +
		`"reasoning_effort":"medium"}}` + "\n"
	mustWrite(t, path, prefix+padding+"\n"+last)
	status, ok := findLatestSessionContext(path)
	if !ok || status.Model != "inside-tail" {
		t.Fatalf("tail status = %+v, %v", status, ok)
	}

	statisticsPath := filepath.Join(root, "cancel.jsonl")
	line := `{"timestamp":"2026-08-09T00:00:00Z","type":"event_msg",` +
		`"payload":{"type":"token_count","info":{"total_token_usage":{"total_tokens":1}}}}` + "\n"
	mustWrite(t, statisticsPath, strings.Repeat(line, 300))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := parseStatisticsFile(ctx, statisticsPath, fixtureLocation)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled parse error = %v", err)
	}
}

func TestMonitorRefreshesAfterMtimeChange(t *testing.T) {
	paths := materializeFixture(t, "normal")
	service := fixtureService(paths)
	monitor := NewMonitor(service, MonitorOptions{PollInterval: 10 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- monitor.Run(ctx) }()

	first := receiveSnapshot(t, monitor.Updates())
	if first.Config.Model != "gpt-fixture-config" {
		t.Fatalf("initial config model = %q", first.Config.Model)
	}
	mustWrite(t, paths.Config, "model='changed-model'\n")
	touch := fixtureNow.Add(3 * time.Hour)
	if err := os.Chtimes(paths.Config, touch, touch); err != nil {
		t.Fatal(err)
	}
	second := receiveSnapshot(t, monitor.Updates())
	if second.Config.Model != "changed-model" {
		t.Fatalf("refreshed config model = %q", second.Config.Model)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("monitor exit = %v", err)
	}
}

func TestStatisticsCacheWriteThrottleKeepsProgressInMemory(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "sessions", "usage.jsonl")
	cachePath := filepath.Join(root, "cache", "statistics.json")
	firstLine := `{"timestamp":"2026-08-09T01:00:00+08:00","type":"event_msg",` +
		`"payload":{"type":"token_count","info":{"total_token_usage":{` +
		`"input_tokens":100,"output_tokens":20,"total_tokens":120}}}}` + "\n"
	mustWrite(t, path, firstLine)
	now := fixtureNow
	service := NewService(Options{
		Paths: Paths{
			Sessions: filepath.Dir(path),
			Cache:    cachePath,
		},
		Location:          fixtureLocation,
		Now:               func() time.Time { return now },
		CacheSaveInterval: time.Hour,
	})
	if _, err := service.Snapshot(context.Background()); err != nil {
		t.Fatal(err)
	}
	marker := fixtureNow.Add(-time.Hour)
	if err := os.Chtimes(cachePath, marker, marker); err != nil {
		t.Fatal(err)
	}

	secondLine := `{"timestamp":"2026-08-09T02:00:00+08:00","type":"event_msg",` +
		`"payload":{"type":"token_count","info":{"total_token_usage":{` +
		`"input_tokens":140,"output_tokens":30,"total_tokens":170}}}}` + "\n"
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(secondLine); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	if err := os.Chtimes(path, now, now); err != nil {
		t.Fatal(err)
	}
	snapshot, err := service.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Statistics.TotalTokens != 170 {
		t.Fatalf("in-memory total = %d, want 170", snapshot.Statistics.TotalTokens)
	}
	info, err := os.Stat(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(marker) {
		t.Fatalf("throttled cache write changed mtime to %v", info.ModTime())
	}

	service.FlushStatisticsCache()
	cache := readStatisticsCache(
		cachePath,
		statisticsTimeZoneKey(fixtureLocation, now),
	)
	if len(cache.Files) != 1 || cache.Files[0].ParsedLength != int64(len(firstLine)+len(secondLine)) {
		t.Fatalf("flushed cache = %+v", cache.Files)
	}
}

func TestServiceReusesParsedSessionTailUntilFileStampChanges(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "sessions", "context.jsonl")
	first := `{"type":"turn_context","payload":{"model":"model-a"}}` + "\n"
	second := `{"type":"turn_context","payload":{"model":"model-b"}}` + "\n"
	if len(first) != len(second) {
		t.Fatal("test contexts differ in length")
	}
	mustWrite(t, path, first)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	service := fixtureService(Paths{
		Sessions: filepath.Dir(path),
		Cache:    filepath.Join(root, "cache.json"),
	})
	initial, err := service.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if initial.Runtime.Model != "model-a" {
		t.Fatalf("initial model = %q", initial.Runtime.Model)
	}

	mustWrite(t, path, second)
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	cached, err := service.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cached.Runtime.Model != "model-a" {
		t.Fatalf("cached model = %q, want model-a", cached.Runtime.Model)
	}

	changedTime := info.ModTime().Add(time.Second)
	if err := os.Chtimes(path, changedTime, changedTime); err != nil {
		t.Fatal(err)
	}
	changed, err := service.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if changed.Runtime.Model != "model-b" {
		t.Fatalf("changed model = %q, want model-b", changed.Runtime.Model)
	}
}

func TestIncrementalReadRegression(t *testing.T) {
	t.Run("append partial truncate and replace share one safe tail index", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, "sessions", "active.jsonl")
		record := func(model string, used string) string {
			return `{"timestamp":"2026-08-09T00:00:00Z","type":"turn_context",` +
				`"payload":{"model":"` + model + `","reasoning_effort":"high"},` +
				`"rate_limits":{"primary":{"used_percent":` + used +
				`,"window_minutes":300,"reset_at":1786251600}}}` + "\n"
		}
		tokenRecord := func(total string) string {
			return `{"timestamp":"2026-08-09T01:00:00Z","type":"event_msg",` +
				`"payload":{"type":"token_count","info":{"total_token_usage":{` +
				`"total_tokens":` + total + `}}}}` + "\n"
		}
		firstLine := record("model-a", "10")
		mustWrite(t, path, firstLine)
		metrics := &ReadMetrics{}
		service := NewService(Options{
			Paths: Paths{
				Sessions: filepath.Dir(path),
				Cache:    filepath.Join(root, "cache.json"),
			},
			Location: fixtureLocation,
			Now:      func() time.Time { return fixtureNow },
			Metrics:  metrics,
		})

		initial, err := service.Snapshot(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if initial.Runtime.Model != "model-a" || initial.RateLimit.Primary == nil ||
			initial.RateLimit.Primary.UsedPercent != 10 {
			t.Fatalf("initial shared tail result = %+v / %+v", initial.Runtime, initial.RateLimit)
		}
		if got := metrics.Counters().TailBytes; got != int64(len(firstLine)) {
			t.Fatalf("initial tail bytes = %d, want one shared read of %d", got, len(firstLine))
		}

		secondLine := record("model-b", "20")
		cut := len(secondLine) / 2
		before := metrics.Counters().TailBytes
		appendText(t, path, secondLine[:cut])
		partial, err := service.Snapshot(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if partial.Runtime.Model != "model-a" || partial.RateLimit.Primary.UsedPercent != 10 {
			t.Fatalf("partial line changed parsed values: %+v / %+v", partial.Runtime, partial.RateLimit)
		}
		if delta := metrics.Counters().TailBytes - before; delta != int64(cut) {
			t.Fatalf("partial append bytes = %d, want %d", delta, cut)
		}

		before = metrics.Counters().TailBytes
		appendText(t, path, secondLine[cut:])
		completed, err := service.Snapshot(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if completed.Runtime.Model != "model-b" || completed.RateLimit.Primary.UsedPercent != 20 {
			t.Fatalf("completed line result = %+v / %+v", completed.Runtime, completed.RateLimit)
		}
		if delta := metrics.Counters().TailBytes - before; delta != int64(len(secondLine)) {
			t.Fatalf("safe-offset completion bytes = %d, want %d", delta, len(secondLine))
		}

		thirdLine := record("model-c", "30")
		before = metrics.Counters().TailBytes
		appendText(t, path, thirdLine)
		appended, err := service.Snapshot(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if appended.Runtime.Model != "model-c" || appended.RateLimit.Primary.UsedPercent != 30 {
			t.Fatalf("incremental append result = %+v / %+v", appended.Runtime, appended.RateLimit)
		}
		if delta := metrics.Counters().TailBytes - before; delta != int64(len(thirdLine)) {
			t.Fatalf("terminated append bytes = %d, want only %d new bytes", delta, len(thirdLine))
		}

		truncatedLine := record("model-d", "40") + tokenRecord("400")
		before = metrics.Counters().TailBytes
		mustWrite(t, path, truncatedLine)
		truncated, err := service.Snapshot(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if truncated.Runtime.Model != "model-d" || truncated.RateLimit.Primary.UsedPercent != 40 ||
			truncated.Statistics.TotalTokens != 400 {
			t.Fatalf("truncated file result = %+v / %+v", truncated.Runtime, truncated.RateLimit)
		}
		if delta := metrics.Counters().TailBytes - before; delta != int64(len(truncatedLine)) {
			t.Fatalf("truncate fallback bytes = %d, want %d", delta, len(truncatedLine))
		}

		replacementLine := record("model-e", "50") + tokenRecord("500")
		if len(replacementLine) != len(truncatedLine) {
			t.Fatal("replacement fixture changed length")
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		replacementPath := filepath.Join(root, "replacement.jsonl")
		mustWrite(t, replacementPath, replacementLine)
		if err := os.Chtimes(replacementPath, info.ModTime(), info.ModTime()); err != nil {
			t.Fatal(err)
		}
		backupPath := filepath.Join(root, "previous.jsonl")
		if err := os.Rename(path, backupPath); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(replacementPath, path); err != nil {
			t.Fatal(err)
		}
		before = metrics.Counters().TailBytes
		replaced, err := service.Snapshot(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if replaced.Runtime.Model != "model-e" || replaced.RateLimit.Primary.UsedPercent != 50 ||
			replaced.Statistics.TotalTokens != 500 {
			t.Fatalf("same-stamp replacement result = %+v / %+v", replaced.Runtime, replaced.RateLimit)
		}
		if delta := metrics.Counters().TailBytes - before; delta != int64(len(replacementLine)) {
			t.Fatalf("replacement fallback bytes = %d, want %d", delta, len(replacementLine))
		}
	})

	t.Run("source masks limit log fallback and expose cancel publish counters", func(t *testing.T) {
		root := t.TempDir()
		sessionPath := filepath.Join(root, "sessions", "active.jsonl")
		logPath := filepath.Join(root, "logs.sqlite")
		configPath := filepath.Join(root, "config.toml")
		rateOnly := `{"timestamp":"2026-08-09T00:00:00Z","type":"event_msg",` +
			`"rate_limits":{"primary":{"used_percent":15,"window_minutes":300,` +
			`"reset_at":1786251600}}}` + "\n"
		logText := "turn{ model=log-model codex.turn.reasoning_effort=high } service_tier=priority\n" +
			`{"type":"codex.rate_limits","timestamp":"2026-08-09T00:00:00Z",` +
			`"rate_limits":{"primary":{"used_percent":75,"window_minutes":300,` +
			`"reset_at":1786251600}}}` + "\n"
		mustWrite(t, sessionPath, rateOnly)
		mustWrite(t, logPath, logText)
		metrics := &ReadMetrics{}
		paths := Paths{
			Config:   configPath,
			Sessions: filepath.Dir(sessionPath),
			Logs:     []string{logPath},
			Cache:    filepath.Join(root, "cache.json"),
		}
		service := NewService(Options{
			Paths:    paths,
			Location: fixtureLocation,
			Now:      func() time.Time { return fixtureNow },
			Metrics:  metrics,
		})
		initial, err := service.Snapshot(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if initial.Runtime.Model != "log-model" || initial.RateLimit.Primary == nil ||
			initial.RateLimit.Primary.UsedPercent != 15 {
			t.Fatalf("initial fallback split = %+v / %+v", initial.Runtime, initial.RateLimit)
		}

		logAppend := "turn{ model=log-next codex.turn.reasoning_effort=medium }\n"
		appendText(t, logPath, logAppend)
		before := metrics.Counters().TailBytes
		logChanged, err := service.Snapshot(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		logInfo, err := os.Stat(logPath)
		if err != nil {
			t.Fatal(err)
		}
		if logChanged.Runtime.Model != "log-next" ||
			logChanged.RateLimit.Primary.UsedPercent != 15 {
			t.Fatalf("runtime-only log fallback = %+v / %+v", logChanged.Runtime, logChanged.RateLimit)
		}
		if delta := metrics.Counters().TailBytes - before; delta != logInfo.Size() {
			t.Fatalf("runtime fallback bytes = %d, want one log read of %d", delta, logInfo.Size())
		}

		runtimeOnly := `{"type":"turn_context","payload":{"model":"session-model"}}` + "\n"
		mustWrite(t, sessionPath, runtimeOnly)
		before = metrics.Counters().TailBytes
		sessionChanged, err := service.Snapshot(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if sessionChanged.Runtime.Model != "session-model" ||
			sessionChanged.RateLimit.Primary == nil ||
			sessionChanged.RateLimit.Primary.UsedPercent != 75 {
			t.Fatalf("rate-only log fallback = %+v / %+v", sessionChanged.Runtime, sessionChanged.RateLimit)
		}
		wantTailBytes := int64(len(runtimeOnly)) + logInfo.Size()
		if delta := metrics.Counters().TailBytes - before; delta != wantTailBytes {
			t.Fatalf("rate fallback bytes = %d, want session plus one log read %d", delta, wantTailBytes)
		}

		mustWrite(t, configPath, "model='config-model'\n")
		before = metrics.Counters().TailBytes
		if _, err := service.Snapshot(context.Background()); err != nil {
			t.Fatal(err)
		}
		if delta := metrics.Counters().TailBytes - before; delta != 0 {
			t.Fatalf("config-only invalidation reread %d tail bytes", delta)
		}

		sessionBacked := `{"type":"turn_context","payload":{"model":"session-both"},` +
			`"rate_limits":{"primary":{"used_percent":25,"window_minutes":300,` +
			`"reset_at":1786251600}}}` + "\n"
		appendText(t, sessionPath, sessionBacked)
		if _, err := service.Snapshot(context.Background()); err != nil {
			t.Fatal(err)
		}
		newLogValues := "turn{ model=log-after-session codex.turn.reasoning_effort=high }\n" +
			`{"type":"codex.rate_limits","timestamp":"2026-08-10T00:00:00Z",` +
			`"rate_limits":{"primary":{"used_percent":5,"window_minutes":300,` +
			`"reset_at":1786338000}}}` + "\n"
		appendText(t, logPath, newLogValues)
		before = metrics.Counters().TailBytes
		if _, err := service.Snapshot(context.Background()); err != nil {
			t.Fatal(err)
		}
		if delta := metrics.Counters().TailBytes - before; delta != 0 {
			t.Fatalf("session-backed log change reread %d tail bytes", delta)
		}

		mustWrite(t, sessionPath, "")
		before = metrics.Counters().TailBytes
		fallbackAfterGap, err := service.Snapshot(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if fallbackAfterGap.Runtime.Model != "log-after-session" ||
			fallbackAfterGap.RateLimit.Primary == nil ||
			fallbackAfterGap.RateLimit.Primary.UsedPercent != 5 {
			t.Fatalf(
				"stale fallback reused after session gap: %+v / %+v",
				fallbackAfterGap.Runtime,
				fallbackAfterGap.RateLimit,
			)
		}
		logInfo, err = os.Stat(logPath)
		if err != nil {
			t.Fatal(err)
		}
		if delta := metrics.Counters().TailBytes - before; delta != 2*logInfo.Size() {
			t.Fatalf("dirty log fallback bytes = %d, want two fresh reads of %d", delta, logInfo.Size())
		}

		inventory, err := collectSourceInventory(context.Background(), paths, metrics)
		if err != nil {
			t.Fatal(err)
		}
		canceledContext, cancel := context.WithCancel(context.Background())
		cancel()
		beforeCounters := metrics.Counters()
		if _, err := service.snapshotFromInventory(canceledContext, inventory); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled snapshot error = %v", err)
		}
		afterCounters := metrics.Counters()
		if afterCounters.SnapshotsStarted != beforeCounters.SnapshotsStarted+1 ||
			afterCounters.SnapshotsCanceled != beforeCounters.SnapshotsCanceled+1 {
			t.Fatalf("canceled counters before=%+v after=%+v", beforeCounters, afterCounters)
		}
		if _, err := readTailContext(canceledContext, logPath, sessionTailBytes, metrics); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled chunked tail error = %v", err)
		}

		monitorMetrics := &ReadMetrics{}
		monitorService := NewService(Options{
			Paths:    paths,
			Location: fixtureLocation,
			Now:      func() time.Time { return fixtureNow },
			Metrics:  monitorMetrics,
		})
		monitor := NewMonitor(monitorService, MonitorOptions{PollInterval: time.Hour})
		monitorContext, stopMonitor := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- monitor.Run(monitorContext) }()
		_ = receiveSnapshot(t, monitor.Updates())
		published := monitorMetrics.Counters()
		if published.SnapshotsStarted != 1 || published.SnapshotsPublished != 1 ||
			published.WalkFiles != 1 {
			t.Fatalf("monitor counters after publish = %+v", published)
		}
		stopMonitor()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("monitor exit = %v", err)
		}
	})

	t.Run("canceled fallback reads and inventory failures remain retryable", func(t *testing.T) {
		t.Run("fallback cancellation does not commit empty log caches", func(t *testing.T) {
			root := t.TempDir()
			sessionPath := filepath.Join(root, "sessions", "active.jsonl")
			logPath := filepath.Join(root, "logs.sqlite")
			sessionValue := `{"type":"turn_context","payload":{"model":"session-main"},` +
				`"rate_limits":{"primary":{"used_percent":25,"window_minutes":300,` +
				`"reset_at":1786251600}}}` + "\n"
			logValue := strings.Repeat("x", contextReadChunk*2) + "\n" +
				"turn{ model=log-retry codex.turn.reasoning_effort=high }\n" +
				`{"type":"codex.rate_limits","timestamp":"2026-08-10T00:00:00Z",` +
				`"rate_limits":{"primary":{"used_percent":7,"window_minutes":300,` +
				`"reset_at":1786338000}}}` + "\n"
			mustWrite(t, sessionPath, sessionValue)
			mustWrite(t, logPath, logValue)
			metrics := &ReadMetrics{}
			paths := Paths{
				Sessions: filepath.Dir(sessionPath),
				Logs:     []string{logPath},
				Cache:    filepath.Join(root, "cache.json"),
			}
			service := NewService(Options{
				Paths:    paths,
				Location: fixtureLocation,
				Now:      func() time.Time { return fixtureNow },
				Metrics:  metrics,
			})
			initial, err := service.Snapshot(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if initial.Runtime.Model != "session-main" || initial.RateLimit.Primary == nil {
				t.Fatalf("initial session-backed snapshot = %+v", initial)
			}

			mustWrite(t, sessionPath, "")
			inventory, err := collectSourceInventory(context.Background(), paths, metrics)
			if err != nil {
				t.Fatal(err)
			}
			readContext, cancelRead := context.WithCancel(context.Background())
			canceledDuringRead := false
			metrics.tailReadHook = func(_ int) {
				if !canceledDuringRead {
					canceledDuringRead = true
					cancelRead()
				}
			}
			_, err = service.snapshotFromInventory(readContext, inventory)
			metrics.tailReadHook = nil
			cancelRead()
			if !canceledDuringRead || !errors.Is(err, context.Canceled) {
				t.Fatalf("fallback cancellation = triggered:%v error:%v", canceledDuringRead, err)
			}
			if service.logRuntimeRead || service.logRateRead {
				t.Fatalf("canceled fallback committed log caches: runtime=%v rate=%v", service.logRuntimeRead, service.logRateRead)
			}

			retried, err := service.snapshotFromInventory(context.Background(), inventory)
			if err != nil {
				t.Fatal(err)
			}
			if retried.Runtime.Model != "log-retry" || retried.RateLimit.Primary == nil ||
				retried.RateLimit.Primary.UsedPercent != 7 {
				t.Fatalf("same-inventory fallback retry = %+v / %+v", retried.Runtime, retried.RateLimit)
			}
		})

		t.Run("failed inventory preserves the last successful cache", func(t *testing.T) {
			root := t.TempDir()
			configPath := filepath.Join(root, "config.toml")
			mustWrite(t, configPath, "model='model-a'\n")
			service := fixtureService(Paths{
				Config: configPath,
				Cache:  filepath.Join(root, "cache.json"),
			})
			initial, err := service.Snapshot(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if initial.Config.Model != "model-a" {
				t.Fatalf("initial config = %+v", initial.Config)
			}

			service.paths.Config = string([]byte{0})
			if _, err := service.Snapshot(context.Background()); err == nil {
				t.Fatal("invalid-path inventory unexpectedly succeeded")
			}
			if !service.hasInventory || service.config.Model != "model-a" {
				t.Fatalf("failed inventory cleared cached state: %+v", service.config)
			}

			service.paths.Config = configPath
			mustWrite(t, configPath, "model='model-b'\n")
			touch := fixtureNow.Add(5 * time.Hour)
			if err := os.Chtimes(configPath, touch, touch); err != nil {
				t.Fatal(err)
			}
			retried, err := service.Snapshot(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if retried.Config.Model != "model-b" {
				t.Fatalf("inventory retry config = %+v", retried.Config)
			}
		})

		t.Run("monitor retries an initial inventory error", func(t *testing.T) {
			root := t.TempDir()
			configPath := filepath.Join(root, "config.toml")
			mustWrite(t, configPath, "model='monitor-retry'\n")
			metrics := &ReadMetrics{}
			var inventoryAttempts atomic.Int64
			metrics.inventoryHook = func() error {
				if inventoryAttempts.Add(1) == 1 {
					return errors.New("injected inventory failure")
				}
				return nil
			}
			service := NewService(Options{
				Paths: Paths{
					Config: configPath,
					Cache:  filepath.Join(root, "cache.json"),
				},
				Location: fixtureLocation,
				Now:      func() time.Time { return fixtureNow },
				Metrics:  metrics,
			})
			monitor := NewMonitor(service, MonitorOptions{PollInterval: 10 * time.Millisecond})
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- monitor.Run(ctx) }()
			snapshot := receiveSnapshot(t, monitor.Updates())
			if snapshot.Config.Model != "monitor-retry" || inventoryAttempts.Load() < 2 {
				t.Fatalf(
					"initial inventory retry = model:%q attempts:%d",
					snapshot.Config.Model,
					inventoryAttempts.Load(),
				)
			}
			counters := metrics.Counters()
			if counters.SnapshotsStarted != 1 || counters.SnapshotsPublished != 1 {
				t.Fatalf("initial inventory retry counters = %+v", counters)
			}
			cancel()
			if err := <-done; !errors.Is(err, context.Canceled) {
				t.Fatalf("monitor exit = %v", err)
			}
		})

		t.Run("monitor retries a same-inventory fallback error", func(t *testing.T) {
			root := t.TempDir()
			sessionPath := filepath.Join(root, "sessions", "active.jsonl")
			logPath := filepath.Join(root, "logs.sqlite")
			sessionValue := `{"type":"turn_context","payload":{"model":"session-main"},` +
				`"rate_limits":{"primary":{"used_percent":25,"window_minutes":300,` +
				`"reset_at":1786251600}}}` + "\n"
			logValue := strings.Repeat("x", contextReadChunk*2) + "\n" +
				"turn{ model=monitor-log-retry codex.turn.reasoning_effort=high }\n" +
				`{"type":"codex.rate_limits","timestamp":"2026-08-10T00:00:00Z",` +
				`"rate_limits":{"primary":{"used_percent":9,"window_minutes":300,` +
				`"reset_at":1786338000}}}` + "\n"
			mustWrite(t, sessionPath, sessionValue)
			mustWrite(t, logPath, logValue)
			failNextRead := make(chan struct{}, 1)
			failureObserved := make(chan struct{}, 1)
			injectedError := errors.New("injected fallback read failure")
			metrics := &ReadMetrics{}
			metrics.tailReadErrorHook = func(_ int) error {
				select {
				case <-failNextRead:
					failureObserved <- struct{}{}
					return injectedError
				default:
					return nil
				}
			}
			service := NewService(Options{
				Paths: Paths{
					Sessions: filepath.Dir(sessionPath),
					Logs:     []string{logPath},
					Cache:    filepath.Join(root, "cache.json"),
				},
				Location: fixtureLocation,
				Now:      func() time.Time { return fixtureNow },
				Metrics:  metrics,
			})
			monitor := NewMonitor(service, MonitorOptions{PollInterval: 10 * time.Millisecond})
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- monitor.Run(ctx) }()
			initial := receiveSnapshot(t, monitor.Updates())
			if initial.Runtime.Model != "session-main" {
				t.Fatalf("initial monitor runtime = %+v", initial.Runtime)
			}

			failNextRead <- struct{}{}
			mustWrite(t, sessionPath, "")
			select {
			case <-failureObserved:
			case <-time.After(5 * time.Second):
				t.Fatal("monitor did not attempt the injected fallback read")
			}
			retried := receiveSnapshot(t, monitor.Updates())
			if retried.Runtime.Model != "monitor-log-retry" ||
				retried.RateLimit.Primary == nil ||
				retried.RateLimit.Primary.UsedPercent != 9 {
				t.Fatalf("same-inventory monitor retry = %+v / %+v", retried.Runtime, retried.RateLimit)
			}
			counters := metrics.Counters()
			if counters.SnapshotsStarted != 3 || counters.SnapshotsPublished != 2 ||
				counters.SnapshotsCanceled != 0 {
				t.Fatalf("same-inventory retry counters = %+v", counters)
			}
			cancel()
			if err := <-done; !errors.Is(err, context.Canceled) {
				t.Fatalf("monitor exit = %v", err)
			}
		})

		t.Run("monitor retries a same-stamp initial session read error", func(t *testing.T) {
			root := t.TempDir()
			sessionPath := filepath.Join(root, "sessions", "active.jsonl")
			sessionValue := `{"type":"turn_context","payload":{"model":"session-retry"},` +
				`"rate_limits":{"primary":{"used_percent":17,"window_minutes":300,` +
				`"reset_at":1786251600}}}` + "\n"
			mustWrite(t, sessionPath, sessionValue)
			injectedError := errors.New("injected session read failure")
			var attempts atomic.Int64
			metrics := &ReadMetrics{}
			metrics.tailReadErrorHook = func(_ int) error {
				if attempts.Add(1) == 1 {
					return injectedError
				}
				return nil
			}
			service := NewService(Options{
				Paths: Paths{
					Sessions: filepath.Dir(sessionPath),
					Cache:    filepath.Join(root, "cache.json"),
				},
				Location: fixtureLocation,
				Now:      func() time.Time { return fixtureNow },
				Metrics:  metrics,
			})
			monitor := NewMonitor(service, MonitorOptions{PollInterval: 10 * time.Millisecond})
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- monitor.Run(ctx) }()
			snapshot := receiveSnapshot(t, monitor.Updates())
			if snapshot.Runtime.Model != "session-retry" || snapshot.RateLimit.Primary == nil ||
				snapshot.RateLimit.Primary.UsedPercent != 17 || attempts.Load() < 2 {
				t.Fatalf("same-stamp session retry = %+v / %+v", snapshot.Runtime, snapshot.RateLimit)
			}
			counters := metrics.Counters()
			if counters.SnapshotsStarted != 2 || counters.SnapshotsPublished != 1 {
				t.Fatalf("same-stamp session retry counters = %+v", counters)
			}
			cancel()
			if err := <-done; !errors.Is(err, context.Canceled) {
				t.Fatalf("monitor exit = %v", err)
			}
		})

		t.Run("statistics I/O failure preserves old cache and retries the same stamp", func(t *testing.T) {
			root := t.TempDir()
			sessionPath := filepath.Join(root, "sessions", "usage.jsonl")
			usageLine := func(total int) string {
				return `{"timestamp":"2026-08-09T01:00:00Z","type":"event_msg",` +
					`"payload":{"type":"token_count","info":{"total_token_usage":{` +
					`"total_tokens":` + strconv.Itoa(total) + `}}}}` + "\n"
			}
			mustWrite(t, sessionPath, usageLine(100))
			var failStatistics atomic.Bool
			failureObserved := make(chan struct{}, 1)
			metrics := &ReadMetrics{}
			metrics.sourceReadErrorHook = func(kind sourceReadKind) error {
				if kind == sourceReadStatistics && failStatistics.CompareAndSwap(true, false) {
					failureObserved <- struct{}{}
					return errors.New("injected statistics read failure")
				}
				return nil
			}
			service := NewService(Options{
				Paths: Paths{
					Sessions: filepath.Dir(sessionPath),
					Cache:    filepath.Join(root, "cache.json"),
				},
				Location: fixtureLocation,
				Now:      func() time.Time { return fixtureNow },
				Metrics:  metrics,
			})
			monitor := NewMonitor(service, MonitorOptions{PollInterval: time.Hour})
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- monitor.Run(ctx) }()
			initial := receiveSnapshot(t, monitor.Updates())
			if initial.Statistics.TotalTokens != 100 {
				t.Fatalf("initial statistics total = %d", initial.Statistics.TotalTokens)
			}

			failStatistics.Store(true)
			appendText(t, sessionPath, usageLine(200))
			monitor.Refresh()
			select {
			case <-failureObserved:
			case <-time.After(5 * time.Second):
				t.Fatal("monitor did not surface the injected statistics read failure")
			}
			service.snapshotMu.Lock()
			cachedTotal := service.statisticsSnapshot(fixtureNow).TotalTokens
			service.snapshotMu.Unlock()
			if cachedTotal != 100 {
				t.Fatalf("failed statistics read committed total %d, want 100", cachedTotal)
			}

			monitor.Refresh()
			retried := receiveSnapshot(t, monitor.Updates())
			if retried.Statistics.TotalTokens != 200 {
				t.Fatalf("same-stamp statistics retry total = %d", retried.Statistics.TotalTokens)
			}
			counters := metrics.Counters()
			if counters.SnapshotsStarted != 3 || counters.SnapshotsPublished != 2 {
				t.Fatalf("statistics retry counters = %+v", counters)
			}
			cancel()
			if err := <-done; !errors.Is(err, context.Canceled) {
				t.Fatalf("monitor exit = %v", err)
			}
		})

		t.Run("config and auth I/O failures retry without publishing fallbacks", func(t *testing.T) {
			root := t.TempDir()
			configPath := filepath.Join(root, "config.toml")
			authPath := filepath.Join(root, "auth.json")
			mustWrite(t, configPath, "model='model-a'\n")
			mustWrite(t, authPath, `{"auth_mode":"chatgpt"}`)
			var armed atomic.Bool
			var configFailed atomic.Bool
			var authFailed atomic.Bool
			failures := make(chan sourceReadKind, 2)
			metrics := &ReadMetrics{}
			metrics.sourceReadErrorHook = func(kind sourceReadKind) error {
				if !armed.Load() {
					return nil
				}
				switch {
				case kind == sourceReadConfig && configFailed.CompareAndSwap(false, true):
					failures <- kind
					return errors.New("injected config read failure")
				case kind == sourceReadAuth && authFailed.CompareAndSwap(false, true):
					failures <- kind
					return errors.New("injected auth read failure")
				default:
					return nil
				}
			}
			service := NewService(Options{
				Paths: Paths{
					Config: configPath,
					Auth:   authPath,
					Cache:  filepath.Join(root, "cache.json"),
				},
				Location: fixtureLocation,
				Now:      func() time.Time { return fixtureNow },
				Metrics:  metrics,
			})
			monitor := NewMonitor(service, MonitorOptions{PollInterval: 10 * time.Millisecond})
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- monitor.Run(ctx) }()
			initial := receiveSnapshot(t, monitor.Updates())
			if initial.Config.Model != "model-a" || initial.Account.DisplayText != "Codex: ChatGPT" {
				t.Fatalf("initial config/account = %+v / %+v", initial.Config, initial.Account)
			}

			armed.Store(true)
			mustWrite(t, configPath, "model='model-b'\n")
			mustWrite(t, authPath, `{"auth_mode":"api_key"}`)
			for range 2 {
				select {
				case <-failures:
				case <-time.After(5 * time.Second):
					t.Fatal("monitor did not attempt both injected source reads")
				}
			}
			retried := receiveSnapshot(t, monitor.Updates())
			if retried.Config.Model != "model-b" || retried.Account.DisplayText != "Codex: api_key" {
				t.Fatalf("retried config/account = %+v / %+v", retried.Config, retried.Account)
			}
			counters := metrics.Counters()
			if counters.SnapshotsStarted != 4 || counters.SnapshotsPublished != 2 {
				t.Fatalf("config/auth retry counters = %+v", counters)
			}
			cancel()
			if err := <-done; !errors.Is(err, context.Canceled) {
				t.Fatalf("monitor exit = %v", err)
			}
		})
	})

	t.Run("read metrics prove bounded steady-state amplification", func(t *testing.T) {
		t.Run("normal fixture monitor stages", func(t *testing.T) {
			paths := materializeFixture(t, "normal")
			files, err := collectSessionInventory(context.Background(), paths.Sessions, nil)
			if err != nil {
				t.Fatal(err)
			}
			var coldTailBytes int64
			for _, file := range limitSessionFiles(files, 16) {
				coldTailBytes += min(file.size, sessionTailBytes)
			}

			metrics := &ReadMetrics{}
			service := NewService(Options{
				Paths:             paths,
				Location:          fixtureLocation,
				Now:               func() time.Time { return fixtureNow },
				CacheSaveInterval: time.Hour,
				Metrics:           metrics,
			})
			monitor := NewMonitor(service, MonitorOptions{PollInterval: time.Hour})
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- monitor.Run(ctx) }()
			defer func() {
				cancel()
				if err := <-done; !errors.Is(err, context.Canceled) {
					t.Errorf("monitor exit = %v", err)
				}
			}()

			_ = receiveSnapshot(t, monitor.Updates())
			cold := verifyPublishedReadStage(t, readStageEvidence{
				Scope:         "fixture",
				Stage:         "cold",
				Before:        ReadCounters{},
				After:         metrics.Counters(),
				WantTailBytes: coldTailBytes,
				WantWalkFiles: int64(len(files)),
			})

			primaryPath := filepath.Join(
				paths.Sessions,
				"2026",
				"08",
				"09",
				"primary.jsonl",
			)
			firstAppend := `{"timestamp":"2026-08-09T04:00:00Z","type":"turn_context",` +
				`"payload":{"model":"evidence-a","reasoning_effort":"high"},` +
				`"rate_limits":{"primary":{"used_percent":31,"window_minutes":300,` +
				`"reset_at":1786251600}}}` + "\n"
			appendText(t, primaryPath, firstAppend)
			monitor.Refresh()
			_ = receiveSnapshot(t, monitor.Updates())
			first := verifyPublishedReadStage(t, readStageEvidence{
				Scope:         "fixture",
				Stage:         "append-1",
				Before:        cold,
				After:         metrics.Counters(),
				WantTailBytes: int64(len(firstAppend)),
				WantWalkFiles: int64(len(files)),
			})

			secondAppend := `{"type":"turn_context","payload":{"model":"evidence-b"}}` + "\n"
			appendText(t, primaryPath, secondAppend)
			monitor.Refresh()
			_ = receiveSnapshot(t, monitor.Updates())
			second := verifyPublishedReadStage(t, readStageEvidence{
				Scope:         "fixture",
				Stage:         "append-2",
				Before:        first,
				After:         metrics.Counters(),
				WantTailBytes: int64(len(secondAppend)),
				WantWalkFiles: int64(len(files)),
			})

			logAppend := "\nmetrics-only-log-change"
			appendText(t, paths.Logs[len(paths.Logs)-1], logAppend)
			monitor.Refresh()
			_ = receiveSnapshot(t, monitor.Updates())
			verifyPublishedReadStage(t, readStageEvidence{
				Scope:         "fixture",
				Stage:         "logs-change-session-backed",
				Before:        second,
				After:         metrics.Counters(),
				WantTailBytes: 0,
				WantWalkFiles: int64(len(files)),
			})
		})

		t.Run("350 files 16 active 10 second replay", func(t *testing.T) {
			const (
				totalFiles  = 350
				activeFiles = 16
				rounds      = 7
			)
			root := t.TempDir()
			sessionsRoot := filepath.Join(root, "sessions")
			if err := os.MkdirAll(sessionsRoot, 0o700); err != nil {
				t.Fatal(err)
			}
			for index := range totalFiles - activeFiles {
				path := filepath.Join(
					sessionsRoot,
					"history-"+strconv.Itoa(index)+".jsonl",
				)
				mustWrite(t, path, "{}\n")
				stamp := fixtureNow.AddDate(0, 0, -30).Add(time.Duration(index) * time.Second)
				if err := os.Chtimes(path, stamp, stamp); err != nil {
					t.Fatal(err)
				}
			}

			activePaths := make([]string, 0, activeFiles)
			initialRecord := func(index int) string {
				return `{"timestamp":"2026-08-09T00:00:00Z","type":"turn_context",` +
					`"payload":{"model":"active-` + strconv.Itoa(index) + `"},` +
					`"rate_limits":{"primary":{"used_percent":10,"window_minutes":300,` +
					`"reset_at":1786251600}}}` + "\n"
			}
			for index := range activeFiles {
				path := filepath.Join(
					sessionsRoot,
					"active-"+strconv.Itoa(index)+".jsonl",
				)
				createSparseSession(t, path, sessionTailBytes, initialRecord(index))
				stamp := fixtureNow.Add(time.Duration(index+1) * time.Minute)
				if err := os.Chtimes(path, stamp, stamp); err != nil {
					t.Fatal(err)
				}
				activePaths = append(activePaths, path)
			}

			metrics := &ReadMetrics{}
			paths := Paths{
				Sessions: sessionsRoot,
				Cache:    filepath.Join(root, "cache.json"),
			}
			service := NewService(Options{
				Paths:             paths,
				Location:          fixtureLocation,
				Now:               func() time.Time { return fixtureNow },
				CacheSaveInterval: time.Hour,
				Metrics:           metrics,
			})
			monitor := NewMonitor(service, MonitorOptions{PollInterval: time.Hour})
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- monitor.Run(ctx) }()
			defer func() {
				cancel()
				if err := <-done; !errors.Is(err, context.Canceled) {
					t.Errorf("monitor exit = %v", err)
				}
			}()

			_ = receiveSnapshot(t, monitor.Updates())
			cold := verifyPublishedReadStage(t, readStageEvidence{
				Scope:         "synthetic",
				Stage:         "cold",
				Before:        ReadCounters{},
				After:         metrics.Counters(),
				WantTailBytes: int64(activeFiles) * sessionTailBytes,
				WantWalkFiles: totalFiles,
			})

			padding := strings.Repeat("x", 700)
			var appendedBytes int64
			for round := range rounds {
				for index, path := range activePaths {
					line := `{"timestamp":"2026-08-09T00:00:00Z","type":"turn_context",` +
						`"payload":{"model":"replay-` + strconv.Itoa(round) + `-` +
						strconv.Itoa(index) + `","padding":"` + padding + `"},` +
						`"rate_limits":{"primary":{"used_percent":20,"window_minutes":300,` +
						`"reset_at":1786251600}}}` + "\n"
					appendText(t, path, line)
					appendedBytes += int64(len(line))
				}
				monitor.Refresh()
				_ = receiveSnapshot(t, monitor.Updates())
			}

			steady := subtractReadCounters(metrics.Counters(), cold)
			legacyTailEstimate := int64(rounds*(activeFiles+1)) * sessionTailBytes
			reduction := 100 * (1 - float64(steady.TailBytes)/float64(legacyTailEstimate))
			t.Logf(
				"READ_METRICS scope=synthetic stage=10s-replay tail_bytes=%d walk_files=%d started=%d published=%d canceled=%d appended_bytes=%d legacy_fixed_tail_estimate=%d reduction_percent=%.2f",
				steady.TailBytes,
				steady.WalkFiles,
				steady.SnapshotsStarted,
				steady.SnapshotsPublished,
				steady.SnapshotsCanceled,
				appendedBytes,
				legacyTailEstimate,
				reduction,
			)
			if appendedBytes < 96*1024 || appendedBytes > 112*1024 {
				t.Fatalf("synthetic replay appended %d bytes, want about 100 KiB", appendedBytes)
			}
			if steady.TailBytes != appendedBytes {
				t.Fatalf("steady tail bytes = %d, want appended bytes %d", steady.TailBytes, appendedBytes)
			}
			if steady.TailBytes > 8*1024*1024 {
				t.Fatalf("steady tail bytes = %d, exceeds 8 MiB/10s gate", steady.TailBytes)
			}
			if steady.WalkFiles != int64(totalFiles*rounds) ||
				steady.SnapshotsStarted != rounds ||
				steady.SnapshotsPublished != rounds ||
				steady.SnapshotsCanceled != 0 {
				t.Fatalf("unexpected steady counters: %+v", steady)
			}
			if steady.TailBytes*100 >= legacyTailEstimate {
				t.Fatalf(
					"steady tail bytes %d did not improve legacy estimate %d by at least 99%%",
					steady.TailBytes,
					legacyTailEstimate,
				)
			}
		})
	})
}

type readStageEvidence struct {
	Scope         string
	Stage         string
	Before        ReadCounters
	After         ReadCounters
	WantTailBytes int64
	WantWalkFiles int64
}

func verifyPublishedReadStage(t *testing.T, evidence readStageEvidence) ReadCounters {
	t.Helper()
	delta := subtractReadCounters(evidence.After, evidence.Before)
	t.Logf(
		"READ_METRICS scope=%s stage=%s tail_bytes=%d walk_files=%d started=%d published=%d canceled=%d",
		evidence.Scope,
		evidence.Stage,
		delta.TailBytes,
		delta.WalkFiles,
		delta.SnapshotsStarted,
		delta.SnapshotsPublished,
		delta.SnapshotsCanceled,
	)
	if delta.TailBytes != evidence.WantTailBytes ||
		delta.WalkFiles != evidence.WantWalkFiles ||
		delta.SnapshotsStarted != 1 ||
		delta.SnapshotsPublished != 1 ||
		delta.SnapshotsCanceled != 0 {
		t.Fatalf("unexpected read counters: %+v", delta)
	}
	return evidence.After
}

func subtractReadCounters(after ReadCounters, before ReadCounters) ReadCounters {
	return ReadCounters{
		TailBytes:          after.TailBytes - before.TailBytes,
		WalkFiles:          after.WalkFiles - before.WalkFiles,
		SnapshotsStarted:   after.SnapshotsStarted - before.SnapshotsStarted,
		SnapshotsPublished: after.SnapshotsPublished - before.SnapshotsPublished,
		SnapshotsCanceled:  after.SnapshotsCanceled - before.SnapshotsCanceled,
	}
}

func materializeFixture(t *testing.T, name string) Paths {
	t.Helper()
	source := filepath.Join("testdata", "fixtures", name)
	root := t.TempDir()
	err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(root, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o600)
	})
	if err != nil {
		t.Fatal(err)
	}
	primary := filepath.Join(root, "sessions", "2026", "08", "09", "primary.jsonl")
	secondary := filepath.Join(root, "sessions", "2026", "08", "01", "secondary.jsonl")
	if _, err := os.Stat(primary); err == nil {
		if err := os.Chtimes(primary, fixtureNow, fixtureNow); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(secondary); err == nil {
		older := fixtureNow.Add(-time.Hour)
		if err := os.Chtimes(secondary, older, older); err != nil {
			t.Fatal(err)
		}
	}
	return Paths{
		Config:   filepath.Join(root, "config.toml"),
		Auth:     filepath.Join(root, "auth.json"),
		Sessions: filepath.Join(root, "sessions"),
		Logs: []string{
			filepath.Join(root, "logs_2.sqlite"),
			filepath.Join(root, "logs_2.sqlite-wal"),
		},
		Cache: filepath.Join(root, "cache", "usage-statistics-cache.json"),
	}
}

func fixtureService(paths Paths) *Service {
	return NewService(Options{
		Paths:    paths,
		Location: fixtureLocation,
		Now:      func() time.Time { return fixtureNow },
	})
}

func mustWrite(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func appendText(t *testing.T, path string, content string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(content); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func createSparseSession(t *testing.T, path string, prefixBytes int64, suffix string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(prefixBytes); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if _, err := file.WriteAt([]byte("\n"+suffix), prefixBytes); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func writeOneLine(t *testing.T, root string, name string, line string) string {
	t.Helper()
	path := filepath.Join(root, name)
	mustWrite(t, path, line+"\n")
	return path
}

func receiveSnapshot(t *testing.T, updates <-chan AppSnapshot) AppSnapshot {
	t.Helper()
	select {
	case snapshot, ok := <-updates:
		if !ok {
			t.Fatal("monitor updates closed")
		}
		return snapshot
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for monitor update")
		return AppSnapshot{}
	}
}
