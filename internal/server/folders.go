package server

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
)

// folderEntry は一覧の 1 つのフォルダ。
type folderEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// foldersResponse は GET /api/folders の答え。
type foldersResponse struct {
	// Path は正規化した今の場所（根の一覧なら空）。
	Path string `json:"path"`
	// Parent は親の場所。根（ドライブの根・UNC の共有の根を含む）と根の一覧では空。
	Parent  string        `json:"parent"`
	Folders []folderEntry `json:"folders"`
}

// registerFolders はフォルダの API を mux に載せる。
//
//   - GET /api/folders?path=<絶対パス>: path の下のフォルダの一覧（名前の順。ファイルは返さない）。path が空なら根の一覧
//     （Linux は /、Windows はドライブ）。相対パス・無いフォルダ・フォルダでないもの・読めないフォルダは 400。
//     たどる範囲は絞らない（ログインした人は、設定で serve が書けるところならどこでも保存先にできるため）
func (u *UI) registerFolders(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/folders", u.guard(u.authed(u.getFolders)))
}

func (u *UI) getFolders(w http.ResponseWriter, r *http.Request) {
	resp, err := listFolders(r.URL.Query().Get("path"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// listFolders は p の下のフォルダを返す。p が空なら根の一覧。
func listFolders(p string) (foldersResponse, error) {
	if p == "" {
		roots, err := rootFolders()
		if err != nil {
			return foldersResponse{}, fmt.Errorf("根の一覧を読めない: %v", err)
		}
		return foldersResponse{Folders: roots}, nil
	}
	if !filepath.IsAbs(p) {
		return foldersResponse{}, fmt.Errorf("絶対パスで書いてほしい: %s", p)
	}
	p = filepath.Clean(p)
	fi, err := os.Stat(p)
	if err != nil {
		return foldersResponse{}, fmt.Errorf("フォルダを開けない: %v", err)
	}
	if !fi.IsDir() {
		return foldersResponse{}, fmt.Errorf("フォルダではない: %s", p)
	}
	entries, err := os.ReadDir(p)
	if err != nil {
		return foldersResponse{}, fmt.Errorf("フォルダを読めない: %v", err)
	}
	resp := foldersResponse{Path: p, Folders: []folderEntry{}}
	if parent := filepath.Dir(p); parent != p {
		resp.Parent = parent
	}
	for _, e := range entries {
		if e.Type().IsRegular() {
			continue
		}
		child := filepath.Join(p, e.Name())
		// シンボリックリンクとジャンクションは先をたどり、フォルダならフォルダとして出す（先が無い・読めないものは出さない）。
		if st, err := os.Stat(child); err != nil || !st.IsDir() {
			continue
		}
		resp.Folders = append(resp.Folders, folderEntry{Name: e.Name(), Path: child})
	}
	sort.Slice(resp.Folders, func(i, j int) bool { return resp.Folders[i].Name < resp.Folders[j].Name })
	return resp, nil
}
