package buddy

import (
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const markerPrefix = "M118 SNAPSHOT_BUDDY_V1 "
const MaxMarkerBytes = 1024

var metricPattern = regexp.MustCompile(`(?:^|\s)gcode(?:,[^\s]+)?\s+v="((?:\\.|[^"\\])*)"`)

type Event struct {
	Kind       string    `json:"kind"`
	PrinterID  string    `json:"printer_id"`
	Name       string    `json:"name,omitempty"`
	Layer      int       `json:"layer"`
	SourceIP   string    `json:"source_ip"`
	ReceivedAt time.Time `json:"received_at"`
	Raw        string    `json:"raw"`
}

// ParsePacket accepts actual Prusa gcode metrics (including syslog envelopes),
// plus bare markers for diagnostics. Unrelated metrics are ignored.
func ParsePacket(data []byte, source string, at time.Time) []Event {
	var result []Event
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		command := line
		if !strings.HasPrefix(command, markerPrefix) {
			m := metricPattern.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			var err error
			command, err = strconv.Unquote(`"` + m[1] + `"`)
			if err != nil {
				continue
			}
		}
		if e, err := ParseMarker(command); err == nil {
			e.SourceIP, e.ReceivedAt = source, at.UTC()
			result = append(result, e)
		}
	}
	return result
}

func ParseMarker(command string) (Event, error) {
	e := Event{Layer: -1, Raw: command}
	if len(command) > MaxMarkerBytes || !utf8.ValidString(command) || !strings.HasPrefix(command, markerPrefix) || strings.IndexFunc(command, unicode.IsControl) >= 0 {
		return e, fmt.Errorf("invalid marker")
	}
	parts := strings.SplitN(strings.TrimPrefix(command, markerPrefix), " ", 3)
	if len(parts) != 3 || !printerIDPattern.MatchString(parts[1]) {
		return e, fmt.Errorf("expected event, printer ID and payload")
	}
	e.Kind, e.PrinterID = parts[0], parts[1]
	switch e.Kind {
	case "START":
		e.Name = strings.TrimSpace(parts[2])
		if e.Name == "" {
			return e, fmt.Errorf("empty print name")
		}
	case "FRAME", "STOP":
		n, err := strconv.Atoi(parts[2])
		if err != nil || n < 0 || n > 10000000 {
			return e, fmt.Errorf("invalid layer")
		}
		e.Layer = n
	default:
		return e, fmt.Errorf("unknown event")
	}
	return e, nil
}

type Snippets struct{ Start, Frame, Stop string }

func GCode(c Config, p Printer) Snippets {
	_, port, _ := net.SplitHostPort(c.Metrics.Address)
	block := func(command string, wait bool) string {
		prefix := ""
		if wait {
			prefix = "M400\n"
		}
		return prefix + "M331 gcode\n" + command + "\nG4 P100\n" + command + "\nM332 gcode"
	}
	labeled := func(purpose, commands string) string {
		label := "Snapshot Buddy: " + purpose + " (" + p.ID + ")"
		return "; BEGIN " + label + "\n" + commands + "\n; END " + label
	}
	return Snippets{
		Start: labeled("start session", fmt.Sprintf("M334 %s %s 13514\n", c.Metrics.AdvertisedHost, port)+block(markerPrefix+"START "+p.ID+" {input_filename_base}", false)),
		Frame: labeled("layer snapshot", block(markerPrefix+"FRAME "+p.ID+" {layer_num}", true)),
		Stop:  labeled("final snapshot and close session", block(markerPrefix+"STOP "+p.ID+" {total_layer_count}", true)),
	}
}
