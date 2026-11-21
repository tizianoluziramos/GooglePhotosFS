//go:build windows && amd64

package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

func nativeNames() bool { return true }

func eventName(letter, suffix string) (*uint16, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	return windows.UTF16PtrFromString("Local\\GooglePhotosFS-" + user.User.Sid.String() + "-" + letter[:1] + "-" + suffix)
}

func createEvent(letter, suffix string) (windows.Handle, error) {
	name, err := eventName(letter, suffix)
	if err != nil {
		return 0, err
	}
	h, err := windows.CreateEvent(nil, 1, 0, name)
	if err != nil && h != 0 {
		windows.CloseHandle(h)
		h = 0
	}
	return h, err
}

func mountLogPath(cfg Config, letter string) string {
	return filepath.Join(cfg.dir, "mount-"+letter[:1]+".log")
}

func platformMount(ctx context.Context, cfg Config, args []string) error {
	if len(args) > 2 {
		return fmt.Errorf("usage: googlephotosfs %s [P:]", args[0])
	}
	letter := cfg.Filesystem.MountPoint
	if len(args) == 2 {
		letter = args[1]
	}
	letter, err := driveLetter(letter)
	if err != nil {
		return err
	}
	switch args[0] {
	case "unmount":
		name, err := eventName(letter, "stop")
		if err != nil {
			return err
		}
		h, err := windows.OpenEvent(windows.EVENT_MODIFY_STATE, false, name)
		if err != nil {
			return fmt.Errorf("locate drive %s: %w", letter, err)
		}
		defer windows.CloseHandle(h)
		name, err = eventName(letter, "done")
		if err != nil {
			return err
		}
		done, err := windows.OpenEvent(windows.SYNCHRONIZE, false, name)
		if err != nil {
			return err
		}
		defer windows.CloseHandle(done)
		if err = windows.SetEvent(h); err != nil {
			return err
		}
		result, err := windows.WaitForSingleObject(done, 60000)
		if err != nil {
			return err
		}
		if result != windows.WAIT_OBJECT_0 {
			return errors.New("unmounting is still in progress")
		}
		fmt.Println("Unmounted drive " + letter)
		return nil
	case "__mount":
		return mounted(ctx, cfg, letter)
	}
	if _, err = newPhotos(cfg.Photos); err != nil {
		return err
	}
	ready, err := createEvent(letter, "ready")
	if err != nil {
		return fmt.Errorf("prepare to mount %s: %w", letter, err)
	}
	defer windows.CloseHandle(ready)
	if err = os.MkdirAll(cfg.dir, 0700); err != nil {
		return err
	}
	stream, err := os.OpenFile(mountLogPath(cfg, letter), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	defer stream.Close()
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	run := exec.Command(exe, "__mount", letter)
	run.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP, HideWindow: true}
	run.Stdout, run.Stderr = stream, stream
	if err = run.Start(); err != nil {
		return err
	}
	ended := make(chan error, 1)
	go func() { ended <- run.Wait() }()
	fmt.Println("Preparing the Google Photos index...")
	for {
		state, err := windows.WaitForSingleObject(ready, 250)
		if err != nil {
			return err
		}
		if state == windows.WAIT_OBJECT_0 {
			fmt.Println("Mounted drive " + letter)
			return nil
		}
		select {
		case <-ctx.Done():
			stopName, _ := eventName(letter, "stop")
			if stop, e := windows.OpenEvent(windows.EVENT_MODIFY_STATE, false, stopName); e == nil {
				_ = windows.SetEvent(stop)
				_ = windows.CloseHandle(stop)
			}
			return ctx.Err()
		case err := <-ended:
			return fmt.Errorf("the mount process exited before the drive opened (%v). See %s", err, mountLogPath(cfg, letter))
		default:
		}
	}
}

func mounted(parent context.Context, cfg Config, letter string) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	stop, err := createEvent(letter, "stop")
	if err != nil {
		return fmt.Errorf("create mount control for %s: %w", letter, err)
	}
	defer windows.CloseHandle(stop)
	done, err := createEvent(letter, "done")
	if err != nil {
		return err
	}
	defer windows.CloseHandle(done)
	defer windows.SetEvent(done)
	readyName, err := eventName(letter, "ready")
	if err != nil {
		return err
	}
	ready, err := windows.OpenEvent(windows.EVENT_MODIFY_STATE, false, readyName)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(ready)
	var waiter sync.WaitGroup
	waiter.Add(1)
	go func() {
		defer waiter.Done()
		for ctx.Err() == nil {
			code, err := windows.WaitForSingleObject(stop, 250)
			if err != nil || code == windows.WAIT_OBJECT_0 {
				cancel()
				return
			}
		}
	}()
	defer func() { cancel(); waiter.Wait() }()
	s, err := openStore(cfg, true)
	if err != nil {
		return err
	}
	defer s.close()
	if err = s.sync(ctx, false); err != nil {
		if s.catalog.Load() == nil {
			return err
		}
		log.Print(err)
	}
	volume, err := newVolume(ctx, s, letter)
	if err != nil {
		return err
	}
	fs, err := volume.mount(letter)
	if err != nil {
		_ = volume.shutdown()
		return fmt.Errorf("WinFsp %s: %w", letter, err)
	}
	defer func() {
		s.downloads.cancel()
		fs.Unmount()
		if err := volume.shutdown(); err != nil {
			log.Print(err)
		}
	}()
	if err = windows.SetEvent(ready); err != nil {
		return err
	}
	if !cfg.Filesystem.ReadOnly {
		stopPending := volume.startPending(ctx)
		defer stopPending()
	}

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err = s.sync(ctx, false); err != nil && ctx.Err() == nil {
				log.Print(err)
				if pause(ctx, 28*time.Second) != nil {
					return nil
				}
			}
		}
	}
}
