package main

import "testing"

func TestLoopbackAddress(t *testing.T) {
	for _, address := range []string{"127.0.0.1:23751", "[::1]:23751"} {
		if !loopbackAddress(address) {
			t.Fatalf("loopback address rejected: %s", address)
		}
	}
	for _, address := range []string{"0.0.0.0:23751", "192.0.2.10:23751", "localhost:23751", "bad"} {
		if loopbackAddress(address) {
			t.Fatalf("non-numeric or non-loopback address accepted: %s", address)
		}
	}
}
