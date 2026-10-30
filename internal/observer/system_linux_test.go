//go:build linux

package observer

import "testing"

func TestLinuxCounterParsers(t *testing.T) {
	total, available, err := parseMemInfo("MemTotal:       1000 kB\nMemAvailable:    250 kB\n")
	if err != nil || total != 1024000 || available != 256000 {
		t.Fatalf("memory counters: total=%d available=%d err=%v", total, available, err)
	}
	load1, load5, load15, err := parseLoadAverage("1.25 0.75 0.50 1/100 123\n")
	if err != nil || load1 != 1.25 || load5 != 0.75 || load15 != 0.5 {
		t.Fatalf("load counters: %v %v %v err=%v", load1, load5, load15, err)
	}
	uptime, err := parseUptime("1234.50 100.00\n")
	if err != nil || uptime != 1234.5 {
		t.Fatalf("uptime=%v err=%v", uptime, err)
	}
	if used := percentUsed(1000, 250); used != 75 {
		t.Fatalf("percent used=%v", used)
	}
}
