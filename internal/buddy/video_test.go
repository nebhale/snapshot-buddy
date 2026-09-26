package buddy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image/color"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func requireFFmpeg(t *testing.T) {
	t.Helper()
	for _, name := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(name); err != nil {
			if os.Getenv("REQUIRE_FFMPEG") == "1" {
				t.Fatal(err)
			}
			t.Skip("FFmpeg and ffprobe are required for video integration tests")
		}
	}
}

func TestVideoOutputCannotEscapeDataDirectory(t *testing.T) {
	s, _ := testService(t)
	handle(t, s, "FRAME core-one 1", time.Now())
	j, err := s.RequestVideo(active(t, s, "core-one").ID, 15000)
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "precious.mp4")
	if err := os.WriteFile(outside, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.Root.MkdirAll(filepath.Dir(jobPath(j)), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(s.Config.DataDir, jobPath(j)+".tmp.mp4")); err != nil {
		t.Fatal(err)
	}
	if err := NewVideos(s).Encode(context.Background(), j); err == nil {
		t.Fatal("encoder followed escaping output symlink")
	}
	if data, err := os.ReadFile(outside); err != nil || string(data) != "keep" {
		t.Fatal("file outside data directory modified")
	}
}

func TestVideoTimingOrderingAndNormalization(t *testing.T) {
	requireFFmpeg(t)
	for _, tc := range []struct {
		name  string
		count int
		ms    int64
	}{
		{"fractional rate", 3, 2300}, {"sub-one-fps", 3, 15000}, {"one frame", 1, 5000}, {"fast", 60, 1000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, cam := testService(t)
			now := time.Now()
			colors := []color.RGBA{{R: 255, A: 255}, {G: 255, A: 255}, {B: 255, A: 255}}
			for i := 0; i < tc.count; i++ {
				cam.image = testJPEG(t, 63+(i%2)*9, 47+(i%2)*7, colors[i%3])
				handle(t, s, fmt.Sprintf("FRAME core-one %d", i+1), now.Add(time.Duration(i)*time.Millisecond))
			}
			ss := active(t, s, "core-one")
			j, err := s.RequestVideo(ss.ID, tc.ms)
			if err != nil {
				t.Fatal(err)
			}
			if err := NewVideos(s).Encode(context.Background(), j); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(s.Config.DataDir, jobPath(j))
			out, err := exec.Command("ffprobe", "-v", "error", "-count_frames", "-show_streams", "-show_format", "-of", "json", path).Output()
			if err != nil {
				t.Fatal(err)
			}
			var probe struct {
				Streams []struct {
					CodecName     string `json:"codec_name"`
					PixFmt        string `json:"pix_fmt"`
					Width, Height int
					NbReadFrames  string `json:"nb_read_frames"`
				}
				Format struct{ Duration string }
			}
			if err := json.Unmarshal(out, &probe); err != nil {
				t.Fatal(err)
			}
			if len(probe.Streams) != 1 {
				t.Fatalf("streams %s", out)
			}
			v := probe.Streams[0]
			if v.CodecName != "h264" || v.PixFmt != "yuv420p" || v.Width != 64 || v.Height != 48 || v.NbReadFrames != strconv.Itoa(tc.count) {
				t.Fatalf("unexpected stream %s", out)
			}
			duration, _ := strconv.ParseFloat(probe.Format.Duration, 64)
			if math.Abs(duration-float64(tc.ms)/1000) > .003 {
				t.Fatalf("duration %.6f wanted %.6f", duration, float64(tc.ms)/1000)
			}
			// Decode a center pixel from every frame to verify source order survives
			// mixed dimensions, slow rates, and H.264 frame reordering.
			pixels, err := exec.Command("ffmpeg", "-v", "error", "-i", path, "-vf", "crop=2:2:32:24,scale=1:1", "-fps_mode", "passthrough", "-f", "rawvideo", "-pix_fmt", "rgb24", "pipe:1").Output()
			if err != nil {
				t.Fatal(err)
			}
			if len(pixels) != tc.count*3 {
				t.Fatalf("decoded %d bytes for %d frames", len(pixels), tc.count)
			}
			for i := 0; i < tc.count; i++ {
				rgb := pixels[i*3 : i*3+3]
				if rgb[i%3] < 180 || rgb[(i+1)%3] > 70 || rgb[(i+2)%3] > 70 {
					t.Fatalf("frame %d color %v", i, rgb)
				}
			}
			file, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Index(string(file), "moov") > strings.Index(string(file), "mdat") {
				t.Fatal("MP4 is not fast-start")
			}
		})
	}
}

