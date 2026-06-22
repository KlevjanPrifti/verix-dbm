package web

// Server-side egress guard. The "test connection" probe and connection create/
// update let an admin point the server at an arbitrary host:port, which is a
// classic SSRF lever: reaching the cloud-metadata endpoint (169.254.169.254),
// loopback services on the app host, or scanning the internal network. We block
// the genuinely dangerous ranges by default while still allowing private RFC1918
// addresses, because real databases routinely live on private networks.

import (
	"context"
	"fmt"
	"net"
	"strings"
)

// guardEgressHost resolves host and rejects targets that should never be dialed
// from a server-side connection: loopback, link-local (including the
// 169.254.169.254 cloud-metadata endpoint and fe80::/10), the unspecified
// address, and multicast. A hostname is rejected if ANY resolved address is
// blocked, so a name can't smuggle a metadata IP alongside a public one. When
// allowLocal is true the guard is skipped entirely (dev mode, or an operator who
// runs the database on loopback and sets DBM_ALLOW_LOCAL_TARGETS=true).
func guardEgressHost(ctx context.Context, host string, allowLocal bool) error {
	if allowLocal {
		return nil
	}
	host = strings.TrimSpace(host)
	if host == "" {
		return fmt.Errorf("empty host")
	}
	var ips []net.IP
	if ip := net.ParseIP(host); ip != nil {
		ips = []net.IP{ip}
	} else {
		var res net.Resolver
		addrs, err := res.LookupIPAddr(ctx, host)
		if err != nil {
			return fmt.Errorf("resolve %q: %w", host, err)
		}
		for _, a := range addrs {
			ips = append(ips, a.IP)
		}
	}
	for _, ip := range ips {
		if blockedEgressIP(ip) {
			return fmt.Errorf("target %q resolves to a disallowed address (%s): loopback, link-local, and "+
				"cloud-metadata ranges are blocked. Set DBM_ALLOW_LOCAL_TARGETS=true to override", host, ip)
		}
	}
	return nil
}

// blockedEgressIP reports whether ip is in a range the server must not dial. IPv4
// addresses mapped into IPv6 (e.g. ::ffff:127.0.0.1) are unmapped by net.IP's
// methods, so they are caught too.
func blockedEgressIP(ip net.IP) bool {
	return ip.IsLoopback() ||
		ip.IsLinkLocalUnicast() || // 169.254.0.0/16 (incl. cloud metadata), fe80::/10
		ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() ||
		ip.IsMulticast() ||
		ip.IsUnspecified()
}
