package power

import "testing"

func TestAcquireRelease(t *testing.T) {
	release, err := Acquire(Reason)
	if err != nil {
		t.Fatal(err)
	}
	release()
	release() // 2 回呼んでも何も起きない
}
