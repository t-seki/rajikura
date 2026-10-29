//go:build windows

package main

import (
	"errors"
	"fmt"

	"golang.org/x/sys/windows/registry"
)

// runKey は、ログイン時に起動するものを登録する、自分の HKCU のキー。runValue はその値の名前。
// service installも同じ名前で登録する。
const (
	runKey   = `Software\Microsoft\Windows\CurrentVersion\Run`
	runValue = "rajikura-tray"
)

// autostartEnabled は、自分の HKCU の Run に登録があるか。
func autostartEnabled() (bool, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("HKCU\\%s を開けない: %w", runKey, err)
	}
	defer k.Close()
	_, _, err = k.GetStringValue(runValue)
	if errors.Is(err, registry.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("HKCU\\%s の %s を読めない: %w", runKey, runValue, err)
	}
	return true, nil
}

// setAutostart は、自分の HKCU の Run に command を登録する（on が false なら登録を消す）。
func setAutostart(on bool, command string) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("HKCU\\%s を開けない: %w", runKey, err)
	}
	defer k.Close()
	if on {
		if err := k.SetStringValue(runValue, command); err != nil {
			return fmt.Errorf("HKCU\\%s に %s を書けない: %w", runKey, runValue, err)
		}
		return nil
	}
	if err := k.DeleteValue(runValue); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return fmt.Errorf("HKCU\\%s の %s を消せない: %w", runKey, runValue, err)
	}
	return nil
}
