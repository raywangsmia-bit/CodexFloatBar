package codexdata

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
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
