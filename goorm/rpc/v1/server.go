package v1

import (
	"bytes"
	context "context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"runtime"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

type GoormRpcServer struct {
	UnimplementedGoormRpcV1Server
	bindDevice string
	directBind bool
}

func NewGoormRpcServer(bindDevice string, directBind bool) *GoormRpcServer {
	server := &GoormRpcServer{
		bindDevice: bindDevice,
		directBind: directBind,
	}

	// for testing
	server.makeHttpClient()

	return server
}

// usually for macos
func getLocalIP(interfaceName string) (net.IP, error) {
	iface, err := net.InterfaceByName(interfaceName)
	if err != nil {
		return nil, fmt.Errorf("interface %s not found: %w", interfaceName, err)
	}

	addrs, err := iface.Addrs()
	if err != nil {
		return nil, fmt.Errorf("unable to get addresses for interface %s: %w", interfaceName, err)
	}

	for _, addr := range addrs {
		var ip net.IP
		switch v := addr.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}

		// Skip loopback and non-IPv4 addresses
		if ip == nil || ip.IsLoopback() || ip.To4() == nil {
			continue
		}

		log.Printf("Found IP address %s for interface %s\n", ip, interfaceName)

		return ip, nil
	}

	return nil, fmt.Errorf("no suitable IP address found for interface %s", interfaceName)
}

func (s *GoormRpcServer) makeHttpClient() *http.Client {
	if s.bindDevice == "auto" {
		return &http.Client{}
	}

	// case for direct bind
	if s.directBind {
		// check if the system is linux
		log.Printf("OS: %s\n", runtime.GOOS)
		if runtime.GOOS != "linux" {
			panic("direct bind is only supported on Linux")
		}

		// check if privileged user
		if syscall.Getuid() != 0 {
			panic("direct bind requires root privileges")
		}

		transport := &http.Transport{
			DialContext: (&net.Dialer{
				Control: func(network, address string, c syscall.RawConn) error {
					var controlErr error
					err := c.Control(func(fd uintptr) {
						// Convert fd to int for syscall functions
						socketFD := int(fd)
						// Set SO_BINDTODEVICE to bind the socket to the specified device
						// Note: This requires root privileges
						controlErr = unix.SetsockoptString(socketFD, unix.SOL_SOCKET, 0x19, s.bindDevice)
					})
					if err != nil {
						return err
					}
					return controlErr
				},
			}).DialContext,
		}

		return &http.Client{
			Transport: transport,
		}
	} else {
		// case for binding to a specific interface
		localIP, err := getLocalIP(s.bindDevice)
		if err != nil {
			panic(fmt.Sprintf("failed to get local IP for interface %s: %v", s.bindDevice, err))
		}

		transport := &http.Transport{
			DialContext: (&net.Dialer{
				LocalAddr: &net.TCPAddr{
					IP: localIP,
				},
			}).DialContext,
		}

		return &http.Client{
			Transport: transport,
		}
	}
}

func httpNonGet(s *GoormRpcServer, req *HttpRequest, method string) (*HttpResponse, error) {
	// Create HTTP request
	httpReq, err := http.NewRequest(method, req.Url, bytes.NewReader(req.Body))
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP %s request: %v", method, err)
	}

	// Add headers, query parameters, and cookies
	for key, value := range req.Headers {
		httpReq.Header.Set(key, value)
	}
	q := httpReq.URL.Query()
	for key, value := range req.Query {
		q.Add(key, value)
	}
	httpReq.URL.RawQuery = q.Encode()
	for key, value := range req.Cookies {
		httpReq.AddCookie(&http.Cookie{Name: key, Value: value})
	}

	// Perform the HTTP request
	client := s.makeHttpClient()
	client.Timeout = time.Duration(req.TimeoutMs) * time.Millisecond

	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to perform HTTP %s request: %v", method, err)
	}
	defer resp.Body.Close()

	// Read response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read HTTP response body: %v", err)
	}

	// Create response
	responseHeaders := make(map[string]string)
	for key, values := range resp.Header {
		responseHeaders[key] = values[0]
	}
	responseCookies := make(map[string]string)
	for _, cookie := range resp.Cookies() {
		responseCookies[cookie.Name] = cookie.Value
	}

	return &HttpResponse{
		StatusCode: int32(resp.StatusCode),
		Headers:    responseHeaders,
		Cookies:    responseCookies,
		Body:       body,
	}, nil
}

func httpGet(s *GoormRpcServer, req *HttpRequest) (*HttpResponse, error) {
	// Create HTTP request
	httpReq, err := http.NewRequest("GET", req.Url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP GET request: %v", err)
	}

	// Add headers, query parameters, and cookies
	for key, value := range req.Headers {
		httpReq.Header.Set(key, value)
	}
	q := httpReq.URL.Query()
	for key, value := range req.Query {
		q.Add(key, value)
	}
	httpReq.URL.RawQuery = q.Encode()
	for key, value := range req.Cookies {
		httpReq.AddCookie(&http.Cookie{Name: key, Value: value})
	}

	// Perform the HTTP request
	client := s.makeHttpClient()
	client.Timeout = time.Duration(req.TimeoutMs) * time.Millisecond

	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to perform HTTP GET request: %v", err)
	}
	defer resp.Body.Close()

	// Read response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read HTTP response body: %v", err)
	}

	// Create response
	responseHeaders := make(map[string]string)
	for key, values := range resp.Header {
		responseHeaders[key] = values[0]
	}
	responseCookies := make(map[string]string)
	for _, cookie := range resp.Cookies() {
		responseCookies[cookie.Name] = cookie.Value
	}
	return &HttpResponse{
		StatusCode: int32(resp.StatusCode),
		Headers:    responseHeaders,
		Cookies:    responseCookies,
		Body:       body,
	}, nil
}

func (s *GoormRpcServer) HttpGet(ctx context.Context, req *HttpRequest) (*HttpResponse, error) {
	return httpGet(s, req)
}

func (s *GoormRpcServer) HttpPost(ctx context.Context, req *HttpRequest) (*HttpResponse, error) {
	return httpNonGet(s, req, "POST")
}

func (s *GoormRpcServer) HttpDelete(ctx context.Context, req *HttpRequest) (*HttpResponse, error) {
	return httpNonGet(s, req, "DELETE")
}

func (s *GoormRpcServer) HttpPut(ctx context.Context, req *HttpRequest) (*HttpResponse, error) {
	return httpNonGet(s, req, "PUT")
}

func (s *GoormRpcServer) HttpPatch(ctx context.Context, req *HttpRequest) (*HttpResponse, error) {
	return httpNonGet(s, req, "PATCH")
}
