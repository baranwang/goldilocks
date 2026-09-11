//go:build windows

package prwatch

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func TestControllerCLIValidatesProtocolBeforePlatform(t *testing.T) {
	c, err := NewController(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err = c.RunCLI(context.Background(), []string{"start", "--pr", specPR}, "", t.TempDir(), &out)
	if err == nil || !strings.Contains(err.Error(), "runtime ID") || out.Len() != 0 {
		t.Fatalf("unexpected protocol result: %v %q", err, out.String())
	}
	err = c.RunCLI(context.Background(), []string{"start", "--pr", specPR}, specParent, t.TempDir(), &out)
	if !errors.Is(err, ErrUnsupportedPlatform) || out.Len() != 0 {
		t.Fatalf("managed execution reached Windows runtime: %v %q", err, out.String())
	}
}
