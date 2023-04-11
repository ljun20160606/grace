package grace_test

import (
	"github.com/ljun20160606/grace"
	"net"
	"os"
	"runtime"
	"strings"
	"syscall"
	"testing"
)

func TestUnixListener(t *testing.T) {
	const activeSock = "active.sock"
	const idleSock = "idle.sock"

	syscall.Unlink(activeSock)
	syscall.Unlink(idleSock)

	option := grace.NetGraceOption{}
	grace1, err := grace.NewNetGrace(option)
	if err != nil {
		t.Fatalf("fail to new grace1 %s", err)
	}

	// old process listen
	if _, err := grace1.Listen("unix", activeSock); err != nil {
		t.Fatal(err)
		return
	}
	if _, err := grace1.Listen("unix", idleSock); err != nil {
		t.Fatal(err)
		return
	}

	// grace1
	ready1 := utilWaitReady(t, grace1)

	grace2, err := grace.NewNetGrace(option)
	if err != nil {
		t.Fatalf("fail to new grace2 %s", err)
		return
	}

	// new process listen
	ln2, err := grace2.Listen("unix", activeSock)
	if err != nil {
		t.Fatal(err)
		return
	}

	{
		t.Log("block_ready1")
		select {
		case <-ready1:
			t.Fatal("ready1 should be blocked before grace2 call ready")
			return
		default:
		}
	}

	// grace2 ready
	utilWaitReady(t, grace2)

	{
		t.Log("grace2_ready")
		select {
		case <-ready1:
		default:
			t.Fatal("ready1 should closed after grace2 call ready")
			return
		}
	}

	{
		t.Log("close_grace1")
		// stop listener of old process
		grace1.Close()

		// testSock expected to still exist
		if _, err = os.Stat(activeSock); err != nil {
			if strings.Contains(err.Error(), "no such file") {
				t.Fatalf("active.sock should still alive after grace1 close")
				return
			}
		}
	}

	{
		t.Log("listener_work_in_new_process")
		// client succeed to dail to sock
		clientConn, err := net.Dial("unix", activeSock)
		if err != nil {
			t.Fatal(err)
			return
		}
		// client write foo
		const foo = "foo"
		if _, err := clientConn.Write([]byte(foo)); err != nil {
			t.Fatal(err)
			return
		}
		// server accept conn
		serverConn, err := ln2.Accept()
		if err != nil {
			t.Fatal(err)
			return
		}
		// server read foo
		readBuf := make([]byte, 3)
		if _, err := serverConn.Read(readBuf); err != nil {
			t.Fatal(err)
			return
		}
		_ = clientConn.Close()
		_ = serverConn.Close()
		s := string(readBuf)
		if s != foo {
			t.Fatal("expected foo")
			return
		}
	}

	{
		t.Log("close_files")
		grace2.Close()
		if _, err := os.Stat(activeSock); err == nil {
			t.Fatal(activeSock + " exist")
			return
		}
		if _, err := os.Stat(idleSock); err == nil {
			t.Fatalf(idleSock+" exists err = %s", err)
			return
		}
		if _, err := os.Stat(grace.DefaultUpgradeSock); err == nil {
			t.Fatalf(idleSock+" exists err = %s", err)
			return
		}
	}
}

func utilWaitReady(t *testing.T, g *grace.NetGrace) chan struct{} {
	// old process wait for a new process
	exit, err := g.Ready()
	if err != nil {
		t.Fatal(err)
	}

	// wait util grace listen upgradeSock
	for {
		// dail util old process ready
		conn, err := net.Dial("unix", grace.DefaultUpgradeSock)
		if err != nil {
			runtime.Gosched()
			continue
		}
		_ = conn.Close()
		break
	}

	return exit
}
