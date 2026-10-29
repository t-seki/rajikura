// Package secret は、設定の秘密（Webhook の URL・SMTP のパスワード・Jellyfin の API キー）を DB に置く前に暗号化する
// （docs/adr/0015-settings-and-secrets-in-db.md）。
//
// 方式は AES-256-GCM。鍵は 32 バイトの乱数で、データのフォルダの鍵のファイル（rajikura.key。base64 の 1 行）に置く。
// 鍵のファイルは、初めて秘密を保存するとき（Seal）に作る。Linux では権限を 0600 にする。Windows では
// ファイルの権限を変えず、データのフォルダの ACL で守る（service install が、サービスのアカウント・SYSTEM・Administrators だけに絞る。
// docs/adr/0020-windows-service-with-virtual-account.md）。
// 暗号文は「nonce（12 バイト）+ 暗号文」で、秘密の名前を追加データにする（別の秘密の欄に写した暗号文は復号できない）。
//
// 鍵のファイルが無い・読めない・合わないときは、復号（Open）をエラーにするだけで、鍵を作り直さない。新しい鍵は、
// 人が秘密を入れ直したとき（Seal）に作る（鍵のファイルが無いか、中身が鍵として読めないときだけ。権限などでファイルを
// 読めないときは、Seal もエラーにして鍵のファイルに触らない）。
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// keySize は鍵の長さ（AES-256）。
const keySize = 32

// ErrNoKey は鍵のファイルが無いこと。
var ErrNoKey = errors.New("鍵のファイルが無い")

// ErrMalformedKey は、鍵のファイルの中身が鍵として読めないこと（読めはしたが、決まった長さの鍵の base64 ではない）。
var ErrMalformedKey = errors.New("鍵のファイルが鍵として読めない")

// Box は鍵のファイル 1 つで秘密を暗号化・復号する。並んで使ってよい。
type Box struct {
	path string

	mu     sync.Mutex
	key    []byte
	keyErr error
}

// Load は path の鍵のファイルを読む。無い・読めないときも Box を返し、KeyError でわけを返す（起動は止めない）。
func Load(path string) *Box {
	b := &Box{path: path}
	b.key, b.keyErr = readKey(path)
	return b
}

func readKey(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNoKey
	}
	if err != nil {
		return nil, fmt.Errorf("鍵のファイル %s を読めない: %w", path, err)
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(data)))
	if err != nil || len(key) != keySize {
		return nil, fmt.Errorf("%w: %s（%d バイトの鍵の base64 ではない）", ErrMalformedKey, path, keySize)
	}
	return key, nil
}

// Path は鍵のファイルのパス。
func (b *Box) Path() string { return b.path }

// KeyError は、鍵が使えないわけ（使えれば nil。鍵のファイルが無ければ ErrNoKey）。
func (b *Box) KeyError() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.keyErr
}

func gcm(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// Open は秘密 name の暗号文 sealed を復号する。鍵が使えない・合わないときはエラーを返す（値は出さない）。
func (b *Box) Open(name string, sealed []byte) (string, error) {
	b.mu.Lock()
	key, keyErr := b.key, b.keyErr
	b.mu.Unlock()
	if keyErr != nil {
		return "", keyErr
	}
	aead, err := gcm(key)
	if err != nil {
		return "", err
	}
	ns := aead.NonceSize()
	if len(sealed) < ns {
		return "", errors.New("暗号文が短すぎる")
	}
	plain, err := aead.Open(nil, sealed[:ns], sealed[ns:], []byte(name))
	if err != nil {
		return "", errors.New("鍵が合わない（復号できない）")
	}
	return string(plain), nil
}

// Seal は秘密 name の値 value を暗号化する。読み込んだときに鍵が使えなかったなら、先に鍵のファイルを読み直し
// （起動の後に人が鍵のファイルを戻したときは、それを使う）、それでも鍵のファイルが無い（ErrNoKey）か中身が鍵として読めない
// （ErrMalformedKey）ときだけ、新しい鍵を作ってファイルに書いてから使う。ファイルを読めない（権限・I/O の誤り）ときは、
// 鍵のファイルに触らずにエラーを返す（その鍵で暗号化したほかの秘密を、二度と復号できなくしないため）。
func (b *Box) Seal(name, value string) ([]byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.keyErr != nil {
		key, err := readKey(b.path)
		switch {
		case err == nil:
		case errors.Is(err, ErrNoKey) || errors.Is(err, ErrMalformedKey):
			key = make([]byte, keySize)
			if _, err := rand.Read(key); err != nil {
				return nil, err
			}
			if err := writeKey(b.path, key); err != nil {
				return nil, err
			}
		default:
			b.keyErr = err
			return nil, err
		}
		b.key, b.keyErr = key, nil
	}
	aead, err := gcm(b.key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return aead.Seal(nonce, nonce, []byte(value), []byte(name)), nil
}

// writeKey は鍵をファイルに書く（権限は 0600。いったん別のファイルに書いてから置き換える）。
func writeKey(path string, key []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(base64.StdEncoding.EncodeToString(key)+"\n"), 0o600); err != nil {
		return fmt.Errorf("鍵のファイル %s を書けない: %w", path, err)
	}
	// 前からあったファイルの権限は WriteFile が変えないので、作り直した後に揃える。
	if err := os.Chmod(tmp, 0o600); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("鍵のファイル %s を書けない: %w", path, err)
	}
	return nil
}
