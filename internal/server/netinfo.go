package server

import (
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
)

var proxyRange = netip.MustParsePrefix("198.18.0.0/15")

// 虚拟机、容器、代理软件创建的网卡名字里常带这些关键字，它们的地址局域网设备访问不到。
var virtualIfaceKeywords = []string{
	"vmware", "virtualbox", "vbox", "vethernet", "hyper-v", "docker",
	"wsl", "virtual", "tap", "tun", "tailscale", "zerotier", "clash", "heysocks",
}

func isVirtualIface(name string) bool {
	n := strings.ToLower(name)
	return slices.ContainsFunc(virtualIfaceKeywords, func(k string) bool {
		return strings.Contains(n, k)
	})
}

// LANAddrs 返回本机可用于局域网访问的 IPv4 地址（排除回环、链路本地地址和虚拟网卡）。
// 只找到虚拟网卡地址时退回全部结果，避免过滤过度导致没有可分享的地址。
func LANAddrs() []string {
	var all, physical []string
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipNet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip, ok := netip.AddrFromSlice(ipNet.IP)
			if !ok {
				continue
			}
			ip = ip.Unmap()
			// 198.18.0.0/15 常被代理软件的 TUN 网卡占用，局域网设备无法访问
			if !ip.Is4() || ip.IsLinkLocalUnicast() || proxyRange.Contains(ip) {
				continue
			}
			all = appendUnique(all, ip.String())
			if !isVirtualIface(iface.Name) {
				physical = appendUnique(physical, ip.String())
			}
		}
	}
	if len(physical) > 0 {
		return physical
	}
	return all
}

func appendUnique(list []string, s string) []string {
	if slices.Contains(list, s) {
		return list
	}
	return append(list, s)
}

// localIPs 返回本机所有网卡上的 IP（含 IPv6 和回环）。
func localIPs() []netip.Addr {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var out []netip.Addr
	for _, a := range addrs {
		if ipNet, ok := a.(*net.IPNet); ok {
			if ip, ok := netip.AddrFromSlice(ipNet.IP); ok {
				out = append(out, ip.Unmap())
			}
		}
	}
	return out
}

// isLocalRequest 判断请求是否来自运行服务的本机：对端地址是回环地址或本机某个网卡的地址。
// 只看 TCP 连接的对端地址，不信任 X-Forwarded-For 等可伪造的请求头。
func isLocalRequest(r *http.Request) bool {
	ap, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := ap.Addr().Unmap()
	return ip.IsLoopback() || slices.Contains(localIPs(), ip)
}

// netInfo 返回局域网地址和端口，前端生成分享二维码时使用（通过 localhost 访问时无法从地址栏得知局域网 IP）。
func (s *Server) netInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ips": LANAddrs(), "port": s.port})
}
