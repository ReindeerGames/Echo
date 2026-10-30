package main

import "testing"

func TestLoopbackAddress(t *testing.T) {
	for _, address := range []string{"127.0.0.1:23750", "[::1]:23750"} {
		if !loopbackAddress(address) {
			t.Fatalf("expected %q to be accepted", address)
		}
	}
	for _, address := range []string{"0.0.0.0:23750", "192.0.2.1:23750", "localhost:23750", "127.0.0.1", ""} {
		if loopbackAddress(address) {
			t.Fatalf("expected %q to be rejected", address)
		}
	}
}
