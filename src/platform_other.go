//go:build !windows || !amd64

package main

import (
	"context"
	"errors"
)

func nativeNames() bool { return false }
func platformMount(context.Context, Config, []string) error {
	return errors.New("native mounting requires Windows x64 with WinFsp Core")
}
