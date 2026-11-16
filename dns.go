package main

import (
	"net"
	"strings"

	"github.com/miekg/dns"
)

// Handle 是每次 DNS 查询的入口，只做：查配置 → 读健康快照 → 权重选值 → 回包。
// 注意：这里绝不做任何健康探测，健康状态由后台 HealthChecker 维护。
func (m *Manager) Handle(w dns.ResponseWriter, r *dns.Msg) {
	st := m.current()

	msg := new(dns.Msg)
	msg.SetReply(r)
	msg.Authoritative = true
	msg.RecursionAvailable = false

	if len(r.Question) == 0 {
		msg.Rcode = dns.RcodeFormatError
		_ = w.WriteMsg(msg)
		return
	}

	q := r.Question[0]
	msg.Rcode = dns.RcodeNameError // 默认 NXDOMAIN

	rr, ok := st.records[strings.ToLower(q.Name)]
	if !ok {
		_ = w.WriteMsg(msg)
		return
	}

	msg.Rcode = dns.RcodeSuccess // 域名存在 → NOERROR
	rrs, ok := m.resolve(st, rr, q)
	if !ok {
		msg.Rcode = dns.RcodeServerFailure
		_ = w.WriteMsg(msg)
		return
	}
	msg.Answer = rrs
	_ = w.WriteMsg(msg)
}

// buildRRs 按记录类型构造应答 RR
func buildRRs(qname string, qtype uint16, recType, value string, ttl uint32) []dns.RR {
	hdr := dns.RR_Header{Name: qname, Class: dns.ClassINET, Ttl: ttl}

	switch recType {
	case "CNAME":
		hdr.Rrtype = dns.TypeCNAME
		return []dns.RR{&dns.CNAME{Hdr: hdr, Target: dns.Fqdn(value)}}

	case "A":
		if qtype != dns.TypeA && qtype != dns.TypeANY {
			return nil
		}
		ip := net.ParseIP(value)
		if ip == nil || ip.To4() == nil {
			return nil
		}
		hdr.Rrtype = dns.TypeA
		return []dns.RR{&dns.A{Hdr: hdr, A: ip.To4()}}

	case "AAAA":
		if qtype != dns.TypeAAAA && qtype != dns.TypeANY {
			return nil
		}
		ip := net.ParseIP(value)
		if ip == nil {
			return nil
		}
		hdr.Rrtype = dns.TypeAAAA
		return []dns.RR{&dns.AAAA{Hdr: hdr, AAAA: ip.To16()}}
	}
	return nil
}
