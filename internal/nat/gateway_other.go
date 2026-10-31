//go:build !linux

package nat

import (
	"fmt"
	"runtime"
)

func defaultGateway() (string, error) {
	return "", fmt.Errorf("nat: automatic default-gateway discovery is not implemented on %s; configure a gateway", runtime.GOOS)
}
