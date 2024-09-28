package v1

import (
	"bytes"
	context "context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

type GoormRpcServer struct {
	UnimplementedGoormRpcV1Server
	bindDevice string
	directBind bool
	client     *http.Client
	seq        atomic.Uint64
}

type relayMetricKey struct {
	Method      string
	StatusClass string
	ErrorKind   string
}

type relayMetrics struct {
	mu                 sync.Mutex
	requestsTotal      map[relayMetricKey]uint64
	errorsTotal        map[relayMetricKey]uint64
	durationSecondsSum map[string]float64
	durationCount      map[string]uint64
	requestBodyBytes   map[string]uint64
	responseBodyBytes  map[string]uint64
}

const defaultRequestTimeout = 5 * time.Second
const maxRequestBodyBytes = 10 * 1024 * 1024
const maxResponseBodyBytes = 10 * 1024 * 1024

var globalRelayMetrics = newRelayMetrics()

func newRelayMetrics() *relayMetrics {
	return &relayMetrics{
		requestsTotal:      map[relayMetricKey]uint64{},
		errorsTotal:        map[relayMetricKey]uint64{},
		durationSecondsSum: map[string]float64{},
		durationCount:      map[string]uint64{},
		requestBodyBytes:   map[string]uint64{},
		responseBodyBytes:  map[string]uint64{},
	}
}

func NewGoormRpcServer(bindDevice string, directBind bool) *GoormRpcServer {
	server := &GoormRpcServer{
		bindDevice: bindDevice,
		directBind: directBind,
	}

	server.client = server.makeHttpClient()

	return server
}

// MetricsHandler returns a Prometheus text exposition endpoint for relay metrics.
func MetricsHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = io.WriteString(w, globalRelayMetrics.renderPrometheus())
	})
}

func (m *relayMetrics) observe(method string, statusCode int, duration time.Duration, requestBytes int64, responseBytes int64, err error) {
	method = normalizeMethod(method)
	statusClass := statusClass(statusCode)
	errorKind := errorKind(err)

	m.mu.Lock()
	defer m.mu.Unlock()

	m.requestsTotal[relayMetricKey{Method: method, StatusClass: statusClass}]++
	if errorKind != "" {
		m.errorsTotal[relayMetricKey{Method: method, ErrorKind: errorKind}]++
	}
	m.durationSecondsSum[method] += duration.Seconds()
	m.durationCount[method]++
	if requestBytes > 0 {
		m.requestBodyBytes[method] += uint64(requestBytes)
	}
	if responseBytes > 0 {
		m.responseBodyBytes[method] += uint64(responseBytes)
	}
}

func (m *relayMetrics) renderPrometheus() string {
	m.mu.Lock()
	defer m.mu.Unlock()

	var b strings.Builder
	b.WriteString("# HELP goorm_rpc_http_requests_total Total relayed HTTP requests.\n")
	b.WriteString("# TYPE goorm_rpc_http_requests_total counter\n")
	for _, key := range sortedMetricKeys(m.requestsTotal) {
		fmt.Fprintf(&b, "goorm_rpc_http_requests_total{method=%q,status_class=%q} %d\n", key.Method, key.StatusClass, m.requestsTotal[key])
	}

	b.WriteString("# HELP goorm_rpc_http_request_errors_total Total relayed HTTP request errors.\n")
	b.WriteString("# TYPE goorm_rpc_http_request_errors_total counter\n")
	for _, key := range sortedMetricKeys(m.errorsTotal) {
		fmt.Fprintf(&b, "goorm_rpc_http_request_errors_total{method=%q,error_kind=%q} %d\n", key.Method, key.ErrorKind, m.errorsTotal[key])
	}

	b.WriteString("# HELP goorm_rpc_http_request_duration_seconds Relayed HTTP request duration.\n")
	b.WriteString("# TYPE goorm_rpc_http_request_duration_seconds summary\n")
	for _, method := range sortedStringKeys(m.durationCount) {
		fmt.Fprintf(&b, "goorm_rpc_http_request_duration_seconds_sum{method=%q} %.9f\n", method, m.durationSecondsSum[method])
		fmt.Fprintf(&b, "goorm_rpc_http_request_duration_seconds_count{method=%q} %d\n", method, m.durationCount[method])
	}

	b.WriteString("# HELP goorm_rpc_http_request_body_bytes_total Total relayed HTTP request body bytes.\n")
	b.WriteString("# TYPE goorm_rpc_http_request_body_bytes_total counter\n")
	for _, method := range sortedStringKeys(m.requestBodyBytes) {
		fmt.Fprintf(&b, "goorm_rpc_http_request_body_bytes_total{method=%q} %d\n", method, m.requestBodyBytes[method])
	}

	b.WriteString("# HELP goorm_rpc_http_response_body_bytes_total Total relayed HTTP response body bytes.\n")
	b.WriteString("# TYPE goorm_rpc_http_response_body_bytes_total counter\n")
	for _, method := range sortedStringKeys(m.responseBodyBytes) {
		fmt.Fprintf(&b, "goorm_rpc_http_response_body_bytes_total{method=%q} %d\n", method, m.responseBodyBytes[method])
	}

	return b.String()
}

