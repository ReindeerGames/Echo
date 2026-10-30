//go:build !linux

package processproxy

import (
	"context"
	"errors"
)

type unsupportedScanner struct{}

func newScanner() Scanner { return unsupportedScanner{} }

func (unsupportedScanner) Snapshot(context.Context, string) (Snapshot, error) {
	return Snapshot{}, errors.New("process inspection requires Linux")
}
