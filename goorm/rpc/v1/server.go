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
	client     *http.Client
}

const defaultRequestTimeout = 5 * time.Second

func NewGoormRpcServer(bindDevice string, directBind bool) *GoormRpcServer {
	server := &GoormRpcServer{
		bindDevice: bindDevice,
		directBind: directBind,
	}

	server.client = server.makeHttpClient()

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
		if s.directBind {
			panic("direct bind requires a specific network device")
		}
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

func httpDo(ctx context.Context, s *GoormRpcServer, req *HttpRequest, method string, requestBody io.Reader) (*HttpResponse, error) {
	log.Printf("%s Url=%s, Query=%v\n", method, req.Url, req.Query)

	if req.TimeoutMs < 0 {
		return nil, fmt.Errorf("timeout_ms must not be negative")
	}

	timeout := defaultRequestTimeout
	if req.TimeoutMs > 0 {
		timeout = time.Duration(req.TimeoutMs) * time.Millisecond
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, method, req.Url, requestBody)
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP %s request: %v", method, err)
	}

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

	resp, err := s.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to perform HTTP %s request: %v", method, err)
	}
	defer resp.Body.Close()

	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read HTTP response body: %v", err)
	}

	responseHeaders := make(map[string]string)
	for key, values := range resp.Header {
		if len(values) > 0 {
			responseHeaders[key] = values[0]
		}
	}
	responseCookies := make(map[string]string)
	for _, cookie := range resp.Cookies() {
		responseCookies[cookie.Name] = cookie.Value
	}

	return &HttpResponse{
		StatusCode: int32(resp.StatusCode),
		Headers:    responseHeaders,
		Cookies:    responseCookies,
		Body:       responseBody,
	}, nil
}

func (s *GoormRpcServer) HttpGet(ctx context.Context, req *HttpRequest) (*HttpResponse, error) {
	return httpDo(ctx, s, req, "GET", nil)
}

func (s *GoormRpcServer) HttpPost(ctx context.Context, req *HttpRequest) (*HttpResponse, error) {
	return httpDo(ctx, s, req, "POST", bytes.NewReader(req.Body))
}

func (s *GoormRpcServer) HttpDelete(ctx context.Context, req *HttpRequest) (*HttpResponse, error) {
	return httpDo(ctx, s, req, "DELETE", bytes.NewReader(req.Body))
}

func (s *GoormRpcServer) HttpPut(ctx context.Context, req *HttpRequest) (*HttpResponse, error) {
	return httpDo(ctx, s, req, "PUT", bytes.NewReader(req.Body))
}

func (s *GoormRpcServer) HttpPatch(ctx context.Context, req *HttpRequest) (*HttpResponse, error) {
	return httpDo(ctx, s, req, "PATCH", bytes.NewReader(req.Body))
}
