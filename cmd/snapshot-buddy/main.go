package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/nebhale/snapshot-buddy/internal/buddy"
)

var version = "dev"

func main() {
	if err := run(); err != nil {
		slog.Error("Snapshot Buddy stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	configPath := flag.String("config", "/config/config.yaml", "YAML configuration path")
	check := flag.Bool("check-config", false, "validate configuration and exit")
	health := flag.Bool("healthcheck", false, "check the configured HTTP listener and exit")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return nil
	}
	c, err := buddy.LoadConfig(*configPath)
	if err != nil {
		return fmt.Errorf("configuration: %w", err)
	}
	if *check {
		fmt.Println("Configuration is valid")
		return nil
	}
	if *health {
		host, port, _ := net.SplitHostPort(c.HTTP.Address)
		if host == "" || host == "0.0.0.0" || host == "::" {
			host = "127.0.0.1"
		}
		client := http.Client{Timeout: 3 * time.Second}
		r, err := client.Get("http://" + net.JoinHostPort(host, port) + "/healthz")
		if err != nil {
			return err
		}
		defer r.Body.Close()
		if r.StatusCode != 200 {
			return fmt.Errorf("unhealthy: HTTP %d", r.StatusCode)
		}
		return nil
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return errors.New("FFmpeg is required on PATH")
	}
	store, err := buddy.OpenStore(c.DataDir)
	if err != nil {
		return err
	}
	defer store.Close()
	s, err := buddy.NewService(c, store, buddy.NewCamera(c))
	if err != nil {
		return err
	}
	defer s.Root.Close()
	handler, err := buddy.NewWeb(s)
	if err != nil {
		return err
	}
	udpAddr, err := net.ResolveUDPAddr("udp", c.Metrics.Address)
	if err != nil {
		return err
	}
	udp, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return err
	}
	defer udp.Close()
	listener, err := net.Listen("tcp", c.HTTP.Address)
	if err != nil {
		return err
	}
	defer listener.Close()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	s.Start(ctx)
	videos := buddy.NewVideos(s)
	videos.Start(ctx)
	server := &http.Server{BaseContext: func(net.Listener) context.Context { return ctx }, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 10 * time.Minute, IdleTimeout: time.Minute, MaxHeaderBytes: 16384}
	failed := make(chan error, 2)
	go func() { failed <- s.ServeUDP(ctx, udp) }()
	go func() { failed <- server.Serve(listener) }()
	slog.Info("Snapshot Buddy ready", "version", version, "http", c.HTTP.Address, "udp", c.Metrics.Address, "printers", len(c.Printers), "authentication", c.AuthUser != "")
	select {
	case <-ctx.Done():
	case err = <-failed:
		if errors.Is(err, http.ErrServerClosed) || (err != nil && strings.Contains(err.Error(), "use of closed network connection")) {
			err = nil
		}
	}
	stop()
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if e := server.Shutdown(shutdown); e != nil {
		server.Close()
	}
	s.Wait()
	videos.Wait()
	return err
}
