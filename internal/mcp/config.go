package mcp

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const DefaultTimeout = 30 * time.Second

type Config struct {
	Servers map[string]ServerConfig `json:"mcpServers"`
}

type ServerConfig struct {
	Transport string            `json:"transport"`
	Command   string            `json:"command,omitempty"`
	Args      []string          `json:"args,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	Directory string            `json:"cwd,omitempty"`
	URL       string            `json:"url,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	Timeout   string            `json:"timeout,omitempty"`
	Tools     []string          `json:"tools,omitempty"` // nil: all; []: none
	Disabled  bool              `json:"disabled,omitempty"`
}

var serverNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
var variablePattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// LoadConfig expands ${ENV_VAR} in string values after decoding JSON, so quotes
// in credentials cannot change the document structure. Missing variables fail closed.
// Relative cwd values are resolved against the MCP configuration file's directory.
func LoadConfig(path string) (Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("open MCP config: %w", err)
	}
	defer file.Close()
	var cfg Config
	decoder := json.NewDecoder(io.LimitReader(file, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return cfg, fmt.Errorf("decode MCP config: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return cfg, fmt.Errorf("MCP config must contain one JSON object")
	}
	if cfg.Servers == nil {
		return cfg, fmt.Errorf("MCP config requires mcpServers")
	}
	for name, server := range cfg.Servers {
		if server.Disabled {
			continue
		}
		var expandErr error
		expand := func(value string) string {
			return variablePattern.ReplaceAllStringFunc(value, func(match string) string {
				key := match[2 : len(match)-1]
				value, exists := os.LookupEnv(key)
				if !exists {
					expandErr = fmt.Errorf("server %s: missing environment variable %s", name, key)
				}
				return value
			})
		}
		server.Command, server.Directory, server.URL = expand(server.Command), expand(server.Directory), expand(server.URL)
		for i, arg := range server.Args {
			server.Args[i] = expand(arg)
		}
		for key, value := range server.Env {
			server.Env[key] = expand(value)
		}
		for key, value := range server.Headers {
			server.Headers[key] = expand(value)
		}
		if expandErr != nil {
			return cfg, expandErr
		}
		if server.Directory != "" && !filepath.IsAbs(server.Directory) {
			server.Directory = filepath.Join(filepath.Dir(path), server.Directory)
		}
		if err := server.validate(name); err != nil {
			return cfg, err
		}
		cfg.Servers[name] = server
	}
	return cfg, nil
}

func (c ServerConfig) requestTimeout() time.Duration {
	if c.Timeout == "" {
		return DefaultTimeout
	}
	timeout, _ := time.ParseDuration(c.Timeout)
	return timeout
}

func (c ServerConfig) validate(name string) error {
	if !serverNamePattern.MatchString(name) {
		return fmt.Errorf("invalid MCP server name %q", name)
	}
	if c.Timeout != "" {
		duration, err := time.ParseDuration(c.Timeout)
		if err != nil || duration <= 0 {
			return fmt.Errorf("server %s: timeout must be a positive duration", name)
		}
	}
	switch c.Transport {
	case "stdio":
		if strings.TrimSpace(c.Command) == "" || c.URL != "" {
			return fmt.Errorf("server %s: stdio requires command and no URL", name)
		}
	case "streamable_http":
		endpoint, err := url.Parse(c.URL)
		if err != nil || endpoint.Host == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.User != nil || endpoint.Fragment != "" {
			return fmt.Errorf("server %s: invalid HTTP endpoint", name)
		}
		if c.Command != "" {
			return fmt.Errorf("server %s: HTTP transport does not accept command", name)
		}
	default:
		return fmt.Errorf("server %s: transport must be stdio or streamable_http", name)
	}
	for key, value := range c.Headers {
		canonical := http.CanonicalHeaderKey(key)
		if canonical == "Mcp-Session-Id" || canonical == "Mcp-Protocol-Version" || canonical == "Content-Type" || canonical == "Accept" || canonical == "Host" || canonical == "Content-Length" {
			return fmt.Errorf("server %s: reserved HTTP header %s", name, key)
		}
		if strings.ContainsAny(key+value, "\r\n") {
			return fmt.Errorf("server %s: invalid HTTP header", name)
		}
	}
	return nil
}
