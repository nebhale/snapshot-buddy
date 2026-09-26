package buddy

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Videos struct {
	service *Service
	binary  string
	wg      sync.WaitGroup
}

func NewVideos(s *Service) *Videos { return &Videos{service: s, binary: "ffmpeg"} }

func (v *Videos) Start(ctx context.Context) {
	for i := 0; i < v.service.Config.Video.Workers; i++ {
		v.wg.Go(func() {
			for ctx.Err() == nil {
				j, err := v.service.Store.ClaimJob()
				if errors.Is(err, ErrNotFound) {
					select {
					case <-ctx.Done():
						return
					case <-time.After(time.Second):
					}
					continue
				}
				if err != nil {
					slog.Error("claim video job", "error", err)
					select {
					case <-ctx.Done():
						return
					case <-time.After(time.Second):
					}
					continue
				}
				err = v.Encode(ctx, j)
				state, message := "ready", ""
				if err != nil {
					state, message = "failed", err.Error()
				}
				if ctx.Err() != nil {
					state, message = "queued", ""
				}
				if err := v.service.Store.FinishJob(j.ID, state, message); err != nil {
					slog.Error("save video result", "job", j.ID, "error", err)
				}
				slog.Info("video job finished", "job", j.ID, "state", state, "error", message)
			}
		})
	}
}

func (v *Videos) Wait() { v.wg.Wait() }

func (s *Service) RequestVideo(id string, durationMS int64) (Job, error) {
	s.filesMu.RLock()
	defer s.filesMu.RUnlock()
	m, err := s.Store.Snapshot(id)
	if err != nil {
		return Job{}, err
	}
	return s.Store.CreateJob(m, durationMS)
}

func jobPath(j Job) string { return filepath.Join("sessions", j.SessionID, "exports", j.ID+".mp4") }

// Encode sends a fixed, ordered snapshot of JPEGs through a pipe. This keeps
// the memory bound independent of print length and avoids duplicating the
// source images on disk. No shell interpolates names or configuration.
func (v *Videos) Encode(ctx context.Context, j Job) error {
	s := v.service
	s.filesMu.RLock()
	defer s.filesMu.RUnlock()
	frames := j.Manifest.Frames()
	if len(frames) == 0 {
		return errors.New("video has no frames")
	}
	output := jobPath(j)
	if err := s.Root.MkdirAll(filepath.Dir(output), 0750); err != nil {
		return err
	}
	temp := output + ".tmp.mp4"
	// Pass an already confined, seekable file descriptor to FFmpeg. Giving it
	// an absolute pathname would bypass os.Root's symlink protections.
	outputFile, err := s.Root.OpenFile(temp, os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0640)
	if err != nil {
		return err
	}
	defer outputFile.Close()
	defer s.Root.Remove(temp)
	rate := fmt.Sprintf("%d/%d", len(frames)*1000, j.DurationMS)
	// Baseline normalization: retain the first frame's aspect ratio, pad to
	// even dimensions, and fit later resolution changes without distortion.
	w, h := (frames[0].Width+1)&^1, (frames[0].Height+1)&^1
	cmd := exec.CommandContext(ctx, v.binary, "-hide_banner", "-loglevel", "error", "-nostdin", "-y",
		"-f", "image2pipe", "-framerate", rate, "-vcodec", "mjpeg", "-i", "pipe:0",
		"-an", "-c:v", "libx264", "-preset", "veryfast", "-crf", "20", "-threads", "2",
		"-vf", "scale=in_range=pc:out_range=tv,format=yuv420p", "-color_range", "tv",
		"-pix_fmt", "yuv420p", "-fps_mode", "passthrough", "-enc_time_base", fmt.Sprintf("%d:%d", j.DurationMS, len(frames)*1000),
		"-video_track_timescale", "1000000", "-moov_size", strconv.Itoa(65536+len(frames)*128),
		"-progress", "pipe:1", "-f", "mp4", "/dev/fd/3")
	// Reserve a conservative 128 bytes per sample plus header overhead for the
	// front-loaded moov atom. Unlike +faststart's second pass, this never needs
	// to reopen the inherited descriptor (which shares offsets on macOS).
	cmd.ExtraFiles = []*os.File{outputFile}
	cmd.WaitDelay = 3 * time.Second
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return err
	}
	stderr := &limitedBuffer{limit: 8192}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		stdin.Close()
		return fmt.Errorf("start FFmpeg: %w", err)
	}
	fed := make(chan error, 1)
	go func() {
		defer stdin.Close()
		for _, frame := range frames {
			if err := ctx.Err(); err != nil {
				fed <- err
				return
			}
			f, err := s.Root.Open(filepath.Join("sessions", j.SessionID, frame.File))
			if err != nil {
				fed <- err
				return
			}
			if frame.Width == w && frame.Height == h {
				_, err = io.Copy(stdin, f)
			} else {
				var src image.Image
				src, err = jpeg.Decode(f)
				if err == nil {
					err = jpeg.Encode(stdin, fitImage(src, w, h), &jpeg.Options{Quality: 95})
				}
			}
			f.Close()
			if err != nil {
				fed <- err
				return
			}
		}
		fed <- nil
	}()
	scanner := bufio.NewScanner(stdout)
	var lastProgress time.Time
	for scanner.Scan() {
		key, value, ok := strings.Cut(scanner.Text(), "=")
		if ok && key == "frame" && time.Since(lastProgress) >= 200*time.Millisecond {
			n, _ := strconv.Atoi(strings.TrimSpace(value))
			progress := min(.99, float64(n)/float64(len(frames)))
			if err := s.Store.JobProgress(j.ID, progress); err != nil {
				slog.Error("save encoding progress", "error", err)
			}
			lastProgress = time.Now()
		}
	}
	feedErr := <-fed
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if feedErr != nil {
		return fmt.Errorf("read snapshot frame: %w", feedErr)
	}
	if waitErr != nil {
		return fmt.Errorf("FFmpeg failed: %s", strings.TrimSpace(stderr.String()))
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	stat, err := outputFile.Stat()
	if err == nil {
		err = outputFile.Sync()
	}
	if err != nil {
		return err
	}
	if stat.Size() == 0 {
		return errors.New("FFmpeg produced an empty video")
	}
	return s.Root.Rename(temp, output)
}

