package main

import (
	"context"
	"flag"
	"log"
	"net"
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

func main() {
	// parse flags
	directBind := flag.Bool("direct-bind", false, "Direct bind to network device")
	bindDevice := flag.String("bind-device", "auto", "Network device to bind to")

	connectionTimeoutMilliseconds := flag.Int64("timeout", 5000, "Connection timeout in milliseconds")

	flag.Parse()

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