func sortedMetricKeys(values map[relayMetricKey]uint64) []relayMetricKey {
	keys := make([]relayMetricKey, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Method != keys[j].Method {
			return keys[i].Method < keys[j].Method
		}
		if keys[i].StatusClass != keys[j].StatusClass {
			return keys[i].StatusClass < keys[j].StatusClass
		}
		return keys[i].ErrorKind < keys[j].ErrorKind
	})
	return keys
}

func sortedStringKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
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

func (s *GoormRpcServer) nextRequestID() string {
	seq := s.seq.Add(1)
	var buf [4]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("relay-%d", seq)
	}
	return fmt.Sprintf("relay-%d-%s", seq, hex.EncodeToString(buf[:]))
}

func applyURLParams(rawURL string, params map[string]string) string {
	for key, value := range params {
		rawURL = strings.ReplaceAll(rawURL, "{"+key+"}", url.PathEscape(value))
	}
	return rawURL
}

func validateRelayURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid url: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("unsupported url scheme %q", parsed.Scheme)
	}
	if parsed.Host == "" {
		return fmt.Errorf("url host must not be empty")
	}
	return nil
}

func validateRequestBodySize(body []byte) error {
	if int64(len(body)) > maxRequestBodyBytes {
		return fmt.Errorf("request body exceeds %d bytes", maxRequestBodyBytes)
	}
	return nil
}

func readResponseBody(body io.Reader) ([]byte, error) {
	limited := io.LimitReader(body, maxResponseBodyBytes+1)
	responseBody, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if len(responseBody) > maxResponseBodyBytes {
		return nil, fmt.Errorf("response body exceeds %d bytes", maxResponseBodyBytes)
	}
	return responseBody, nil
}

func normalizeMethod(method string) string {
	return strings.ToUpper(strings.TrimSpace(method))
}

func statusClass(code int) string {
	switch {
	case code >= 100 && code < 200:
		return "informational"
	case code >= 200 && code < 300:
		return "success"
	case code >= 300 && code < 400:
		return "redirect"
	case code >= 400 && code < 500:
		return "client_error"
	case code >= 500 && code < 600:
		return "server_error"
	default:
		return "unknown"
	}
}

func errorKind(err error) string {
	if err == nil {
		return ""
	}
	if strings.Contains(err.Error(), "context deadline exceeded") {
		return "timeout"
	}
	if strings.Contains(err.Error(), "request body exceeds") || strings.Contains(err.Error(), "response body exceeds") {
		return "body_large"
	}
	if strings.Contains(err.Error(), "unsupported url scheme") || strings.Contains(err.Error(), "url host must not be empty") || strings.Contains(err.Error(), "timeout_ms") {
		return "validation"
	}
	return "upstream"
}

func safeLogURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	if parsed.User != nil {
		parsed.User = url.UserPassword("<redacted>", "<redacted>")
	}
	return parsed.String()
}

func responseHeaderMap(headers http.Header) map[string]string {
	responseHeaders := make(map[string]string)
	for key, values := range headers {
		if len(values) > 0 {
			responseHeaders[key] = values[0]
		}
	}
	return responseHeaders
}

func responseCookieMap(cookies []*http.Cookie) map[string]string {
	responseCookies := make(map[string]string)
	for _, cookie := range cookies {
		responseCookies[cookie.Name] = cookie.Value
	}
	return responseCookies
}

