package conf

import (
	"encoding/json"
	"sort"
	"strings"

	"v2ray.com/core/app/dns"
	"v2ray.com/core/app/router"
	"v2ray.com/core/common/net"
)

type NameServerConfig struct {
	Address   *Address
	Port      uint16
	Domains   []string
	ExpectIPs StringList
}

func (c *NameServerConfig) UnmarshalJSON(data []byte) error {
	var address Address
	if err := json.Unmarshal(data, &address); err == nil {
		c.Address = &address
		return nil
	}

	var advanced struct {
		Address   *Address   `json:"address"`
		Port      uint16     `json:"port"`
		Domains   []string   `json:"domains"`
		ExpectIPs StringList `json:"expectIps"`
	}
	if err := json.Unmarshal(data, &advanced); err == nil {
		c.Address = advanced.Address
		c.Port = advanced.Port
		c.Domains = advanced.Domains
		c.ExpectIPs = advanced.ExpectIPs
		return nil
	}

	return newError("failed to parse name server: ", string(data))
}

func toDomainMatchingType(t router.Domain_Type) dns.DomainMatchingType {
	switch t {
	case router.Domain_Domain:
		return dns.DomainMatchingType_Subdomain
	case router.Domain_Full:
		return dns.DomainMatchingType_Full
	case router.Domain_Plain:
		return dns.DomainMatchingType_Keyword
	case router.Domain_Regex:
		return dns.DomainMatchingType_Regex
	default:
		panic("unknown domain type")
	}
}

func (c *NameServerConfig) Build() (*dns.NameServer, error) {
	if c.Address == nil {
		return nil, newError("NameServer address is not specified.")
	}

	var domains []*dns.NameServer_PriorityDomain

	for _, d := range c.Domains {
		parsedDomain, err := parseDomainRule(d)
		if err != nil {
			return nil, newError("invalid domain rule: ", d).Base(err)
		}

		for _, pd := range parsedDomain {
			domains = append(domains, &dns.NameServer_PriorityDomain{
				Type:   toDomainMatchingType(pd.Type),
				Domain: pd.Value,
			})
		}
	}

	geoipList, err := toCidrList(c.ExpectIPs)
	if err != nil {
		return nil, newError("invalid ip rule: ", c.ExpectIPs).Base(err)
	}

	return &dns.NameServer{
		Address: &net.Endpoint{
			Network: net.Network_UDP,
			Address: c.Address.Build(),
			Port:    uint32(c.Port),
		},
		PrioritizedDomain: domains,
		Geoip:             geoipList,
	}, nil
}

var typeMap = map[router.Domain_Type]dns.DomainMatchingType{
	router.Domain_Full:   dns.DomainMatchingType_Full,
	router.Domain_Domain: dns.DomainMatchingType_Subdomain,
	router.Domain_Plain:  dns.DomainMatchingType_Keyword,
	router.Domain_Regex:  dns.DomainMatchingType_Regex,
}

// DnsConfig is a JSON serializable object for dns.Config.
type DnsConfig struct {
	Servers  []*NameServerConfig `json:"servers"`
	Hosts    map[string]*Address `json:"hosts"`
	ClientIP *Address            `json:"clientIp"`
	Tag      string              `json:"tag"`
	UseFake  StringList          `json:"useFake"`
}

func getHostMapping(addr *Address) *dns.Config_HostMapping {
	if addr.Family().IsIP() {
		return &dns.Config_HostMapping{
			Ip: [][]byte{[]byte(addr.IP())},
		}
	} else {
		return &dns.Config_HostMapping{
			ProxiedDomain: addr.Domain(),
		}
	}
}

var prefixMapper = map[string]string{
	"domain:":  "d",
	"regexp:":  "r",
	"keyword:": "k",
	"full:":    "f",
	"geosite:": "egeosite.dat:",
	"ext:":     "e",
	"geoip:":   "i",
}
var typeMapper = map[router.Domain_Type]string{
	router.Domain_Full:   "f",
	router.Domain_Domain: "d",
	router.Domain_Plain:  "k",
	router.Domain_Regex:  "r",
}

func compressPattern(pattern string) string {
	for prefix, cmd := range prefixMapper {
		if strings.HasPrefix(pattern, prefix) {
			return cmd + pattern[len(prefix):len(pattern)]
		}
	}
	return "f" + pattern // If no prefix, use full match by default
}
func getHostPattern(addr *Address, pattern string) (ret *dns.Config_HostMapping) {
	ret = new(dns.Config_HostMapping)
	if addr.Family().IsIP() {
		ret.Ip = [][]byte{[]byte(addr.IP())}
	} else {
		ret.ProxiedDomain = addr.Domain()
	}
	ret.Pattern = compressPattern(pattern)
	return
}

