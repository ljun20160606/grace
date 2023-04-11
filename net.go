package grace

import (
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	DefaultUpgradeSock = "grace_upgrade.sock"
)

// upgradeTimeout is the duration that timeout of write or read in processes while upgrading
const upgradeTimeout = 3 * time.Second

type UpgradeRequest struct {
	Path string
}

const (
	PathMetadata = "/metadata"
	PathReady    = "/ready"
)

type FdsMetadata struct {
	Filenames []fileName
}

type NetGraceOption struct {
	UpgradeSock string
}

// NetGrace handles zero downtime upgrades and passing files between processes.
type NetGrace struct {
	inheritedListener map[fileName]net.Listener
	usedListener      map[fileName]net.Listener

	upgradeListener net.Listener
	upgraded        uint32

	mutex    sync.Mutex
	exitOnce sync.Once

	option NetGraceOption
}

func NewNetGrace(option NetGraceOption) (*NetGrace, error) {
	if option.UpgradeSock == "" {
		option.UpgradeSock = DefaultUpgradeSock
	}

	n := &NetGrace{
		inheritedListener: map[fileName]net.Listener{},
		usedListener:      map[fileName]net.Listener{},
		option:            option,
	}

	if err := n.Inherit(); err != nil {
		return nil, err
	}
	return n, nil
}

// Inherit load listener from old process by unixSocket
// 1. Dail to upgradeSocket to determine whether there is an old process
// 2. Reads length of fds from old process. Returns if length is 0
// 3. Response ack after bind listenSocket. Gets fds by listenSocket.
func (n *NetGrace) Inherit() error {
	n.mutex.Lock()
	defer n.mutex.Unlock()
	// Determine whether there is an old process
	uc, err := net.DialTimeout("unix", n.option.UpgradeSock, upgradeTimeout)
	if err != nil {
		if isNotExistGraceSock(err) {
			log.Info("[grace] %s doesn't exist", n.option.UpgradeSock)
			return nil
		}
		return err
	}
	defer uc.Close()

	// read metadata of fds
	fdsMetadata, err := n.applyFdsMetadata(uc)
	if err != nil {
		return err
	}

	log.Info("[grace] read the fdsMetadata from old process %+v", fdsMetadata)
	if len(fdsMetadata.Filenames) == 0 {
		return nil
	}

	names := make([]string, len(fdsMetadata.Filenames))
	for i := range fdsMetadata.Filenames {
		names = append(names, fdsMetadata.Filenames[i].String())
	}

	// get fds
	_ = uc.SetDeadline(time.Now().Add(upgradeTimeout))
	getFiles, err := GetFiles(uc.(*net.UnixConn), names)
	if err != nil {
		return err
	}
	for i, f := range getFiles {
		listener, err := net.FileListener(f)
		if err != nil {
			return err
		}
		_ = f.Close()

		// unlink of listener is true returned by net.ListenUnix
		// unlink of listener is false returned by net.FileListener
		if listener, ok := listener.(unlinkOnCloser); ok {
			listener.SetUnlinkOnClose(true)
		}

		filename := fdsMetadata.Filenames[i]
		n.inheritedListener[filename] = listener
	}
	return nil
}

// Listen return Listener if it in the inherited map and move it to used list
// otherwise net.Listen
func (n *NetGrace) Listen(network, address string) (net.Listener, error) {
	n.mutex.Lock()
	defer n.mutex.Unlock()

	name := fileName{Network: network, Address: address}
	l := n.inheritedListener[name]
	if l != nil {
		delete(n.inheritedListener, name)
		n.usedListener[name] = l
		log.Info("[grace] return listener from inherit name = %s", name)
		return l, nil
	}

	ln, err := net.Listen(network, address)
	if err != nil {
		return nil, err
	}

	n.usedListener[name] = ln

	return ln, nil
}

func (n *NetGrace) Ready() (chan struct{}, error) {
	conn, err := net.DialTimeout("unix", n.option.UpgradeSock, upgradeTimeout)
	if err != nil {
		if isNotExistGraceSock(err) {
			return n.wait()
		}
		return nil, err
	}

	readyRequest := NetGraceProtocol{Data: UpgradeRequest{Path: PathReady}}
	_ = conn.SetDeadline(time.Now().Add(upgradeTimeout))
	if _, err := readyRequest.WriteTo(conn); err != nil {
		return nil, fmt.Errorf("send ready %s", err)
	}

	var msg string
	readyResp := NetGraceProtocol{Data: &msg}
	_ = conn.SetDeadline(time.Now().Add(upgradeTimeout))
	if _, err := readyResp.ReadFrom(conn); err != nil {
		return nil, fmt.Errorf("wait ready resp %s", err)
	}

	return n.wait()
}