func httpDo(ctx context.Context, s *GoormRpcServer, req *HttpRequest, method string, requestBody io.Reader) (*HttpResponse, error) {
	start := time.Now()
	requestID := s.nextRequestID()
	method = normalizeMethod(method)
	var statusCode int
	var requestBytes int64
	var responseBytes int64
	var relayErr error
	defer func() {
		globalRelayMetrics.observe(method, statusCode, time.Since(start), requestBytes, responseBytes, relayErr)
	}()

	if req == nil {
		relayErr = fmt.Errorf("request must not be nil")
		log.Printf("Relay end - ID=%s Method=%s Duration=%s ErrorKind=%s Error=%v", requestID, method, time.Since(start), errorKind(relayErr), relayErr)
		return nil, relayErr
	}

	targetURL := applyURLParams(req.Url, req.Params)
	log.Printf("Relay start - ID=%s Method=%s URL=%s Query=%v", requestID, method, safeLogURL(targetURL), req.Query)

	if req.TimeoutMs < 0 {
		relayErr = fmt.Errorf("timeout_ms must not be negative")
		log.Printf("Relay end - ID=%s Method=%s URL=%s Duration=%s ErrorKind=%s Error=%v", requestID, method, safeLogURL(targetURL), time.Since(start), errorKind(relayErr), relayErr)
		return nil, relayErr
	}
	if err := validateRelayURL(targetURL); err != nil {
		relayErr = err
		log.Printf("Relay end - ID=%s Method=%s URL=%s Duration=%s ErrorKind=%s Error=%v", requestID, method, safeLogURL(targetURL), time.Since(start), errorKind(relayErr), relayErr)
		return nil, relayErr
	}

	if requestBody != nil {
		body, err := io.ReadAll(requestBody)
		if err != nil {
			relayErr = fmt.Errorf("failed to read HTTP %s request body: %v", method, err)
			log.Printf("Relay end - ID=%s Method=%s URL=%s Duration=%s ErrorKind=%s Error=%v", requestID, method, safeLogURL(targetURL), time.Since(start), errorKind(relayErr), relayErr)
			return nil, relayErr
		}
		requestBytes = int64(len(body))
		if err := validateRequestBodySize(body); err != nil {
			relayErr = err
			log.Printf("Relay end - ID=%s Method=%s URL=%s Duration=%s RequestBytes=%d ErrorKind=%s Error=%v", requestID, method, safeLogURL(targetURL), time.Since(start), requestBytes, errorKind(relayErr), relayErr)
			return nil, relayErr
		}
		requestBody = bytes.NewReader(body)
	}

	timeout := defaultRequestTimeout
	if req.TimeoutMs > 0 {
		timeout = time.Duration(req.TimeoutMs) * time.Millisecond
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, method, targetURL, requestBody)
	if err != nil {
		relayErr = fmt.Errorf("failed to create HTTP %s request: %v", method, err)
		log.Printf("Relay end - ID=%s Method=%s URL=%s Duration=%s ErrorKind=%s Error=%v", requestID, method, safeLogURL(targetURL), time.Since(start), errorKind(relayErr), relayErr)
		return nil, relayErr
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
		relayErr = fmt.Errorf("failed to perform HTTP %s request: %v", method, err)
		log.Printf("Relay end - ID=%s Method=%s URL=%s Duration=%s RequestBytes=%d ErrorKind=%s Error=%v", requestID, method, safeLogURL(targetURL), time.Since(start), requestBytes, errorKind(relayErr), relayErr)
		return nil, relayErr
	}
	defer resp.Body.Close()
	statusCode = resp.StatusCode

	responseBody, err := readResponseBody(resp.Body)
	if err != nil {
		relayErr = fmt.Errorf("failed to read HTTP response body: %v", err)
		log.Printf("Relay end - ID=%s Method=%s URL=%s Status=%d Class=%s Duration=%s RequestBytes=%d ErrorKind=%s Error=%v", requestID, method, safeLogURL(targetURL), statusCode, statusClass(statusCode), time.Since(start), requestBytes, errorKind(relayErr), relayErr)
		return nil, relayErr
	}
	responseBytes = int64(len(responseBody))

	log.Printf("Relay end - ID=%s Method=%s URL=%s Status=%d Class=%s Duration=%s RequestBytes=%d ResponseBytes=%d", requestID, method, safeLogURL(targetURL), statusCode, statusClass(statusCode), time.Since(start), requestBytes, responseBytes)

	return &HttpResponse{
		StatusCode: int32(resp.StatusCode),
		Headers:    responseHeaderMap(resp.Header),
		Cookies:    responseCookieMap(resp.Cookies()),
		Body:       responseBody,
	}, nil
}

func (s *GoormRpcServer) HttpGet(ctx context.Context, req *HttpRequest) (*HttpResponse, error) {
	if len(req.GetBody()) > 0 {
		return nil, fmt.Errorf("GET request body is not supported")
	}
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
