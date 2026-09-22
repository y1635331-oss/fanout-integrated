//go:build !linux

package main

import (
	"fmt"
	"net"
)

func dialerInNetns(ns string) func(string, string) (net.Conn, error) {
	return func(string, string) (net.Conn, error) { return nil, fmt.Errorf("network namespaces require Linux") }
}
func forceIPv4Network(n string) string {
	if n == "tcp" {
		return "tcp4"
	}
	if n == "udp" {
		return "udp4"
	}
	return n
}
