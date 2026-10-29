//go:build !windows

package server

// rootFolders は根の一覧。Linux などは / の 1 つ。
func rootFolders() ([]folderEntry, error) {
	return []folderEntry{{Name: "/", Path: "/"}}, nil
}
