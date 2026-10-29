//go:build windows

package server

import "golang.org/x/sys/windows"

// rootFolders は根の一覧。serve から見えるドライブを C:\ の形で返す。
func rootFolders() ([]folderEntry, error) {
	mask, err := windows.GetLogicalDrives()
	if err != nil {
		return nil, err
	}
	out := []folderEntry{}
	for i := 0; i < 26; i++ {
		if mask&(1<<uint(i)) == 0 {
			continue
		}
		root := string(rune('A'+i)) + `:\`
		out = append(out, folderEntry{Name: root, Path: root})
	}
	return out, nil
}
