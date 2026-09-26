package buddy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image/jpeg"
	"io"
	"mime"
	"net/http"
	"net/url"
	"time"
)

const MaxJPEGBytes = 20 << 20
const MaxImagePixels = 20_000_000

type Image struct {
	Bytes         []byte
	Width, Height int
}
type Capturer interface {
	Capture(context.Context, string) (Image, error)
}

type Camera struct {
	base     *url.URL
	client   *http.Client
	attempts int
	timeout  time.Duration
}

func NewCamera(c Config) *Camera {
	u, _ := url.Parse(c.Go2RTC.URL)
	return &Camera{base: u, client: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, attempts: c.Capture.Attempts, timeout: c.Capture.Timeout}
}

func (c *Camera) Capture(ctx context.Context, stream string) (Image, error) {
	var last error
	for i := 0; i < c.attempts; i++ {
		attempt, cancel := context.WithTimeout(ctx, c.timeout)
		img, err := c.fetch(attempt, stream)
		cancel()
		if err == nil {
			return img, nil
		}
		last = err
		if ctx.Err() != nil {
			return Image{}, ctx.Err()
		}
		if i+1 < c.attempts {
			select {
			case <-ctx.Done():
				return Image{}, ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
	return Image{}, fmt.Errorf("capture failed after %d attempt(s): %w", c.attempts, last)
}

func (c *Camera) fetch(ctx context.Context, stream string) (Image, error) {
	u := c.base.JoinPath("api/frame.jpeg")
	q := url.Values{"src": []string{stream}}
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return Image{}, errors.New("invalid go2rtc URL")
	}
	req.Header.Set("Accept", "image/jpeg")
	req.Header.Set("Cache-Control", "no-cache, no-store")
	resp, err := c.client.Do(req)
	if err != nil {
		// A URL error can contain go2rtc credentials; do not persist it.
		if ctx.Err() != nil {
			return Image{}, ctx.Err()
		}
		return Image{}, errors.New("go2rtc connection failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Image{}, fmt.Errorf("go2rtc returned HTTP %d", resp.StatusCode)
	}
	media, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || media != "image/jpeg" {
		return Image{}, errors.New("go2rtc did not return image/jpeg")
	}
	if resp.ContentLength > MaxJPEGBytes {
		return Image{}, errors.New("JPEG exceeds 20 MiB")
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, MaxJPEGBytes+1))
	if err != nil {
		return Image{}, errors.New("JPEG response was interrupted")
	}
	if len(b) > MaxJPEGBytes {
		return Image{}, errors.New("JPEG exceeds 20 MiB")
	}
	config, err := jpeg.DecodeConfig(bytes.NewReader(b))
	if err != nil || config.Width < 1 || config.Height < 1 || int64(config.Width)*int64(config.Height) > MaxImagePixels {
		return Image{}, errors.New("invalid JPEG or dimensions exceed 20 megapixels")
	}
	if _, err := jpeg.Decode(bytes.NewReader(b)); err != nil {
		return Image{}, errors.New("JPEG is truncated or corrupt")
	}
	return Image{Bytes: b, Width: config.Width, Height: config.Height}, nil
}
