package buddy

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Buddy firmware's metric strings hold 47 bytes. Leave room for the marker,
// event, and an eight-digit layer number without silently truncating routing.
var printerIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,22}$`)

type Printer struct {
	ID       string `yaml:"id" json:"id"`
	Name     string `yaml:"name" json:"name"`
	Stream   string `yaml:"stream" json:"stream"`
	SourceIP string `yaml:"source_ip" json:"source_ip,omitempty"`
}

type Config struct {
	HTTP struct {
		Address string `yaml:"address"`
	} `yaml:"http"`
	Metrics struct {
		Address        string `yaml:"address"`
		AdvertisedHost string `yaml:"advertised_host"`
	} `yaml:"metrics"`
	Go2RTC struct {
		URL string `yaml:"url"`
	} `yaml:"go2rtc"`
	Capture struct {
		Timeout  time.Duration `yaml:"timeout"`
		Attempts int           `yaml:"attempts"`
	} `yaml:"capture"`
	Video struct {
		Workers                int `yaml:"workers"`
		DefaultDurationSeconds int `yaml:"default_duration_seconds"`
	} `yaml:"video"`
	Printers     []Printer `yaml:"printers"`
	DataDir      string    `yaml:"data_dir"`
	AuthUser     string    `yaml:"-"`
	AuthPassword string    `yaml:"-"`
}

func LoadConfig(path string) (Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return Config{}, err
	}
	defer f.Close()
	c, err := DecodeConfig(f)
	if err != nil {
		return c, err
	}
	c.AuthUser, c.AuthPassword = os.Getenv("SNAPSHOT_BUDDY_AUTH_USER"), os.Getenv("SNAPSHOT_BUDDY_AUTH_PASSWORD")
	if (c.AuthUser == "") != (c.AuthPassword == "") {
		return c, errors.New("both authentication environment variables must be set together")
	}
	if v := os.Getenv("SNAPSHOT_BUDDY_DATA_DIR"); v != "" {
		c.DataDir = v
	}
	return c, nil
}

func DecodeConfig(r io.Reader) (Config, error) {
	var c Config
	c.HTTP.Address, c.Metrics.Address = ":8080", ":8514"
	c.Capture.Timeout, c.Capture.Attempts = 5*time.Second, 2
	c.Video.Workers, c.Video.DefaultDurationSeconds = 1, 10
	c.DataDir = "/data"
	d := yaml.NewDecoder(r)
	d.KnownFields(true)
	if err := d.Decode(&c); err != nil {
		return c, err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return c, errors.New("configuration must contain one YAML document")
	}
	for _, addr := range []string{c.HTTP.Address, c.Metrics.Address} {
		_, port, err := net.SplitHostPort(addr)
		if err != nil {
			return c, fmt.Errorf("invalid listen address %q: %w", addr, err)
		}
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return c, fmt.Errorf("invalid listen port %q", port)
		}
	}
	if _, err := netip.ParseAddr(c.Metrics.AdvertisedHost); err != nil {
		return c, errors.New("metrics.advertised_host must be the receiver's LAN IP address")
	}
	u, err := url.Parse(c.Go2RTC.URL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.RawQuery != "" || u.Fragment != "" {
		return c, errors.New("go2rtc.url must be an HTTP(S) base URL without query or fragment")
	}
	if c.Capture.Timeout < 100*time.Millisecond || c.Capture.Timeout > time.Minute || c.Capture.Attempts < 1 || c.Capture.Attempts > 5 {
		return c, errors.New("capture timeout must be 100ms–1m and attempts 1–5")
	}
	if c.Video.Workers < 1 || c.Video.Workers > 4 || c.Video.DefaultDurationSeconds < 1 || c.Video.DefaultDurationSeconds > 3600 {
		return c, errors.New("video workers must be 1–4 and default duration 1–3600 seconds")
	}
	if strings.TrimSpace(c.DataDir) == "" {
		return c, errors.New("data_dir must not be empty")
	}
	if len(c.Printers) == 0 {
		return c, errors.New("at least one printer is required")
	}
	seen := map[string]bool{}
	for i := range c.Printers {
		p := &c.Printers[i]
		if !printerIDPattern.MatchString(p.ID) || seen[p.ID] {
			return c, fmt.Errorf("invalid or repeated printer ID %q (use 1–23 lowercase letters, digits, underscores or hyphens)", p.ID)
		}
		seen[p.ID] = true
		if strings.TrimSpace(p.Name) == "" {
			p.Name = p.ID
		}
		if strings.TrimSpace(p.Stream) == "" || strings.ContainsAny(p.Stream, ":#?&/\\\r\n") {
			return c, fmt.Errorf("printer %s: stream must be a named go2rtc stream, not a URL", p.ID)
		}
		if p.SourceIP != "" {
			if _, err := netip.ParseAddr(p.SourceIP); err != nil {
				return c, fmt.Errorf("printer %s: invalid source_ip", p.ID)
			}
		}
	}
	return c, nil
}
