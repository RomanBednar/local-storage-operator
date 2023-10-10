package util

import (
	"testing"
)

type VolumeHelper interface {
	FormatAsExt4(*testing.T, string)
	HasExt4(string) bool
}
