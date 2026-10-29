package config

import (
	"testing"
)

// backup_dir と backup_keep は書いたとおりに当たり、backup_dir は空にできる（自動のバックアップをやめる）。
// DB に置く形（Values）から作り直しても同じになる。
func TestApplyBackup(t *testing.T) {
	if c := Default(); c.BackupDir != "" || c.BackupKeep != DefaultBackupKeep {
		t.Fatalf("既定 = %q, %d", c.BackupDir, c.BackupKeep)
	}
	dir := t.TempDir()
	c, err := apply(Default(), `{"backup_dir": `+js(dir)+`, "backup_keep": 3}`)
	if err != nil {
		t.Fatal(err)
	}
	if c.BackupDir != dir || c.BackupKeep != 3 {
		t.Fatalf("got %q, %d", c.BackupDir, c.BackupKeep)
	}

	got, err := FromStored(Values(c))
	if err != nil {
		t.Fatal(err)
	}
	if got.BackupDir != dir || got.BackupKeep != 3 {
		t.Fatalf("Values から作り直した後 = %q, %d", got.BackupDir, got.BackupKeep)
	}

	// 空にすると、自動では取らない。
	off, err := apply(c, `{"backup_dir": ""}`)
	if err != nil {
		t.Fatal(err)
	}
	if off.BackupDir != "" || off.BackupKeep != 3 {
		t.Fatalf("空にした後 = %q, %d", off.BackupDir, off.BackupKeep)
	}
}
