package smtpemail

import (
	"context"
	"fmt"
	"net"
	"net/mail"
	"strings"
)

type smtpEndpoint struct {
	Host     string
	Port     int
	Security string
}

var knownSMTPDomains = map[string]smtpEndpoint{
	"gmail.com":      {Host: "smtp.gmail.com", Port: 587, Security: "starttls"},
	"googlemail.com": {Host: "smtp.gmail.com", Port: 587, Security: "starttls"},
	"outlook.com":    {Host: "smtp.office365.com", Port: 587, Security: "starttls"},
	"hotmail.com":    {Host: "smtp.office365.com", Port: 587, Security: "starttls"},
	"live.com":       {Host: "smtp.office365.com", Port: 587, Security: "starttls"},
	"msn.com":        {Host: "smtp.office365.com", Port: 587, Security: "starttls"},
	"qq.com":         {Host: "smtp.qq.com", Port: 465, Security: "ssl"},
	"foxmail.com":    {Host: "smtp.qq.com", Port: 465, Security: "ssl"},
	"163.com":        {Host: "smtp.163.com", Port: 465, Security: "ssl"},
	"126.com":        {Host: "smtp.126.com", Port: 465, Security: "ssl"},
	"yeah.net":       {Host: "smtp.yeah.net", Port: 465, Security: "ssl"},
	"icloud.com":     {Host: "smtp.mail.me.com", Port: 587, Security: "starttls"},
	"me.com":         {Host: "smtp.mail.me.com", Port: 587, Security: "starttls"},
	"mac.com":        {Host: "smtp.mail.me.com", Port: 587, Security: "starttls"},
	"yahoo.com":      {Host: "smtp.mail.yahoo.com", Port: 465, Security: "ssl"},
	"zoho.com":       {Host: "smtp.zoho.com", Port: 587, Security: "starttls"},
}

func discoverSMTPEndpoints(ctx context.Context, email string) ([]smtpEndpoint, error) {
	address, err := mail.ParseAddress(email)
	if err != nil {
		return nil, fmt.Errorf("a valid email is required")
	}
	at := strings.LastIndex(address.Address, "@")
	if at < 0 || at == len(address.Address)-1 {
		return nil, fmt.Errorf("a valid email is required")
	}
	domain := strings.ToLower(address.Address[at+1:])
	if endpoint, ok := knownSMTPDomains[domain]; ok {
		return []smtpEndpoint{endpoint}, nil
	}

	var endpoints []smtpEndpoint
	mxRecords, _ := net.DefaultResolver.LookupMX(ctx, domain)
	for _, record := range mxRecords {
		if endpoint, ok := endpointForMXHost(record.Host); ok {
			endpoints = appendEndpoint(endpoints, endpoint)
		}
	}
	if len(endpoints) > 0 {
		return endpoints, nil
	}

	for _, service := range []struct {
		name     string
		security string
	}{{"submission", "starttls"}, {"submissions", "ssl"}} {
		_, records, _ := net.DefaultResolver.LookupSRV(ctx, service.name, "tcp", domain)
		for _, record := range records {
			port := int(record.Port)
			if port != 465 && port != 587 {
				continue
			}
			endpoints = appendEndpoint(endpoints, smtpEndpoint{
				Host: strings.TrimSuffix(record.Target, "."), Port: port, Security: service.security,
			})
		}
	}
	if len(endpoints) > 0 {
		return endpoints, nil
	}

	// Most private mail systems publish smtp.<domain> even when they do not
	// publish the optional RFC 6186 SRV records. Connection validation still
	// verifies public DNS, TLS, and the supplied credentials before saving.
	host := "smtp." + domain
	if ips, lookupErr := net.DefaultResolver.LookupIP(ctx, "ip", host); lookupErr == nil {
		for _, ip := range ips {
			if isPublicIP(ip) {
				return []smtpEndpoint{
					{Host: host, Port: 587, Security: "starttls"},
					{Host: host, Port: 465, Security: "ssl"},
				}, nil
			}
		}
	}
	return nil, fmt.Errorf("this email provider cannot be configured automatically")
}

func endpointForMXHost(host string) (smtpEndpoint, bool) {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	switch {
	case strings.Contains(host, "google.com"), strings.Contains(host, "googlemail.com"):
		return smtpEndpoint{Host: "smtp.gmail.com", Port: 587, Security: "starttls"}, true
	case strings.Contains(host, "outlook.com"):
		return smtpEndpoint{Host: "smtp.office365.com", Port: 587, Security: "starttls"}, true
	case strings.Contains(host, "qq.com"):
		return smtpEndpoint{Host: "smtp.exmail.qq.com", Port: 465, Security: "ssl"}, true
	case strings.Contains(host, "mxhichina.com"):
		return smtpEndpoint{Host: "smtp.qiye.aliyun.com", Port: 465, Security: "ssl"}, true
	case strings.Contains(host, "feishu.cn"):
		return smtpEndpoint{Host: "smtp.feishu.cn", Port: 465, Security: "ssl"}, true
	case strings.Contains(host, "zoho.eu"):
		return smtpEndpoint{Host: "smtp.zoho.eu", Port: 587, Security: "starttls"}, true
	case strings.Contains(host, "zoho.in"):
		return smtpEndpoint{Host: "smtp.zoho.in", Port: 587, Security: "starttls"}, true
	case strings.Contains(host, "zoho"):
		return smtpEndpoint{Host: "smtp.zoho.com", Port: 587, Security: "starttls"}, true
	case strings.Contains(host, "yahoodns.net"):
		return smtpEndpoint{Host: "smtp.mail.yahoo.com", Port: 465, Security: "ssl"}, true
	default:
		return smtpEndpoint{}, false
	}
}

func appendEndpoint(endpoints []smtpEndpoint, endpoint smtpEndpoint) []smtpEndpoint {
	for _, existing := range endpoints {
		if existing == endpoint {
			return endpoints
		}
	}
	return append(endpoints, endpoint)
}
