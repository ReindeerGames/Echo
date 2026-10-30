//go:build linux

package processproxy

import "testing"

func TestParseProcessStatusReturnsOnlyBoundedCounters(t *testing.T) {
	raw := "Name:\tworker\nState:\tS (sleeping)\nUid:\t1000\t1000\t1000\t1000\nVmRSS:\t1536 kB\nThreads:\t7\nSecret:\ttoken=must-not-leak\n"
	state, threads, rssBytes, err := parseProcessStatus(raw)
	if err != nil || state != 'S' || threads != 7 || rssBytes != 1536*1024 {
		t.Fatalf("unexpected process status state=%q threads=%d rss=%d err=%v", state, threads, rssBytes, err)
	}
}

func TestLinuxScannerReturnsEmptySnapshotForUnknownExactName(t *testing.T) {
	snapshot, err := (linuxScanner{}).Snapshot(t.Context(), "echo-no-such")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot != (Snapshot{}) {
		t.Fatalf("unexpected exact-name match: %#v", snapshot)
	}
}
