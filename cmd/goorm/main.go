package main

import (
	"context"
	"flag"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	rpc "thunderstorm/goorm/rpc/v1"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
)

func logRequests() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		start := time.Now()
		resp, err := handler(ctx, req)
		log.Printf("Request - Method: %s, Duration: %s, Error: %v", info.FullMethod, time.Since(start), err)
		return resp, err
	}
}

func configureLogOutput(logDir string) (*os.File, error) {
	if logDir == "" {
		return nil, nil
	}
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return nil, err
	}
	logFile, err := os.OpenFile(filepath.Join(logDir, "goorm.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, err
	}
	log.SetOutput(io.MultiWriter(os.Stderr, logFile))
	return logFile, nil
}

func startMetricsServer(addr string) {
	if addr == "" {
		return
	}
	mux := http.NewServeMux()
	mux.Handle("/metrics", rpc.MetricsHandler())
	go func() {
		log.Printf("Metrics listening on %s/metrics", addr)
		if err := http.ListenAndServe(addr, mux); err != nil {
			log.Printf("metrics server failed: %v", err)
		}
	}()
}

func main() {
	// parse flags
	directBind := flag.Bool("direct-bind", false, "Direct bind to network device")
	bindDevice := flag.String("bind-device", "auto", "Network device to bind to")
	logDir := flag.String("log-dir", "", "Directory to write goorm.log; empty logs only to stderr")
	metricsAddr := flag.String("metrics-addr", "", "Address for Prometheus metrics endpoint; empty disables metrics HTTP server")

	connectionTimeoutMilliseconds := flag.Int64("timeout", 5000, "Connection timeout in milliseconds")

	flag.Parse()

	logFile, err := configureLogOutput(*logDir)
	if err != nil {
		log.Fatalf("failed to configure log output: %v", err)
	}
	if logFile != nil {
		defer logFile.Close()
	}
	startMetricsServer(*metricsAddr)

	// set server options - timeout to 5 seconds
	server := grpc.NewServer(
		grpc.ConnectionTimeout(
			time.Duration(*connectionTimeoutMilliseconds)*time.Millisecond,
		),
		grpc.UnaryInterceptor(logRequests()),
	)

	rpc.RegisterGoormRpcV1Server(server, rpc.NewGoormRpcServer(*bindDevice, *directBind))
	reflection.Register(server)

	listener, err := net.Listen("tcp", ":50051")
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}

	log.Printf("Server listening on :50051, bind device: %s, direct bind: %v, timeout: %d ms", *bindDevice, *directBind, *connectionTimeoutMilliseconds)
	if err := server.Serve(listener); err != nil {
		log.Fatalf("failed to serve: %v", err)
	}
}