// fitImage uses bilinear resampling only when dimensions differ. Matching
// camera frames go to FFmpeg unchanged. The destination is letterboxed.
func fitImage(src image.Image, w, h int) image.Image {
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	b := src.Bounds()
	scale := min(float64(w)/float64(b.Dx()), float64(h)/float64(b.Dy()))
	nw, nh := max(1, int(float64(b.Dx())*scale)), max(1, int(float64(b.Dy())*scale))
	ox, oy := (w-nw)/2, (h-nh)/2
	for y := 0; y < nh; y++ {
		fy := max(0, (float64(y)+.5)/scale-.5)
		y0 := min(int(fy), b.Dy()-1)
		y1 := min(y0+1, b.Dy()-1)
		dy := fy - float64(y0)
		for x := 0; x < nw; x++ {
			fx := max(0, (float64(x)+.5)/scale-.5)
			x0 := min(int(fx), b.Dx()-1)
			x1 := min(x0+1, b.Dx()-1)
			dx := fx - float64(x0)
			a := color.RGBAModel.Convert(src.At(b.Min.X+x0, b.Min.Y+y0)).(color.RGBA)
			bb := color.RGBAModel.Convert(src.At(b.Min.X+x1, b.Min.Y+y0)).(color.RGBA)
			c := color.RGBAModel.Convert(src.At(b.Min.X+x0, b.Min.Y+y1)).(color.RGBA)
			d := color.RGBAModel.Convert(src.At(b.Min.X+x1, b.Min.Y+y1)).(color.RGBA)
			blend := func(a, b, c, d uint8) uint8 {
				return uint8((1-dy)*((1-dx)*float64(a)+dx*float64(b)) + dy*((1-dx)*float64(c)+dx*float64(d)) + .5)
			}
			dst.SetRGBA(x+ox, y+oy, color.RGBA{blend(a.R, bb.R, c.R, d.R), blend(a.G, bb.G, c.G, d.G), blend(a.B, bb.B, c.B, d.B), 255})
		}
	}
	return dst
}

type limitedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if b.Len() < b.limit {
		_, _ = b.Buffer.Write(p[:min(len(p), b.limit-b.Len())])
	}
	return n, nil
}