func TestVideoSnapshotCacheAndRecovery(t *testing.T) {
	s, _ := testService(t)
	now := time.Now()
	handle(t, s, "FRAME core-one 1", now)
	ss := active(t, s, "core-one")
	j, err := s.RequestVideo(ss.ID, 15000)
	if err != nil {
		t.Fatal(err)
	}
	cached, err := s.RequestVideo(ss.ID, 15000)
	if err != nil || cached.ID != j.ID {
		t.Fatal("did not reuse identical export")
	}
	handle(t, s, "FRAME core-one 2", now.Add(time.Second))
	if len(j.Manifest.Frames()) != 1 {
		t.Fatal("job's frame list mutated")
	}
	newer, err := s.RequestVideo(ss.ID, 15000)
	if err != nil || newer.ID == j.ID || len(newer.Manifest.Frames()) != 2 {
		t.Fatal("revision did not invalidate cache")
	}
	if err := s.CloseSession(ss.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSession(ss.ID); !errors.Is(err, ErrConflict) {
		t.Fatal("deleted session with queued video")
	}
	claimed, err := s.Store.ClaimJob()
	if err != nil || claimed.ID != j.ID {
		t.Fatal("job claim")
	}
	s.Root.Close()
	s.Store.Close()
	s.Store, err = OpenStore(s.Config.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	s.Root, err = os.OpenRoot(s.Config.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := s.Store.Job(j.ID)
	if err != nil || recovered.State != "queued" || len(recovered.Manifest.Frames()) != 1 {
		t.Fatalf("job recovery %+v %v", recovered, err)
	}
	if err := s.Store.FinishJob(j.ID, "failed", "encoder unavailable"); err != nil {
		t.Fatal(err)
	}
	retried, err := s.Store.CreateJob(j.Manifest, j.DurationMS)
	if err != nil || retried.ID != j.ID || retried.State != "queued" || retried.Error != "" {
		t.Fatalf("retry %+v %v", retried, err)
	}
	if _, err := s.RequestVideo(ss.ID, 0); err == nil {
		t.Fatal("accepted zero duration")
	}
}

func TestVideoWorkerAndFailedEncode(t *testing.T) {
	requireFFmpeg(t)
	s, _ := testService(t)
	handle(t, s, "FRAME core-one 1", time.Now())
	ss := active(t, s, "core-one")
	j, err := s.RequestVideo(ss.ID, 1000)
	if err != nil {
		t.Fatal(err)
	}
	v := NewVideos(s)
	ctx, cancel := context.WithCancel(context.Background())
	v.Start(ctx)
	defer func() { cancel(); v.Wait() }()
	deadline := time.Now().Add(20 * time.Second)
	for {
		got, err := s.Store.Job(j.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.State == "ready" {
			if got.Progress != 1 {
				t.Fatal("incomplete progress")
			}
			break
		}
		if got.State == "failed" || time.Now().After(deadline) {
			t.Fatalf("worker %+v", got)
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	v.Wait()
	v.binary = "/nonexistent/ffmpeg"
	if err := v.Encode(context.Background(), j); err == nil {
		t.Fatal("missing encoder was accepted")
	}
	if _, err := s.Root.Stat(jobPath(j) + ".tmp.mp4"); !os.IsNotExist(err) {
		t.Fatal("partial export survived failed encode")
	}
}
