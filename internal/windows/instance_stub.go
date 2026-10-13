//go:build !windows && !darwin && !linux

package windows

type SingleInstance struct{}

func AcquireSingleInstance(name string) (*SingleInstance, bool, error) {
	return &SingleInstance{}, true, nil
}
func (i *SingleInstance) Close() {}
