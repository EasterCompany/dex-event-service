package config

// ServerMapConfig represents the structure of server-map.json
type ServerMapConfig struct {
	Servers map[string]Server `json:"servers"`
}

// Server represents a single server in the server map
type Server struct {
	User        string   `json:"user"`
	Key         string   `json:"key"`
	PublicIPV4  string   `json:"public_ipv4"`
	PrivateIPV4 string   `json:"private_ipv4"`
	PublicIPV6  string   `json:"public_ipv6"`
	Services    []string `json:"services,omitempty"`
}
