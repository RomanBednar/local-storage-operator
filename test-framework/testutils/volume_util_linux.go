//go:build linux
// +build linux

package util

var _ VolumeHelper = &volumeHelper{}

type volumeHelper struct{}

func NewVolumeHelper() VolumeHelper {
	return &volumeHelper{}
}

func (h *volumeHelper) FormatAsExt4(t *testing.T, fname string) {
	cmd := exec.Command("mkfs.ext4", fname)
	err := cmd.Run()
	if err != nil {
		t.Fatalf("error formatting file: %v; command error: %v", fname, cmd.Err)
	}
}

func (h *volumeHelper) HasExt4(fname string) bool {
	cmd := exec.Command("fsck.ext4", "-n", fname)
	return cmd.Run() == nil
}