// Build implements Buildable
func (c *DnsConfig) Build() (*dns.Config, error) {
	config := &dns.Config{
		Tag: c.Tag,
	}

	if c.ClientIP != nil {
		if !c.ClientIP.Family().IsIP() {
			return nil, newError("not an IP address:", c.ClientIP.String())
		}
		config.ClientIp = []byte(c.ClientIP.IP())
	}

	for _, server := range c.Servers {
		ns, err := server.Build()
		if err != nil {
			return nil, newError("failed to build name server").Base(err)
		}
		config.NameServer = append(config.NameServer, ns)
	}

	if c.Hosts != nil && len(c.Hosts) > 0 {
		domains := make([]string, 0, len(c.Hosts))
		config.ExternalRules = make(map[string]*dns.ConfigPatterns)
		for pattern, address := range c.Hosts {
			domains = append(domains, pattern)
			new_rule := getHostPattern(address, pattern)
			config.HostRules = append(config.HostRules, new_rule)
			cmd := new_rule.Pattern[0]
			arg := new_rule.Pattern[1:]
			var filename, country string
			if cmd == 'e' {
				kv := strings.Split(arg, ":")
				if len(kv) != 2 {
					return nil, newError("invalid external resource: ", arg)
				}
				filename, country = kv[0], kv[1]
			}
			domains, err := loadGeositeWithAttr(filename, country)
			if err != nil {
				return nil, newError("invalid external settings from ", filename, ": ", arg).Base(err)
			}
			extern_rules := &dns.ConfigPatterns{
				Patterns: make([]string, len(domains)),
			}
			index := 0
			for _, d := range domains {
				extern_rules.Patterns[index] = typeMapper[d.Type] + d.Value
				index++
			}
			config.ExternalRules[arg] = extern_rules
		}
		sort.Strings(domains)
		for _, domain := range domains {
			addr := c.Hosts[domain]
			var mappings []*dns.Config_HostMapping
			if strings.HasPrefix(domain, "domain:") {
				mapping := getHostMapping(addr)
				mapping.Type = dns.DomainMatchingType_Subdomain
				mapping.Pattern = domain[7:]

				mappings = append(mappings, mapping)
			} else if strings.HasPrefix(domain, "geosite:") {
				domains, err := loadGeositeWithAttr("geosite.dat", strings.ToUpper(domain[8:]))
				if err != nil {
					return nil, newError("invalid geosite settings: ", domain).Base(err)
				}
				for _, d := range domains {
					mapping := getHostMapping(addr)
					mapping.Type = typeMap[d.Type]
					mapping.Pattern = d.Value

					mappings = append(mappings, mapping)
				}
			} else if strings.HasPrefix(domain, "regexp:") {
				mapping := getHostMapping(addr)
				mapping.Type = dns.DomainMatchingType_Regex
				mapping.Pattern = domain[7:]

				mappings = append(mappings, mapping)
			} else if strings.HasPrefix(domain, "keyword:") {
				mapping := getHostMapping(addr)
				mapping.Type = dns.DomainMatchingType_Keyword
				mapping.Pattern = domain[8:]

				mappings = append(mappings, mapping)
			} else if strings.HasPrefix(domain, "full:") {
				mapping := getHostMapping(addr)
				mapping.Type = dns.DomainMatchingType_Full
				mapping.Pattern = domain[5:]

				mappings = append(mappings, mapping)
			} else if strings.HasPrefix(domain, "ext:") {
				kv := strings.Split(domain[4:], ":")
				if len(kv) != 2 {
					return nil, newError("invalid external resource: ", domain)
				}
				filename := kv[0]
				country := kv[1]
				domains, err := loadGeositeWithAttr(filename, country)
				if err != nil {
					return nil, newError("failed to load domains: ", country, " from ", filename).Base(err)
				}
				for _, d := range domains {
					mapping := getHostMapping(addr)
					mapping.Type = typeMap[d.Type]
					mapping.Pattern = d.Value

					mappings = append(mappings, mapping)
				}
			} else {
				mapping := getHostMapping(addr)
				mapping.Type = dns.DomainMatchingType_Full
				mapping.Pattern = domain

				mappings = append(mappings, mapping)
			}

			config.StaticHosts = append(config.StaticHosts, mappings...)
		}
	}

	config.UseFake = (c.UseFake != nil)

	return config, nil
}
