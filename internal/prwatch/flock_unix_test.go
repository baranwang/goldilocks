//go:build darwin || linux

package prwatch_test

import (
	"errors"
	"os"
	"strings"
	"syscall"
	"testing"

	pw "github.com/baranwang/goldilocks/internal/prwatch"
)

func TestLockInteroperatesWithExistingFlock(t *testing.T) {
	s := mustStore(t, t.TempDir())
	path := strings.TrimSuffix(s.Path, ".json") + ".lock"
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	check(t, err)
	check(t, syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB))
	defer f.Close()
	if release, err := s.Lock(true); !errors.Is(err, pw.ErrLocked) {
		if release != nil {
			release()
		}
		t.Fatal("existing flock owner was ignored", err)
	}
}
