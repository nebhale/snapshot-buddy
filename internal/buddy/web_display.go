package buddy

import (
	"fmt"
	"strings"
)

func printerDisplayName(c Config, id string) string {
	for _, printer := range c.Printers {
		if printer.ID == id {
			name := strings.TrimSpace(printer.Name)
			if name == "" || name == id {
				return "Unnamed printer"
			}
			return name
		}
	}
	return "Unknown printer"
}

// Format received protocol data for display without changing the stored marker.
func markerDescription(c Config, raw string) string {
	event, err := ParseMarker(raw)
	if err != nil {
		return "Unrecognized marker"
	}
	printer := printerDisplayName(c, event.PrinterID)
	if event.Kind == "START" {
		return fmt.Sprintf("START · %s · %s", printer, event.Name)
	}
	return fmt.Sprintf("%s · %s · Layer %d", event.Kind, printer, event.Layer)
}
