//go:build windows

package main

import (
	"fmt"
	"os"
	"testing"
)

func TestAcquireSingleRefusesSecond(t *testing.T) {
	name := fmt.Sprintf(`Local\rajikura-tray-test-%d`, os.Getpid())
	rel1, ok, err := acquireSingle(name)
	if err != nil || !ok {
		t.Fatalf("1 つ目が取れない: ok=%v err=%v", ok, err)
	}
	_, ok2, err := acquireSingle(name)
	if err != nil {
		t.Fatal(err)
	}
	if ok2 {
		t.Fatal("2 つ目も取れてしまった")
	}
	rel1()
	rel3, ok3, err := acquireSingle(name)
	if err != nil || !ok3 {
		t.Fatalf("放した後に取れない: ok=%v err=%v", ok3, err)
	}
	rel3()
}
