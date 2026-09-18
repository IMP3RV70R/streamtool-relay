package updaterclient

import (
	"errors"
	"net"
	"syscall"
)

func checkRootPeer(connection net.Conn) error {
	local, ok := connection.(*net.UnixConn)
	if !ok {
		return errors.New("invalid updater peer")
	}
	raw, err := local.SyscallConn()
	if err != nil {
		return errors.New("invalid updater peer")
	}
	var peer *syscall.Ucred
	var socketErr error
	err = raw.Control(func(fd uintptr) {
		peer, socketErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	})
	if err != nil || socketErr != nil || peer == nil || peer.Uid != 0 {
		return errors.New("invalid updater peer")
	}
	return nil
}
