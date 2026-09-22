package main

import (
	"bufio"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func upstreamDialer(raw string) func(string, string) (net.Conn, error) {
	return func(network, addr string) (net.Conn, error) {
		if network != "tcp" && network != "tcp4" {
			return nil, fmt.Errorf("上游代理模式只支持 TCP")
		}
		u, err := url.Parse(raw)
		if err != nil {
			return nil, fmt.Errorf("上游 URL 无效")
		}
		c, err := net.DialTimeout("tcp4", u.Host, 10*time.Second)
		if err != nil {
			return nil, fmt.Errorf("连接上游失败")
		}
		ok := false
		defer func() {
			if !ok {
				c.Close()
			}
		}()
		c.SetDeadline(time.Now().Add(15 * time.Second))
		if u.Scheme == "https" {
			tc := tls.Client(c, &tls.Config{ServerName: u.Hostname(), MinVersion: tls.VersionTLS12})
			if err = tc.Handshake(); err != nil {
				return nil, fmt.Errorf("上游 TLS 校验失败")
			}
			c = tc
		}
		if u.Scheme == "socks5" {
			methods := []byte{5, 1, 0}
			if u.User != nil {
				methods = []byte{5, 1, 2}
			}
			if _, err = c.Write(methods); err != nil {
				return nil, err
			}
			b := make([]byte, 2)
			if _, err = io.ReadFull(c, b); err != nil {
				return nil, err
			}
			if b[0] != 5 || b[1] != methods[2] {
				return nil, fmt.Errorf("上游认证方法不匹配")
			}
			if b[1] == 2 {
				user := u.User.Username()
				pass, _ := u.User.Password()
				auth := append([]byte{1, byte(len(user))}, []byte(user)...)
				auth = append(auth, byte(len(pass)))
				auth = append(auth, []byte(pass)...)
				if _, err = c.Write(auth); err != nil {
					return nil, err
				}
				if _, err = io.ReadFull(c, b); err != nil || b[0] != 1 || b[1] != 0 {
					return nil, fmt.Errorf("上游认证失败")
				}
			}
			host, p, e := net.SplitHostPort(addr)
			if e != nil {
				return nil, e
			}
			port, e := strconv.Atoi(p)
			if e != nil || port < 1 || port > 65535 {
				return nil, fmt.Errorf("目标端口无效")
			}
			var req = []byte{5, 1, 0}
			if ip := net.ParseIP(host).To4(); ip != nil {
				req = append(req, 1)
				req = append(req, ip...)
			} else {
				if len(host) > 255 {
					return nil, fmt.Errorf("域名过长")
				}
				req = append(req, 3, byte(len(host)))
				req = append(req, []byte(host)...)
			}
			req = binary.BigEndian.AppendUint16(req, uint16(port))
			if _, err = c.Write(req); err != nil {
				return nil, err
			}
			head := make([]byte, 4)
			if _, err = io.ReadFull(c, head); err != nil {
				return nil, err
			}
			if head[0] != 5 || head[1] != 0 {
				return nil, fmt.Errorf("上游拒绝连接")
			}
			n := 0
			switch head[3] {
			case 1:
				n = 4
			case 4:
				n = 16
			case 3:
				if _, err = io.ReadFull(c, b[:1]); err != nil {
					return nil, err
				}
				n = int(b[0])
			default:
				return nil, fmt.Errorf("上游响应无效")
			}
			if _, err = io.CopyN(io.Discard, c, int64(n+2)); err != nil {
				return nil, err
			}
		} else {
			request := "CONNECT " + addr + " HTTP/1.1\r\nHost: " + addr + "\r\n"
			if u.User != nil {
				pass, _ := u.User.Password()
				request += "Proxy-Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte(u.User.Username()+":"+pass)) + "\r\n"
			}
			request += "\r\n"
			if _, err = io.WriteString(c, request); err != nil {
				return nil, err
			}
			reader := bufio.NewReader(c)
			resp, e := http.ReadResponse(reader, &http.Request{Method: "CONNECT"})
			if e != nil {
				return nil, fmt.Errorf("上游 HTTP 响应无效")
			}
			if resp.StatusCode != 200 {
				return nil, fmt.Errorf("上游 CONNECT 返回 %d", resp.StatusCode)
			}
			c = &bufferedConn{Conn: c, reader: reader}
		}
		c.SetDeadline(time.Time{})
		ok = true
		return c, nil
	}
}

type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.reader.Read(p) }
func validateUpstream(raw string) (string, error) {
	u, e := url.Parse(strings.TrimSpace(raw))
	if e != nil || u.Hostname() == "" {
		return "", fmt.Errorf("代理 URL 无效")
	}
	if u.Scheme != "socks5" && u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("仅支持 socks5/http/https")
	}
	p, e := strconv.Atoi(u.Port())
	if e != nil || p < 1 || p > 65535 {
		return "", fmt.Errorf("代理必须包含有效端口")
	}
	if u.Path != "" && u.Path != "/" || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("代理 URL 不应包含路径/参数")
	}
	if u.User != nil {
		pass, _ := u.User.Password()
		if len(u.User.Username()) > 255 || len(pass) > 255 || strings.ContainsAny(u.User.Username()+pass, "\r\n") {
			return "", fmt.Errorf("代理凭据无效")
		}
	}
	return u.String(), nil
}
