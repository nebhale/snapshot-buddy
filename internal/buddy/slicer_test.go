package buddy

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Exercise the actual macro engine, including UTF-8 and exact byte budgets.
func TestSlicerFilenameGuard(t *testing.T) {
	binary := os.Getenv("PRUSA_SLICER")
	if binary == "" {
		binary = "/Applications/PrusaSlicer.app/Contents/MacOS/PrusaSlicer"
	}
	if _, err := os.Stat(binary); err != nil {
		if os.Getenv("REQUIRE_SLICER") == "1" {
			t.Fatal(err)
		}
		t.Skip("set PRUSA_SLICER to validate generated snippets")
	}
	for _, id := range []string{"c1", "12345678901234567890123"} {
		limit := 47 - len(markerPrefix+"START "+id+" ")
		for _, tc := range []struct{ name, want string }{
			{"A b_1.-", "A b_1.-"},
			{strings.Repeat("a", limit), strings.Repeat("a", limit)},
			{strings.Repeat("a", limit+1), "Print"},
			{"紫色", "Print"}, {"part;1", "Print"}, {`part"1`, "Print"},
		} {
			t.Run(id+"/"+tc.name, func(t *testing.T) {
				dir := t.TempDir()
				model := filepath.Join(dir, tc.name+".stl")
				if err := os.WriteFile(model, []byte(slicerCubeSTL()), 0600); err != nil {
					t.Fatal(err)
				}
				c := testConfig(t)
				c.Printers[0].ID = id
				snippets := GCode(c, c.Printers[0])
				output := filepath.Join(dir, "print.gcode")
				out, err := exec.Command(binary, "--export-gcode", "--output", output, "--start-gcode", snippets.Start, "--end-gcode", snippets.Stop, "--after-layer-gcode", snippets.Layer, model).CombinedOutput()
				if err != nil {
					t.Fatalf("slicing failed: %v\n%s", err, out)
				}
				data, err := os.ReadFile(output)
				if err != nil {
					t.Fatal(err)
				}
				starts := 0
				for _, line := range strings.Split(string(data), "\n") {
					if !strings.HasPrefix(line, markerPrefix) {
						continue
					}
					if len(line) > 47 {
						t.Fatalf("oversized marker: %q", line)
					}
					event, err := ParseMarker(line)
					if err != nil {
						t.Fatal(err)
					}
					if event.Kind == "START" {
						starts++
						if event.Name != tc.want {
							t.Fatalf("name %q, want %q", event.Name, tc.want)
						}
					}
				}
				if starts != 3 {
					t.Fatalf("got %d START copies", starts)
				}
			})
		}
	}
}

func slicerCubeSTL() string {
	v := [][3]int{{0, 0, 0}, {10, 0, 0}, {10, 10, 0}, {0, 10, 0}, {0, 0, 4}, {10, 0, 4}, {10, 10, 4}, {0, 10, 4}}
	faces := [][3]int{{0, 2, 1}, {0, 3, 2}, {4, 5, 6}, {4, 6, 7}, {0, 1, 5}, {0, 5, 4}, {1, 2, 6}, {1, 6, 5}, {2, 3, 7}, {2, 7, 6}, {3, 0, 4}, {3, 4, 7}}
	var b strings.Builder
	b.WriteString("solid cube\n")
	for _, f := range faces {
		b.WriteString("facet normal 0 0 0\nouter loop\n")
		for _, i := range f {
			fmt.Fprintf(&b, "vertex %d %d %d\n", v[i][0], v[i][1], v[i][2])
		}
		b.WriteString("endloop\nendfacet\n")
	}
	b.WriteString("endsolid cube\n")
	return b.String()
}
