package tools

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	defaultWebFetchTimeoutSeconds = 20
	maxWebFetchTimeoutSeconds     = 60
	maxWebFetchRequestBytes       = 256 * 1024
	defaultWebFetchResponseBytes  = 128 * 1024
	maxWebFetchResponseBytes      = 512 * 1024
)

var webFetchAllowedMethods = []string{
	http.MethodGet,
	http.MethodHead,
	http.MethodPost,
	http.MethodPut,
	http.MethodPatch,
	http.MethodDelete,
}

func init() {
	Register(ToolFunc{
		Meta: Metadata{
			Name:        "web_fetch",
			Description: "Fetch a direct HTTP or HTTPS endpoint. Use this for known URLs or API endpoints when web_search is unnecessary or after web_search returns a URL.",
			Parameters: Schema{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []any{"url"},
				"properties": map[string]any{
					"url": map[string]any{
						"type":        "string",
						"description": "Absolute HTTP or HTTPS URL to request.",
					},
					"method": map[string]any{
						"type":        "string",
						"description": "HTTP method. Defaults to GET. Allowed values: GET, HEAD, POST, PUT, PATCH, DELETE.",
						"enum":        []any{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE"},
					},
					"headers": map[string]any{
						"type":                 "object",
						"description":          "Optional request headers. Header values must be strings.",
						"additionalProperties": map[string]any{"type": "string"},
					},
					"body": map[string]any{
						"type":        "string",
						"description": "Optional request body for POST, PUT, PATCH, or DELETE. Capped at 256 KiB.",
					},
					"timeoutSeconds": map[string]any{
						"type":        "integer",
						"description": "Request timeout in seconds. Defaults to 20 and is capped at 60.",
						"minimum":     1,
						"maximum":     maxWebFetchTimeoutSeconds,
					},
					"maxBytes": map[string]any{
						"type":        "integer",
						"description": "Maximum response body bytes to return. Defaults to 128 KiB and is capped at 512 KiB.",
						"minimum":     1,
						"maximum":     maxWebFetchResponseBytes,
					},
				},
			},
		},
		Run: webFetch,
	})
	Register(ToolFunc{
		Meta: Metadata{
			Name:        "web_read",
			Description: "Read a public HTTP or HTTPS URL with GET or HEAD. This read-only fetch cannot send a body, custom headers, or mutating methods.",
			Parameters: Schema{
				"type": "object", "additionalProperties": false, "required": []any{"url"},
				"properties": map[string]any{
					"url":            map[string]any{"type": "string", "description": "Absolute HTTP or HTTPS URL."},
					"method":         map[string]any{"type": "string", "enum": []any{"GET", "HEAD"}, "description": "Defaults to GET."},
					"timeoutSeconds": map[string]any{"type": "integer", "minimum": 1, "maximum": maxWebFetchTimeoutSeconds},
					"maxBytes":       map[string]any{"type": "integer", "minimum": 1, "maximum": maxWebFetchResponseBytes},
				},
			},
		},
		Run: webRead,
	})
}

func webRead(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
	var args struct {
		URL            string `json:"url"`
		Method         string `json:"method"`
		TimeoutSeconds int    `json:"timeoutSeconds"`
		MaxBytes       int    `json:"maxBytes"`
	}
	if err := DecodeToolArguments(arguments, &args); err != nil {
		return nil, SafeError{Code: "invalid_arguments", Message: "arguments must be valid JSON"}
	}
	method := strings.ToUpper(strings.TrimSpace(args.Method))
	if method == "" {
		method = http.MethodGet
	}
	if method != http.MethodGet && method != http.MethodHead {
		return nil, SafeError{Code: "invalid_arguments", Message: "method must be GET or HEAD"}
	}
	payload, _ := json.Marshal(webFetchArgs{URL: args.URL, Method: method, TimeoutSeconds: args.TimeoutSeconds, MaxBytes: args.MaxBytes})
	return webFetch(ctx, payload)
}

type webFetchArgs struct {
	URL            string            `json:"url"`
	Method         string            `json:"method"`
	Headers        map[string]string `json:"headers"`
	Body           string            `json:"body"`
	TimeoutSeconds int               `json:"timeoutSeconds"`
	MaxBytes       int               `json:"maxBytes"`
}

type webFetchOutput struct {
	URL             string              `json:"url"`
	FinalURL        string              `json:"finalUrl,omitempty"`
	Method          string              `json:"method"`
	StatusCode      int                 `json:"statusCode"`
	Status          string              `json:"status"`
	Headers         map[string][]string `json:"headers,omitempty"`
	ContentType     string              `json:"contentType,omitempty"`
	Body            string              `json:"body,omitempty"`
	BodyBase64      string              `json:"bodyBase64,omitempty"`
	BodyEncoding    string              `json:"bodyEncoding"`
	BytesRead       int                 `json:"bytesRead"`
	Truncated       bool                `json:"truncated"`
	RequestDuration int64               `json:"requestDurationMs"`
}

func webFetch(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
	if err := ctx.context().Err(); err != nil {
		return nil, err
	}
	var args webFetchArgs
	if len(arguments) > 0 {
		if err := DecodeToolArguments(arguments, &args); err != nil {
			return nil, SafeError{Code: "invalid_arguments", Message: "arguments must be valid JSON"}
		}
	}
	normalized, err := normalizeWebFetchArgs(args)
	if err != nil {
		return nil, err
	}

	var body io.Reader
	if normalized.Body != "" {
		body = strings.NewReader(normalized.Body)
	}
	request, err := http.NewRequestWithContext(ctx.context(), normalized.Method, normalized.URL, body)
	if err != nil {
		return nil, SafeError{Code: "invalid_arguments", Message: "failed to create request"}
	}
	for name, value := range normalized.Headers {
		request.Header.Set(name, value)
	}
	if normalized.Body != "" && request.Header.Get("Content-Type") == "" {
		request.Header.Set("Content-Type", "text/plain; charset=utf-8")
	}

	timeout := time.Duration(normalized.TimeoutSeconds) * time.Second
	client := &http.Client{Timeout: timeout}
	start := time.Now()
	response, err := client.Do(request)
	if err != nil {
		if contextErr := ctx.context().Err(); contextErr != nil {
			return nil, contextErr
		}
		return nil, SafeError{Code: "fetch_failed", Message: err.Error()}
	}
	defer response.Body.Close()

	bodyBytes, truncated, err := readWebFetchBody(response.Body, normalized.MaxBytes)
	if err != nil {
		return nil, SafeError{Code: "fetch_failed", Message: "failed to read response body"}
	}
	contentType := strings.TrimSpace(response.Header.Get("Content-Type"))
	output := webFetchOutput{
		URL:             normalized.URL,
		Method:          normalized.Method,
		StatusCode:      response.StatusCode,
		Status:          response.Status,
		Headers:         cloneHTTPHeaders(response.Header),
		ContentType:     contentType,
		BodyEncoding:    "utf-8",
		BytesRead:       len(bodyBytes),
		Truncated:       truncated,
		RequestDuration: time.Since(start).Milliseconds(),
	}
	if response.Request != nil && response.Request.URL != nil && response.Request.URL.String() != normalized.URL {
		output.FinalURL = response.Request.URL.String()
	}
	if len(bodyBytes) > 0 {
		if webFetchBodyIsText(contentType, bodyBytes) {
			output.Body = string(bodyBytes)
		} else {
			output.BodyEncoding = "base64"
			output.BodyBase64 = base64.StdEncoding.EncodeToString(bodyBytes)
		}
	}
	return output, nil
}

func normalizeWebFetchArgs(args webFetchArgs) (webFetchArgs, error) {
	args.URL = strings.TrimSpace(args.URL)
	if args.URL == "" {
		return args, SafeError{Code: "invalid_arguments", Message: "url is required"}
	}
	parsed, err := url.ParseRequestURI(args.URL)
	if err != nil || parsed.Host == "" {
		return args, SafeError{Code: "invalid_arguments", Message: "url must be an absolute HTTP or HTTPS URL"}
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return args, SafeError{Code: "invalid_arguments", Message: "url must use http or https"}
	}

	args.Method = strings.ToUpper(strings.TrimSpace(args.Method))
	if args.Method == "" {
		args.Method = http.MethodGet
	}
	if !slices.Contains(webFetchAllowedMethods, args.Method) {
		return args, SafeError{Code: "invalid_arguments", Message: "method must be one of GET, HEAD, POST, PUT, PATCH, or DELETE"}
	}
	if len(args.Body) > maxWebFetchRequestBytes {
		return args, SafeError{Code: "invalid_arguments", Message: "body exceeds 256 KiB"}
	}
	if args.Body != "" && (args.Method == http.MethodGet || args.Method == http.MethodHead) {
		return args, SafeError{Code: "invalid_arguments", Message: "body is only supported for POST, PUT, PATCH, or DELETE"}
	}
	if args.TimeoutSeconds <= 0 {
		args.TimeoutSeconds = defaultWebFetchTimeoutSeconds
	}
	if args.TimeoutSeconds > maxWebFetchTimeoutSeconds {
		args.TimeoutSeconds = maxWebFetchTimeoutSeconds
	}
	if args.MaxBytes <= 0 {
		args.MaxBytes = defaultWebFetchResponseBytes
	}
	if args.MaxBytes > maxWebFetchResponseBytes {
		args.MaxBytes = maxWebFetchResponseBytes
	}
	cleanHeaders, err := normalizeWebFetchHeaders(args.Headers)
	if err != nil {
		return args, err
	}
	args.Headers = cleanHeaders
	return args, nil
}

func normalizeWebFetchHeaders(headers map[string]string) (map[string]string, error) {
	if len(headers) == 0 {
		return nil, nil
	}
	clean := make(map[string]string, len(headers))
	for name, value := range headers {
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, SafeError{Code: "invalid_arguments", Message: "header names cannot be empty"}
		}
		if !httpgutsValidHeaderFieldName(name) {
			return nil, SafeError{Code: "invalid_arguments", Message: fmt.Sprintf("invalid header name %q", name)}
		}
		lower := strings.ToLower(name)
		if lower == "host" || lower == "content-length" {
			return nil, SafeError{Code: "invalid_arguments", Message: fmt.Sprintf("header %q cannot be set manually", name)}
		}
		if strings.ContainsAny(value, "\r\n") {
			return nil, SafeError{Code: "invalid_arguments", Message: fmt.Sprintf("invalid header value for %q", name)}
		}
		clean[name] = value
	}
	return clean, nil
}

