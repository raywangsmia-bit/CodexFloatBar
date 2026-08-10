package codexdata

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

type csharpContractDocument struct {
	Schema int `json:"schema"`
	Oracle struct {
		Generator               string            `json:"generator"`
		LinkedSourceFingerprint string            `json:"linkedSourceFingerprint"`
		OracleSourceFingerprint string            `json:"oracleSourceFingerprint"`
		FixtureFingerprint      string            `json:"fixtureFingerprint"`
		FixedClock              string            `json:"fixedClock"`
		LocalTimeZoneOffset     string            `json:"localTimeZoneOffset"`
		Coverage                map[string]string `json:"coverage"`
	} `json:"oracle"`
	Snapshot json.RawMessage `json:"snapshot"`
}

func TestAppSnapshotMatchesCanonicalCSharpOracle(t *testing.T) {
	fixtureRoot := filepath.Join("testdata", "fixtures", "normal")
	goldenPath := filepath.Join("testdata", "golden", "csharp-oracle.json")

	document := readCSharpContractDocument(t, goldenPath)
	if document.Schema != 1 {
		t.Fatalf("C# contract schema = %d, want 1", document.Schema)
	}
	if document.Oracle.Generator != "net8-linked-wpf-services" {
		t.Fatalf("unexpected C# oracle generator %q", document.Oracle.Generator)
	}
	if document.Oracle.LinkedSourceFingerprint == "" ||
		document.Oracle.OracleSourceFingerprint == "" {
		t.Fatal("C# oracle provenance fingerprints are missing")
	}
	if document.Oracle.FixedClock != "2026-08-09T12:00:00+08:00" ||
		document.Oracle.LocalTimeZoneOffset != "+08:00" {
		t.Fatalf(
			"unexpected C# oracle clock contract: clock=%q offset=%q",
			document.Oracle.FixedClock,
			document.Oracle.LocalTimeZoneOffset,
		)
	}
	for _, source := range []string{
		"account",
		"config",
		"rateLimit",
		"session",
		"statistics",
	} {
		if strings.TrimSpace(document.Oracle.Coverage[source]) == "" {
			t.Fatalf("C# oracle does not describe %s coverage", source)
		}
	}

	fixturePaths := contractTreePaths(t, fixtureRoot)
	assertContractFingerprint(
		t,
		"normal fixture",
		document.Oracle.FixtureFingerprint,
		fingerprintContractFiles(t, fixtureRoot, fixturePaths),
	)

	paths := materializeFixture(t, "normal")
	snapshot, err := fixtureService(paths).Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	actual, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	actual = canonicalContractJSON(t, actual)
	expected := canonicalContractJSON(t, document.Snapshot)
	if !bytes.Equal(actual, expected) {
		t.Fatalf(
			"Go snapshot differs from canonical C# oracle\nactual:\n%s\nexpected:\n%s",
			actual,
			expected,
		)
	}
}

func readCSharpContractDocument(t *testing.T, path string) csharpContractDocument {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document csharpContractDocument
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	return document
}

func assertContractFingerprint(t *testing.T, name string, expected string, actual string) {
	t.Helper()
	if expected != actual {
		t.Fatalf(
			"%s fingerprint is stale: golden=%s current=%s; regenerate the C# oracle",
			name,
			expected,
			actual,
		)
	}
}

func contractTreePaths(t *testing.T, root string) []string {
	t.Helper()
	paths := []string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		paths = append(paths, filepath.ToSlash(relative))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(paths)
	return paths
}

func fingerprintContractFiles(t *testing.T, root string, paths []string) string {
	t.Helper()
	sorted := slices.Clone(paths)
	slices.Sort(sorted)
	var aggregate strings.Builder
	for _, relative := range sorted {
		normalized := filepath.ToSlash(relative)
		contents, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(normalized)))
		if err != nil {
			t.Fatal(err)
		}
		contentHash := sha256.Sum256(contents)
		aggregate.WriteString(normalized)
		aggregate.WriteByte('\n')
		aggregate.WriteString(hex.EncodeToString(contentHash[:]))
		aggregate.WriteByte('\n')
	}
	result := sha256.Sum256([]byte(aggregate.String()))
	return hex.EncodeToString(result[:])
}

func canonicalContractJSON(t *testing.T, data []byte) []byte {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		t.Fatal(err)
	}
	canonical, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}
