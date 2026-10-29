//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	toast "git.sr.ht/~jackmordaunt/go-toast/v2"
	"golang.org/x/sys/windows/registry"
)

// appID はトーストの AUMID（AppUserModelID）。
const appID = "rajikura"

// aumidKey は、AUMID の表示名とアイコンを書く HKCU のキー。
var aumidKey = `SOFTWARE\Classes\AppUserModelId\` + appID

// registerAUMID は、AUMID の表示名とアイコン（iconPath の PNG）を HKCU に書く。トレイの起動のたびに書き直す
// （go-toast の SetAppData は、値が既にあると書き直さないので使わない）。押したときは Protocol で URL を開くだけなので、
// COM のアクティベーション（CustomActivator・LocalServer32）は書かない。
func registerAUMID(iconPath string) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, aumidKey, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("HKCU\\%s を開けない: %w", aumidKey, err)
	}
	defer k.Close()
	if err := k.SetStringValue("DisplayName", "rajikura"); err != nil {
		return fmt.Errorf("HKCU\\%s の DisplayName を書けない: %w", aumidKey, err)
	}
	if err := k.SetStringValue("IconUri", iconPath); err != nil {
		return fmt.Errorf("HKCU\\%s の IconUri を書けない: %w", aumidKey, err)
	}
	return nil
}

// writeAppIcon は、トーストに出すアプリのアイコンを dir の下の rajikura\tray-icon.png に書き、そのパスを返す。
func writeAppIcon(cacheDir string) (string, error) {
	b, err := appIconPNG()
	if err != nil {
		return "", fmt.Errorf("トーストのアイコンを作れない: %w", err)
	}
	p := filepath.Join(cacheDir, "rajikura", "tray-icon.png")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", fmt.Errorf("%s を作れない: %w", filepath.Dir(p), err)
	}
	if err := os.WriteFile(p, b, 0o644); err != nil {
		return "", fmt.Errorf("%s を書けない: %w", p, err)
	}
	return p, nil
}

// outgoingToast は、出すトーストと、押したときに開く URL（空なら開かない）。
type outgoingToast struct {
	Title, Body, URL string
}

// runToasts は、ch のトーストを順に出し、出せたかを done に渡す（err が nil なら Windows に渡せた）。
// COM を使うので、1 つの OS のスレッドに留めて出す。ch が閉じたら返る。
func runToasts(ch <-chan outgoingToast, done func(outgoingToast, error)) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	for t := range ch {
		n := toast.Notification{
			AppID: appID,
			Title: cdata(t.Title),
			Body:  cdata(t.Body),
		}
		if t.URL != "" {
			n.ActivationType = toast.Protocol
			n.ActivationArguments = toastLaunch(t.URL)
		}
		done(t, n.Push())
	}
}
