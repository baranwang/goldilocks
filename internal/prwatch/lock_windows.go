//go:build windows

package prwatch

func flock(path string) (func() error, error) {
	return nil, ErrUnsupportedPlatform
}
