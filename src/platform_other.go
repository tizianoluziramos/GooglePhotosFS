//go:build !windows || !amd64

package main

import (
	"context"
	"errors"
)

func nativeNames() bool { return false }
func platformMount(context.Context, Config, []string) error {
	return errors.New("el montaje nativo requiere Windows x64 con WinFsp Core")
}
