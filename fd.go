package grace

import (
	"net"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// GetFiles receives file descriptors from a Unix domain socket.
//
// You need to close all files in the returned slice. The slice can be
// non-empty even if this function returns an error.
//
// Use net.FileConn() if you're receiving a network connection.
func GetFiles(uc *net.UnixConn, filenames []string) ([]*os.File, error) {
	buf := make([]byte, 1)
	oob := make([]byte, syscall.CmsgSpace(4*len(filenames)))
	_, oobn, _, _, err := uc.ReadMsgUnix(buf, oob)
	if err != nil {
		return nil, err
	}

	scms, err := unix.ParseSocketControlMessage(oob[0:oobn])
	if err != nil {
		return nil, err
	}
	if len(scms) != 1 {
		return nil, err
	}
	gotFds, err := unix.ParseUnixRights(&scms[0])

	res := make([]*os.File, 0, len(gotFds))

	for fi, fd := range gotFds {
		syscall.CloseOnExec(fd)

		var filename string
		if fi < len(filenames) {
			filename = filenames[fi]
		}
		res = append(res, os.NewFile(uintptr(fd), filename))
	}

	return res, err
}

// SendFiles sends file descriptors to Unix domain socket.
func SendFiles(uc *net.UnixConn, fds []int) error {
	if len(fds) == 0 {
		return nil
	}

	buf := make([]byte, 1)
	rights := syscall.UnixRights(fds...)
	_, _, err := uc.WriteMsgUnix(buf, rights, nil)
	if err != nil {
		return err
	}
	return nil
}

type fileName struct {
	Network string
	Address string
}

func (name fileName) isUnix() bool {
	if name.Network == "unix" || name.Network == "unixpacket" {
		return true
	}
	return false
}

func (name fileName) socketFileExist() bool {
	info, err := os.Stat(name.Address)
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeSocket == os.ModeSocket
}

func (name fileName) String() string {
	return name.Network + ":" + name.Address
}