func readWebFetchBody(reader io.Reader, maxBytes int) ([]byte, bool, error) {
	var buf bytes.Buffer
	limited := io.LimitReader(reader, int64(maxBytes)+1)
	if _, err := io.Copy(&buf, limited); err != nil {
		return nil, false, err
	}
	body := buf.Bytes()
	if len(body) <= maxBytes {
		return body, false, nil
	}
	return body[:maxBytes], true, nil
}

func cloneHTTPHeaders(headers http.Header) map[string][]string {
	if len(headers) == 0 {
		return nil
	}
	clone := make(map[string][]string, len(headers))
	for name, values := range headers {
		clone[name] = append([]string(nil), values...)
	}
	return clone
}

func webFetchBodyIsText(contentType string, body []byte) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err == nil {
		mediaType = strings.ToLower(mediaType)
		if strings.HasPrefix(mediaType, "text/") ||
			mediaType == "application/json" ||
			strings.HasSuffix(mediaType, "+json") ||
			mediaType == "application/xml" ||
			strings.HasSuffix(mediaType, "+xml") ||
			mediaType == "application/javascript" ||
			mediaType == "application/x-javascript" {
			return utf8.Valid(body)
		}
	}
	return utf8.Valid(body) && !bytes.Contains(body, []byte{0})
}

func httpgutsValidHeaderFieldName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		if r > 127 {
			return false
		}
		if !strings.ContainsRune("!#$%&'*+-.^_`|~0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ", r) {
			return false
		}
	}
	return true
}
