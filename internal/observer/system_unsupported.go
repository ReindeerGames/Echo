//go:build !linux

package observer

import "errors"

type unsupportedSystemReader struct{}

func newSystemReader() SystemReader { return unsupportedSystemReader{} }

func (unsupportedSystemReader) Snapshot(string) (SystemSnapshot, error) {
	return SystemSnapshot{}, errors.New("system probes require Linux")
}
