//go:build !windows

package secure

func ProtectData([]byte) ([]byte, error)   { return nil, ErrUnsupported }
func UnprotectData([]byte) ([]byte, error) { return nil, ErrUnsupported }
func AcquireOwner() (func(), error)        { return nil, ErrUnsupported }
