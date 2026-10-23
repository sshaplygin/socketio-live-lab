#!/bin/sh
# Per-source limits for the public TLS port, below Caddy and the server:
#   - at most 20 concurrent connections per IPv4 address (per /64 for IPv6);
#   - at most 30 new connections per minute per address, burst 30.
# Excess packets are dropped before a TLS handshake costs any CPU. Nothing else
# on the host is matched. Run as root: `firewall.sh up` (idempotent) or `down`.
#
# IPv4 reaches the container through Docker's DNAT, so the rule sits in
# DOCKER-USER and matches the original destination port. IPv6 is served by
# docker-proxy on the host, so its rule sits in INPUT.
set -eu

port=${LAB_TLS_PORT:-8443}
iface=${LAB_PUBLIC_IFACE:-$(ip route show default | awk '{ print $5; exit }')}
chain=LIVE-LAB-LIMIT

v4_jump() { iptables "$1" DOCKER-USER -i "$iface" -p tcp -m conntrack --ctstate NEW --ctorigdstport "$port" -j "$chain"; }
v6_jump() { ip6tables "$1" INPUT -i "$iface" -p tcp --dport "$port" -m conntrack --ctstate NEW -j "$chain"; }

fill() { # $1 = iptables or ip6tables, $2 = source mask
	"$1" -N "$chain" 2>/dev/null || "$1" -F "$chain"
	"$1" -A "$chain" -m connlimit --connlimit-above 20 --connlimit-mask "$2" -j DROP
	"$1" -A "$chain" -m hashlimit --hashlimit-name "live-lab-$1" --hashlimit-mode srcip \
		--hashlimit-srcmask "$2" --hashlimit-above 30/minute --hashlimit-burst 30 -j DROP
	"$1" -A "$chain" -j RETURN
}

case ${1:-} in
up)
	[ -n "$iface" ] || { echo "no default-route interface; set LAB_PUBLIC_IFACE" >&2; exit 1; }
	iptables -N DOCKER-USER 2>/dev/null || true
	fill iptables 32
	v4_jump -C 2>/dev/null || v4_jump -I
	fill ip6tables 64
	v6_jump -C 2>/dev/null || v6_jump -I
	echo "live-lab limits on $iface:$port"
	;;
down)
	while v4_jump -C 2>/dev/null; do v4_jump -D; done
	while v6_jump -C 2>/dev/null; do v6_jump -D; done
	for t in iptables ip6tables; do
		"$t" -F "$chain" 2>/dev/null || true
		"$t" -X "$chain" 2>/dev/null || true
	done
	echo "live-lab limits removed"
	;;
*)
	echo "usage: firewall.sh up|down" >&2
	exit 2
	;;
esac
