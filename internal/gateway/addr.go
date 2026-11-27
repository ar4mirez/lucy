package gateway

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
)

// AddressPolicy decides whether the gateway may connect to an address. It returns nil to allow
// it, or why not.
type AddressPolicy func(netip.Addr) error

// ErrAddressDenied is the error of an address the policy refuses.
var ErrAddressDenied = errors.New("gateway: address denied")

// deniedPrefixes are the ranges no upstream may resolve into (LUCY-CRED-004): this machine,
// private networks, link-local (cloud metadata lives at 169.254.169.254), shared address space
// (100.64.0.0/10 holds some clouds' metadata), reserved, documentation, benchmarking and
// multicast ranges, and IPv6 forms that embed an IPv4 address (NAT64, 6to4, Teredo) or a private one.
var deniedPrefixes = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{
		"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12",
		"192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "192.168.0.0/16", "198.18.0.0/15",
		"198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4", "255.255.255.255/32",
		"::/128", "::1/128", "::ffff:0:0/96", "64:ff9b::/96", "64:ff9b:1::/48", "100::/64",
		"2001::/32", "2001:db8::/32", "2002::/16", "fc00::/7", "fe80::/10", "fec0::/10", "ff00::/8",
	} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

// PublicOnly is the gateway's address policy: only global unicast addresses outside every denied
// range. It is the only policy outside tests.
func PublicOnly(a netip.Addr) error {
	a = a.Unmap()
	if !a.IsValid() || !a.IsGlobalUnicast() || a.IsPrivate() {
		return fmt.Errorf("%w: %s is not a public address", ErrAddressDenied, a)
	}
	for _, p := range deniedPrefixes {
		if p.Contains(a) {
			return fmt.Errorf("%w: %s is in %s", ErrAddressDenied, a, p)
		}
	}
	return nil
}

// Resolver looks a host name up. *net.Resolver implements it.
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// resolve returns host's addresses when every one of them is allowed. A host that resolves to
// any denied address is refused whole, so a name cannot mix a public address with an internal one
// and have the dialer pick. A literal address is checked the same way.
func resolve(ctx context.Context, r Resolver, allow AddressPolicy, host string) ([]netip.Addr, error) {
	var addrs []netip.Addr
	if a, err := netip.ParseAddr(host); err == nil {
		addrs = []netip.Addr{a}
	} else {
		if addrs, err = r.LookupNetIP(ctx, "ip", host); err != nil {
			return nil, fmt.Errorf("gateway: resolving %s: %w", host, err)
		}
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("gateway: %s has no address", host)
	}
	for _, a := range addrs {
		if err := allow(a); err != nil {
			return nil, err
		}
	}
	return slices.Clip(addrs), nil
}

type pinnedKey struct{}

// pinned is the addresses a request was checked against: the dialer connects only to these, so a
// name that resolves elsewhere between the check and the connection (DNS rebinding) is never
// reached.
type pinned struct {
	addrs []netip.Addr
	port  string
}

// dialPinned dials one of the request's checked addresses, never a fresh lookup.
func dialPinned(allow AddressPolicy) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, _ string) (net.Conn, error) {
		p, ok := ctx.Value(pinnedKey{}).(pinned)
		if !ok || len(p.addrs) == 0 {
			return nil, errors.New("gateway: a connection without checked addresses")
		}
		var d net.Dialer
		var errs []error
		for _, a := range p.addrs {
			if err := allow(a); err != nil { // checked again at the last moment
				return nil, err
			}
			c, err := d.DialContext(ctx, network, net.JoinHostPort(a.String(), p.port))
			if err == nil {
				return c, nil
			}
			errs = append(errs, err)
		}
		return nil, errors.Join(errs...)
	}
}
