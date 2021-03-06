// Copyright 2016 The Prometheus Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"fmt"
	"net"
	"net/http"
	"regexp"

	"github.com/miekg/dns"
	"github.com/prometheus/common/log"
)

// validRRs checks a slice of RRs received from the server against a DNSRRValidator.
func validRRs(rrs *[]dns.RR, v *DNSRRValidator) bool {
	// Fail the probe if there are no RRs of a given type, but a regexp match is required
	// (i.e. FailIfNotMatchesRegexp is set).
	if len(*rrs) == 0 && len(v.FailIfNotMatchesRegexp) > 0 {
		return false
	}
	for _, rr := range *rrs {
		log.Debugf("Validating RR: %q", rr)
		for _, re := range v.FailIfMatchesRegexp {
			match, err := regexp.MatchString(re, rr.String())
			if err != nil {
				log.Errorf("Error matching regexp %q: %s", re, err)
				return false
			}
			if match {
				return false
			}
		}
		for _, re := range v.FailIfNotMatchesRegexp {
			match, err := regexp.MatchString(re, rr.String())
			if err != nil {
				log.Errorf("Error matching regexp %q: %s", re, err)
				return false
			}
			if !match {
				return false
			}
		}
	}
	return true
}

// validRcode checks rcode in the response against a list of valid rcodes.
func validRcode(rcode int, valid []string) bool {
	var validRcodes []int
	// If no list of valid rcodes is specified, only NOERROR is considered valid.
	if valid == nil {
		validRcodes = append(validRcodes, dns.StringToRcode["NOERROR"])
	} else {
		for _, rcode := range valid {
			rc, ok := dns.StringToRcode[rcode]
			if !ok {
				log.Errorf("Invalid rcode %v. Existing rcodes: %v", rcode, dns.RcodeToString)
				return false
			}
			validRcodes = append(validRcodes, rc)
		}
	}
	for _, rc := range validRcodes {
		if rcode == rc {
			return true
		}
	}
	log.Debugf("%s (%d) is not one of the valid rcodes (%v)", dns.RcodeToString[rcode], rcode, validRcodes)
	return false
}

func probeDNS(target string, w http.ResponseWriter, module Module) bool {
	var numAnswer, numAuthority, numAdditional int
	var dialProtocol, fallbackProtocol string
	defer func() {
		// These metrics can be used to build additional alerting based on the number of replies.
		// They should be returned even in case of errors.
		fmt.Fprintf(w, "probe_dns_answer_rrs %d\n", numAnswer)
		fmt.Fprintf(w, "probe_dns_authority_rrs %d\n", numAuthority)
		fmt.Fprintf(w, "probe_dns_additional_rrs %d\n", numAdditional)
	}()

	if module.DNS.Protocol == "" {
		module.DNS.Protocol = "udp"
	}

	if (module.DNS.Protocol == "tcp" || module.DNS.Protocol == "udp") && module.DNS.PreferredIpProtocol == "" {
		module.DNS.PreferredIpProtocol = "ip6"
	}
	if module.DNS.PreferredIpProtocol == "ip6" {
		fallbackProtocol = "ip4"
	} else {
		fallbackProtocol = "ip6"
	}

	dialProtocol = module.DNS.Protocol
	if module.DNS.Protocol == "udp" || module.DNS.Protocol == "tcp" {
		target_address, _, _ := net.SplitHostPort(target)
		ip, err := net.ResolveIPAddr(module.DNS.PreferredIpProtocol, target_address)
		if err != nil {
			ip, err = net.ResolveIPAddr(fallbackProtocol, target_address)
			if err != nil {
				return false
			}
		}

		if ip.IP.To4() == nil {
			dialProtocol = module.DNS.Protocol + "6"
		} else {
			dialProtocol = module.DNS.Protocol + "4"
		}
	}

	if dialProtocol[len(dialProtocol)-1] == '6' {
		fmt.Fprintf(w, "probe_ip_protocol 6\n")
	} else {
		fmt.Fprintf(w, "probe_ip_protocol 4\n")
	}

	client := new(dns.Client)
	client.Net = dialProtocol
	client.Timeout = module.Timeout

	qt := dns.TypeANY
	if module.DNS.QueryType != "" {
		var ok bool
		qt, ok = dns.StringToType[module.DNS.QueryType]
		if !ok {
			log.Errorf("Invalid type %v. Existing types: %v", module.DNS.QueryType, dns.TypeToString)
			return false
		}
	}

	msg := new(dns.Msg)
	msg.SetQuestion(dns.Fqdn(module.DNS.QueryName), qt)

	response, _, err := client.Exchange(msg, target)
	if err != nil {
		log.Warnf("Error while sending a DNS query: %s", err)
		return false
	}
	log.Debugf("Got response: %#v", response)

	numAnswer, numAuthority, numAdditional = len(response.Answer), len(response.Ns), len(response.Extra)

	if !validRcode(response.Rcode, module.DNS.ValidRcodes) {
		return false
	}
	if !validRRs(&response.Answer, &module.DNS.ValidateAnswer) {
		log.Debugf("Answer RRs validation failed")
		return false
	}
	if !validRRs(&response.Ns, &module.DNS.ValidateAuthority) {
		log.Debugf("Authority RRs validation failed")
		return false
	}
	if !validRRs(&response.Extra, &module.DNS.ValidateAdditional) {
		log.Debugf("Additional RRs validation failed")
		return false
	}
	return true
}