func (n *NetGrace) Close() error {
	if atomic.LoadUint32(&n.upgraded) == 1 {
		return n.stopFiles()
	}
	return n.closeFiles()
}

func (n *NetGrace) CloseListener(ln net.Listener) {
	n.mutex.Lock()
	defer n.mutex.Unlock()
	for name, l := range n.usedListener {
		if l == ln {
			delete(n.usedListener, name)
			break
		}
	}
	_ = ln.Close()
}

func (n *NetGrace) ListInheritFilenames() []fileName {
	var filenames []fileName
	n.mutex.Lock()
	defer n.mutex.Unlock()
	for name := range n.inheritedListener {
		filenames = append(filenames, name)
	}
	return filenames
}

// wait handles request of reading listener from new Process by unixSocket
func (n *NetGrace) wait() (chan struct{}, error) {
	_ = syscall.Unlink(n.option.UpgradeSock)

	// bind upgrade.sock
	ln, err := net.Listen("unix", n.option.UpgradeSock)
	if err != nil {
		return nil, err
	}

	// used by closeFiles to clean resource
	n.mutex.Lock()
	n.upgradeListener = ln
	n.mutex.Unlock()

	c := make(chan struct{})

	go func() {
		for {
			// accept connection of new process
			conn, err := ln.Accept()
			if err != nil {
				break
			}

			go n.handleConn(conn)
		}
		n.closeUpgradeListener()
		close(c)
	}()

	return c, nil
}

func (n *NetGrace) closeFiles() (err error) {
	n.exitOnce.Do(func() {
		n.mutex.Lock()
		defer n.mutex.Unlock()
		for _, ln := range n.usedListener {
			_ = ln.Close()
		}
		n.usedListener = make(map[fileName]net.Listener, 0)

		if e := n.closeIdleFiles(); e != nil {
			err = e
		}

		n.closeUpgradeListener()
	})
	return
}

func (n *NetGrace) stopFiles() (err error) {
	n.exitOnce.Do(func() {
		n.mutex.Lock()
		defer n.mutex.Unlock()
		for _, ln := range n.usedListener {
			if e := StopListener(ln); e != nil {
				err = e
			}
		}

		if e := n.closeIdleFiles(); e != nil {
			err = e
		}

		n.closeUpgradeListener()
	})
	return
}

func (n *NetGrace) CloseIdleFiles() error {
	n.mutex.Lock()
	defer n.mutex.Unlock()
	return n.closeIdleFiles()
}

func (n *NetGrace) handleConn(conn net.Conn) {
	defer conn.Close()

	request := UpgradeRequest{}
	protocol := NetGraceProtocol{Data: &request}
	if _, err := protocol.ReadFrom(conn); err != nil {
		log.Error("handleConn %s", err)
		return
	}

	switch request.Path {
	case PathMetadata:
		if err := n.handleUpgrade(conn); err != nil {
			log.Error("handleUpgrade %s", err)
		} else {
			atomic.StoreUint32(&n.upgraded, 1)
		}
	case PathReady:
		if err := n.handleReady(conn); err != nil {
			log.Error("handleReady %s", err)
		}
	default:
	}
}

// handleUpgrade
// 1. Send length of fds to new process. Returns if length is 0 after send
// 2. Read ack after new process has listened listenSocket
// 3. Send fds to new process by listenSocket
func (n *NetGrace) handleUpgrade(uc net.Conn) error {
	// copy fds
	filenames, fds, err := n.listUsedFds()
	if err != nil {
		return err
	}

	// write fdsMetadata to new process
	if err := n.sendFdsMetadata(uc, filenames); err != nil {
		return err
	}
	if len(filenames) == 0 {
		return nil
	}

	// dail to listen.sock
	_ = uc.SetDeadline(time.Now().Add(upgradeTimeout))
	if err := SendFiles(uc.(*net.UnixConn), fds); err != nil {
		return err
	}

	return nil
}

