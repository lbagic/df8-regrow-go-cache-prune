package scanner

import (
	"testing"
	"time"

	"github.com/lbagic/regrow/internal/engine"
)

func TestParseSimDevices(t *testing.T) {
	data := []byte(`{
	  "devices": {
	    "com.apple.CoreSimulator.SimRuntime.iOS-17-0": [
	      {"name": "iPhone 15", "udid": "AAA-111", "isAvailable": true,
	       "dataPath": "/x", "dataPathSize": 4000000000,
	       "lastBootedAt": "2026-05-01T10:00:00Z"},
	      {"name": "iPhone 14", "udid": "BBB-222", "isAvailable": false,
	       "dataPathSize": 900000000}
	    ]
	  }
	}`)
	available, unavailable, err := parseSimDevices(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(available) != 1 || len(unavailable) != 1 {
		t.Fatalf("got %d available, %d unavailable", len(available), len(unavailable))
	}
	a := available[0]
	if a.Label != "iPhone 15 (iOS 17.0)" || a.Arg != "AAA-111" || a.Bytes != 4_000_000_000 {
		t.Errorf("available item wrong: %+v", a)
	}
	if a.LastUsed != time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC) {
		t.Errorf("lastBootedAt not parsed: %v", a.LastUsed)
	}
	if unavailable[0].Arg != "BBB-222" {
		t.Errorf("unavailable item wrong: %+v", unavailable[0])
	}
}

func TestParseSimRuntimes(t *testing.T) {
	data := []byte(`{
	  "11111111-2222": {
	    "identifier": "11111111-2222",
	    "runtimeIdentifier": "com.apple.CoreSimulator.SimRuntime.iOS-17-0",
	    "build": "21A328", "sizeBytes": 7000000000, "deletable": true,
	    "lastUsedAt": "2026-01-15T08:00:00Z"
	  },
	  "33333333-4444": {
	    "identifier": "33333333-4444",
	    "runtimeIdentifier": "com.apple.CoreSimulator.SimRuntime.iOS-18-1",
	    "build": "22B81", "sizeBytes": 8000000000, "deletable": false
	  }
	}`)
	items, err := parseSimRuntimes(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("non-deletable runtime must be excluded; got %+v", items)
	}
	it := items[0]
	if it.Label != "iOS 17.0 (21A328)" || it.Arg != "11111111-2222" || it.Bytes != 7_000_000_000 {
		t.Errorf("runtime item wrong: %+v", it)
	}
}

func TestParseTMSnapshots(t *testing.T) {
	out := "Snapshots for disk /:\n" +
		"com.apple.TimeMachine.2026-07-12-090000.local\n" +
		"com.apple.TimeMachine.2026-07-13-090000.local\n"
	items := parseTMSnapshots(out)
	if len(items) != 2 {
		t.Fatalf("got %d items", len(items))
	}
	if items[0].Label != "com.apple.TimeMachine.2026-07-12-090000.local" {
		t.Errorf("label wrong: %q", items[0].Label)
	}
	// The embedded date is the delete handle and the recency signal.
	if items[0].Arg != "2026-07-12-090000" {
		t.Errorf("arg wrong: %q", items[0].Arg)
	}
	if want := time.Date(2026, 7, 12, 9, 0, 0, 0, time.Local); !items[0].LastUsed.Equal(want) {
		t.Errorf("LastUsed = %v, want %v", items[0].LastUsed, want)
	}
}

func TestParseTMSnapshotsUnrecognizedNameHasNoArg(t *testing.T) {
	items := parseTMSnapshots("com.apple.TimeMachine.weird-name.local\n")
	if len(items) != 1 || items[0].Arg != "" {
		t.Fatalf("unparseable snapshot must carry no delete handle, got %+v", items)
	}
}

// Every tool_query named by the shipped catalog must resolve in the
// default registry — a typo here is invisible until a user scans.
func TestEmbeddedCatalogToolQueriesRegistered(t *testing.T) {
	catalog, err := engine.LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	queries := DefaultQueries()
	for _, r := range catalog {
		if r.ToolQuery == "" {
			continue
		}
		if _, ok := queries[r.ToolQuery]; !ok {
			t.Errorf("rule %s references unknown tool_query %q", r.ID, r.ToolQuery)
		}
	}
}
