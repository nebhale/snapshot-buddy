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

const markerPrefix = "M118 SB1 "
const legacyMarkerPrefix = "M118 SNAPSHOT_BUDDY_V1 "
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
		if _, _, ok := markerPayload(command); !ok {
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

func markerPayload(command string) (payload string, legacy bool, ok bool) {
	if strings.HasPrefix(command, markerPrefix) {
		return strings.TrimPrefix(command, markerPrefix), false, true
	}
	if strings.HasPrefix(command, legacyMarkerPrefix) {
		return strings.TrimPrefix(command, legacyMarkerPrefix), true, true
	}
	return "", false, false
}

func ParseMarker(command string) (Event, error) {
	e := Event{Layer: -1, Raw: command}
	payload, legacy, ok := markerPayload(command)
	if len(command) > MaxMarkerBytes || !utf8.ValidString(command) || !ok || strings.IndexFunc(command, unicode.IsControl) >= 0 {
		return e, fmt.Errorf("invalid marker")
	}
	parts := strings.SplitN(payload, " ", 3)
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
	case "FRAME":
		// Already-sliced files can retain the original marker indefinitely.
		if !legacy {
			return e, fmt.Errorf("unknown event")
		}
		e.Kind = "LAYER"
		fallthrough
	case "LAYER":
		if legacy && parts[0] != "FRAME" {
			return e, fmt.Errorf("unknown event")
		}
		fallthrough
	case "STOP":
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

type Snippets struct{ Start, Layer, Stop string }

func GCode(c Config, p Printer) Snippets {
	_, port, _ := net.SplitHostPort(c.Metrics.Address)
	if c.Metrics.AdvertisedPort != 0 {
		port = strconv.Itoa(c.Metrics.AdvertisedPort)
	}
	block := func(command string, wait bool, copies, delayMS int) string {
		prefix := ""
		if wait {
			prefix = "M400\n"
		}
		retry := fmt.Sprintf("\nG4 P%d\n%s", delayMS, command)
		return prefix + "M331 gcode\n" + command + strings.Repeat(retry, copies-1) + "\nM332 gcode"
	}
	labeled := func(purpose, commands string) string {
		label := "Snapshot Buddy: " + purpose + " (" + p.ID + ")"
		return "; BEGIN " + label + "\n" + commands + "\n; END " + label
	}
	// ASCII makes the character limit equal the firmware's 47-byte limit.
	name := fmt.Sprintf("{if input_filename_base =~ /^[a-zA-Z0-9 _.-]{1,%d}$/}{input_filename_base}{else}Print{endif}", 47-len(markerPrefix+"START "+p.ID+" "))
	// Spread lifecycle retries beyond the firmware's one-second metrics
	// batching interval, while keeping the existing layer pacing unchanged.
	return Snippets{
		Start: labeled("start session", fmt.Sprintf("M334 %s %s 13514\n", c.Metrics.AdvertisedHost, port)+block(markerPrefix+"START "+p.ID+" "+name, false, 3, 1100)),
		Layer: labeled("layer snapshot", block(markerPrefix+"LAYER "+p.ID+" {layer_num}", true, 2, 100)),
		Stop:  labeled("final snapshot and close session", block(markerPrefix+"STOP "+p.ID+" {total_layer_count}", true, 3, 1100)),
	}
}