func (n *NetGrace) handleReady(conn net.Conn) error {
	n.mutex.Lock()
	n.closeUpgradeListener()
	n.mutex.Unlock()

	protocol := NetGraceProtocol{Data: "ok"}
	if _, err := protocol.WriteTo(conn); err != nil {
		return fmt.Errorf("response ready err %s", err)
	}
	return nil
}

func (n *NetGrace) sendFdsMetadata(upgradeConn net.Conn, filenames []fileName) error {
	_ = upgradeConn.SetDeadline(time.Now().Add(upgradeTimeout))
	fdsMetadata := FdsMetadata{Filenames: filenames}
	protocol := &NetGraceProtocol{Data: fdsMetadata}
	if _, err := protocol.WriteTo(upgradeConn); err != nil {
		return err
	}
	return nil
}

func (n *NetGrace) applyFdsMetadata(conn net.Conn) (*FdsMetadata, error) {
	if err := conn.SetDeadline(time.Now().Add(upgradeTimeout)); err != nil {
		return nil, err
	}
	apply := &NetGraceProtocol{Data: UpgradeRequest{Path: PathMetadata}}
	if _, err := apply.WriteTo(conn); err != nil {
		return nil, fmt.Errorf("apply metadata %s", err)
	}

	if err := conn.SetDeadline(time.Now().Add(upgradeTimeout)); err != nil {
		return nil, err
	}
	fdsMetadata := &FdsMetadata{}
	read := &NetGraceProtocol{Data: fdsMetadata}
	if _, err := read.ReadFrom(conn); err != nil {
		return nil, fmt.Errorf("read metadata %s", err)
	}
	return fdsMetadata, nil
}

func (n *NetGrace) listUsedFds() ([]fileName, []int, error) {
	var filenames []fileName
	var fds []int
	n.mutex.Lock()
	for name, l := range n.usedListener {
		rawConn, err := l.(syscall.Conn).SyscallConn()
		if err != nil {
			return nil, nil, err
		}
		if err := rawConn.Control(func(fd uintptr) {
			fds = append(fds, int(fd))
		}); err != nil {
			return nil, nil, err
		}
		filenames = append(filenames, name)
	}
	n.mutex.Unlock()
	return filenames, fds, nil
}

func (n *NetGrace) closeUpgradeListener() {
	if n.upgradeListener != nil {
		_ = n.upgradeListener.Close()
	}
}

func (n *NetGrace) closeIdleFiles() (err error) {
	for _, ln := range n.inheritedListener {
		if e := ln.Close(); !isClosedError(e) && e != nil {
			err = e
		}
	}
	n.inheritedListener = make(map[fileName]net.Listener, 0)
	return
}

func StopListener(ln net.Listener) error {
	switch ln := ln.(type) {
	case *net.UnixListener:
		_ = ln.SetDeadline(time.Now())
	case *net.TCPListener:
		_ = ln.SetDeadline(time.Now())
	default:
		return fmt.Errorf("unknown net.Listener type. only support unix or tcp")
	}
	return nil
}

func Fork() (int, error) {
	execSpec := &os.ProcAttr{
		Env:   os.Environ(),
		Files: append([]*os.File{os.Stdin, os.Stdout, os.Stderr}),
		Sys: &syscall.SysProcAttr{
			Setpgid: true,
			Pgid:    getpid(),
		},
	}

	// Fork exec the new version of your server
	process, err := os.StartProcess(os.Args[0], os.Args, execSpec)
	if err != nil {
		return 0, err
	}

	return process.Pid, nil
}

// Make sure current process exit won't affect process of child.
func getpid() int {
	var newPgid int
	pid := os.Getpid()
	pgid, err := syscall.Getpgid(pid)
	if err != nil {
		return newPgid
	}
	if pid != pgid {
		newPgid = pgid
	}
	return newPgid
}

type unlinkOnCloser interface {
	SetUnlinkOnClose(bool)
}

func isNotExistGraceSock(err error) bool {
	if err, ok := err.(*net.OpError); ok {
		msg := err.Unwrap().Error()
		return strings.Contains(msg, "no such file") || strings.Contains(msg, "connection refuse")
	}
	return false
}

func isClosedError(err error) bool {
	if err, ok := err.(*net.OpError); ok {
		return strings.Contains(err.Unwrap().Error(), "use of closed")
	}
	return false
}
