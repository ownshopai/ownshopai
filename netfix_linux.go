//go:build linux

package main

// Termux on Android runs our static Linux programs, but it has no /etc/resolv.conf and no /etc/ssl:
// Go's own DNS resolver then asks 127.0.0.1:53 (nothing there) and finds no CA roots for TLS.
// This file fixes both at start-up. It uses only the standard library because
// scripts/linux-arm64.sh copies it unchanged into the bundled wacli's main package as well.

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
)

func init() { netfixApply(os.Getenv("PREFIX"), "/etc/resolv.conf") }

func netfixApply(prefix, resolvConf string) {
	if os.Getenv("SSL_CERT_FILE") == "" && prefix != "" {
		if _, err := os.Stat("/etc/ssl"); err != nil {
			if c := filepath.Join(prefix, "etc", "tls", "cert.pem"); netfixReadable(c) {
				os.Setenv("SSL_CERT_FILE", c) // also inherited by child programs
			}
		}
	}
	if netfixReadable(resolvConf) {
		return
	}
	servers := netfixServers(prefix)
	net.DefaultResolver = &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		var d net.Dialer
		var err error
		for _, s := range servers {
			c, e := d.DialContext(ctx, network, s)
			if e == nil {
				return c, nil
			}
			err = e
		}
		return nil, err
	}}
}

func netfixReadable(p string) bool {
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	f.Close()
	return true
}

// netfixServers lists DNS servers: those in $PREFIX/etc/resolv.conf (Termux ships one), then 1.1.1.1 and 8.8.8.8.
func netfixServers(prefix string) []string {
	var out []string
	if prefix != "" {
		if b, err := os.ReadFile(filepath.Join(prefix, "etc", "resolv.conf")); err == nil {
			for _, line := range strings.Split(string(b), "\n") {
				f := strings.Fields(line)
				if len(f) >= 2 && f[0] == "nameserver" && net.ParseIP(f[1]) != nil {
					out = append(out, net.JoinHostPort(f[1], "53"))
				}
			}
		}
	}
	return append(out, "1.1.1.1:53", "8.8.8.8:53")
}
