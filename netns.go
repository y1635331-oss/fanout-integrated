//go:build linux

package main

import (
	"context"
	"fmt"
	"golang.org/x/sys/unix"
	"net"
	"os"
	"runtime"
	"strconv"
	"time"
)

// All business sockets, including DNS, are created inside the namespace and
// bound to tun0. A missing VPN interface fails closed; there is no host fallback.
func dialerInNetns(ns string) func(string, string) (net.Conn, error) {
	return func(network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		if net.ParseIP(host) == nil {
			resolver := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
				return rawNamespaceDial(ns, network, "1.1.1.1:53")
			}}
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			ips, e := resolver.LookupIP(ctx, "ip4", host)
			if e != nil {
				return nil, e
			}
			if len(ips) == 0 {
				return nil, fmt.Errorf("no IPv4 address")
			}
			host = ips[0].String()
		}
		return rawNamespaceDial(ns, network, net.JoinHostPort(host, port))
	}
}
func rawNamespaceDial(ns, network, address string) (net.Conn, error) {
	host, p, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	ip := net.ParseIP(host).To4()
	if ip == nil {
		return nil, fmt.Errorf("IPv4 required")
	}
	port, err := strconv.Atoi(p)
	if err != nil || port < 1 || port > 65535 {
		return nil, fmt.Errorf("invalid port")
	}
	type result struct {
		c net.Conn
		e error
	}
	done := make(chan result, 1)
	go func() {
		runtime.LockOSThread()
		// If restore fails this goroutine exits while locked, retiring the OS thread.
		origin, e := os.Open("/proc/thread-self/ns/net")
		if e != nil {
			done <- result{nil, e}
			return
		}
		defer origin.Close()
		target, e := os.Open("/var/run/netns/" + ns)
		if e != nil {
			runtime.UnlockOSThread()
			done <- result{nil, e}
			return
		}
		defer target.Close()
		if e = unix.Setns(int(target.Fd()), unix.CLONE_NEWNET); e != nil {
			runtime.UnlockOSThread()
			done <- result{nil, e}
			return
		}
		typ := unix.SOCK_STREAM
		if network == "udp" || network == "udp4" {
			typ = unix.SOCK_DGRAM
		}
		fd, e := unix.Socket(unix.AF_INET, typ|unix.SOCK_NONBLOCK|unix.SOCK_CLOEXEC, 0)
		if e == nil {
			e = unix.SetsockoptString(fd, unix.SOL_SOCKET, unix.SO_BINDTODEVICE, "tun0")
		}
		if e == nil {
			sa := &unix.SockaddrInet4{Port: port}
			copy(sa.Addr[:], ip)
			e = unix.Connect(fd, sa)
			if e == unix.EINPROGRESS {
				poll := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLOUT}}
				var n int
				n, e = unix.Poll(poll, 10000)
				if e == nil && n == 0 {
					e = fmt.Errorf("dial timeout")
				}
				if e == nil {
					var errno int
					errno, e = unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_ERROR)
					if e == nil && errno != 0 {
						e = unix.Errno(errno)
					}
				}
			}
		}
		restore := unix.Setns(int(origin.Fd()), unix.CLONE_NEWNET)
		if restore == nil {
			runtime.UnlockOSThread()
		} else {
			e = restore
		}
		if e != nil {
			if fd >= 0 {
				unix.Close(fd)
			}
			done <- result{nil, e}
			return
		}
		f := os.NewFile(uintptr(fd), "fanout-tunnel")
		c, e := net.FileConn(f)
		f.Close()
		done <- result{c, e}
	}()
	r := <-done
	return r.c, r.e
}
func forceIPv4Network(network string) string {
	switch network {
	case "tcp":
		return "tcp4"
	case "udp":
		return "udp4"
	}
	return network
}
