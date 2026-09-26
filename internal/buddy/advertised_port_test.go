package buddy

import (
	"strings"
	"testing"
)

func TestAdvertisedMetricsPort(t *testing.T) {
	raw := `metrics:
  address: ":18514"
  advertised_host: "192.168.1.50"
  advertised_port: 8514
go2rtc:
  url: http://go2rtc:1984
printers:
  - id: c1
    stream: camera
`
	c, err := DecodeConfig(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(GCode(c, c.Printers[0]).Start, "M334 192.168.1.50 8514 13514") {
		t.Fatal("snippet did not use relay port")
	}
	c, err = DecodeConfig(strings.NewReader(strings.Replace(raw, "  advertised_port: 8514\n", "", 1)))
	if err != nil || c.Metrics.AdvertisedPort != 18514 {
		t.Fatalf("default: %+v %v", c, err)
	}
	if _, err = DecodeConfig(strings.NewReader(strings.Replace(raw, "advertised_port: 8514", "advertised_port: 65536", 1))); err == nil {
		t.Fatal("accepted invalid port")
	}
}
