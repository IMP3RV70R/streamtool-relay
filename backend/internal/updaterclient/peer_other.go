//go:build !linux

package updaterclient

import (
	"errors"
	"net"
)

func checkRootPeer(net.Conn) error { return errors.New("host updater requires Linux") }
