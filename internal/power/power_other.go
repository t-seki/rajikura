//go:build !windows

package power

import "time"

func acquire(string) (func(), error) { return func() {}, nil }

// noWaker は何もしない Waker（タイマーは鳴らない）。
type noWaker struct{}

func newWaker() Waker { return noWaker{} }

func (noWaker) Set(time.Time) ([]error, error) { return nil, nil }
func (noWaker) Cancel() error                  { return nil }
func (noWaker) Fired() <-chan time.Time        { return nil }
func (noWaker) Close()                         {}
