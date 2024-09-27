package v1

import (
	"bytes"
	context "context"
	"fmt"
	"io"
	"net"
	"net/http"
	"syscall"

	"golang.org/x/sys/unix"
)

type GoormRpcServer struct {
	UnimplementedGoormRpcV1Server
	bindDevice string
}

func NewGoormRpcServer(bindDevice string) *GoormRpcServer {
	return &GoormRpcServer{
		bindDevice: bindDevice,
	}
}

func (s *GoormRpcServer) makeHttpClient() *http.Client {
	if s.bindDevice == "auto" {
		return &http.Client{}
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
}

func (s *GoormRpcServer) HttpGet(ctx context.Context, req *HttpGetRequest) (*HttpResponse, error) {
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

func (s *GoormRpcServer) HttpPost(ctx context.Context, req *HttpPostRequest) (*HttpResponse, error) {
	// Create HTTP POST request
	httpReq, err := http.NewRequest("POST", req.Url, bytes.NewReader(req.Body))
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP POST request: %v", err)
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
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to perform HTTP POST request: %v", err)
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
