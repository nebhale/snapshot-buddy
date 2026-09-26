package main

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"time"

	"github.com/nebhale/snapshot-buddy/internal/buddy"
)

type camera struct{}

func (camera) Capture(context.Context, string) (buddy.Image, error) {
	im := image.NewRGBA(image.Rect(0, 0, 640, 480))
	fill := func(r image.Rectangle, c color.RGBA) {
		for y := r.Min.Y; y < r.Max.Y; y++ {
			for x := r.Min.X; x < r.Max.X; x++ {
				im.SetRGBA(x, y, c)
			}
		}
	}
	fill(im.Bounds(), color.RGBA{24, 32, 31, 255})
	for y := 260; y < 450; y += 18 {
		fill(image.Rect(30, y, 610, y+1), color.RGBA{53, 66, 62, 255})
	}
	for x := 40; x < 620; x += 28 {
		fill(image.Rect(x, 260, x+1, 450), color.RGBA{53, 66, 62, 255})
	}
	for y := 140; y < 362; y += 4 {
		inset := (y - 140) / 5
		fill(image.Rect(220-inset, y, 420+inset, y+3), color.RGBA{176, 193, 143, 255})
	}
	var out bytes.Buffer
	if err := jpeg.Encode(&out, im, &jpeg.Options{Quality: 90}); err != nil {
		return buddy.Image{}, err
	}
	return buddy.Image{Bytes: out.Bytes(), Width: 640, Height: 480}, nil
}

func main() {
	data, err := os.MkdirTemp("", "snapshot-buddy-ui-")
	if err != nil {
		log.Fatal(err)
	}
	c, err := buddy.DecodeConfig(bytes.NewBufferString("metrics:\n  advertised_host: 127.0.0.1\ngo2rtc:\n  url: http://127.0.0.1:1984\nprinters:\n  - id: core-one\n    name: Prusa CORE One\n    stream: core-one\n"))
	if err != nil {
		log.Fatal(err)
	}
	c.DataDir = data
	s, err := buddy.OpenStore(data)
	if err != nil {
		log.Fatal(err)
	}
	service, err := buddy.NewService(c, s, camera{})
	if err != nil {
		log.Fatal(err)
	}
	at := time.Now().UTC()
	event := func(payload string) {
		e, err := buddy.ParseMarker("M118 SNAPSHOT_BUDDY_V1 " + payload)
		if err != nil {
			log.Fatal(err)
		}
		e.SourceIP, e.ReceivedAt = "127.0.0.1", at
		at = at.Add(time.Minute)
		if err := service.Handle(context.Background(), e); err != nil {
			log.Fatal(err)
		}
	}
	for _, name := range []string{"Gridfinity tool organizer", "Camera mounting bracket", "Spiral desk planter"} {
		event("START core-one " + name)
		for i := 1; i <= 8; i++ {
			event(fmt.Sprintf("FRAME core-one %d", i))
		}
		event("STOP core-one 9")
		at = at.Add(2 * time.Hour)
	}
	event("FRAME core-one 42")
	event("STOP core-one 43")
	event("START core-one Workshop parts tray")
	for i := 1; i <= 4; i++ {
		event(fmt.Sprintf("FRAME core-one %d", i))
	}
	buddy.NewVideos(service).Start(context.Background())
	h, err := buddy.NewWeb(service)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("Local preview: http://127.0.0.1:8090; sample data: %s", data)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /demo.jpg", func(w http.ResponseWriter, r *http.Request) {
		img, _ := (camera{}).Capture(r.Context(), "")
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(img.Bytes)
	})
	mux.HandleFunc("GET /static/live.js", func(w http.ResponseWriter, r *http.Request) {
		recorded := httptest.NewRecorder()
		h.ServeHTTP(recorded, r)
		// Use the real modal implementation with explicitly synthetic demo media.
		definitions, _, _ := strings.Cut(recorded.Body.String(), "const liveDialog =")
		w.Header().Set("Content-Type", "application/javascript")
		fmt.Fprint(w, definitions, demoMedia)
	})
	mux.Handle("/", h)
	log.Fatal(http.ListenAndServe("127.0.0.1:8090", mux))
}

const demoMedia = `
const demoSheet = new LiveSheet(document.querySelector("#live-dialog"));
document.querySelectorAll("[data-live]").forEach(button => {
 const video = button.querySelector("video");
 const canvas = document.createElement("canvas");
 canvas.width = 640; canvas.height = 480;
 const ctx = canvas.getContext("2d");
 const image = new Image();
 image.onload = () => {
   ctx.drawImage(image, 0, 0);
   video.srcObject = canvas.captureStream(5);
   video.play().catch(() => {});
   setInterval(() => ctx.drawImage(image, 0, 0), 200);
   button.querySelector("[data-live-message]").hidden = true;
   button.querySelector("[data-live-status]").textContent = "Live";
 };
 image.src = "/demo.jpg";
 const preview = {button, video, message: {textContent: ""}};
 button.addEventListener("click", () => demoSheet.open(preview));
});
`
