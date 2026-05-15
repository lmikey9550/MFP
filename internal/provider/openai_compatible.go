package provider

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"mfp/internal/config"
	"mfp/internal/core"
)

type AttemptRequest struct {
	Provider  core.ProviderConfig
	Candidate core.ActualModelRef
	Path      string
	Body      []byte
	Headers   http.Header
}

type AttemptResponse struct {
	StatusCode int
	Header     http.Header
	Body       []byte
	Stream     io.ReadCloser
}

type OpenAICompatible struct {
	client *http.Client
}

func NewOpenAICompatible() *OpenAICompatible {
	return &OpenAICompatible{
		client: &http.Client{},
	}
}

func (a *OpenAICompatible) Do(ctx context.Context, req AttemptRequest) (AttemptResponse, error) {
	timeout := time.Duration(req.Provider.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	client := *a.client
	client.Timeout = timeout

	target, err := joinURLPath(req.Provider.BaseURL, req.Path)
	if err != nil {
		return AttemptResponse{}, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(req.Body))
	if err != nil {
		return AttemptResponse{}, err
	}
	for key, values := range req.Headers {
		for _, value := range values {
			httpReq.Header.Add(key, value)
		}
	}
	if httpReq.Header.Get("Content-Type") == "" {
		httpReq.Header.Set("Content-Type", "application/json")
	}
	if credential := config.ResolveCredential(req.Provider); credential != "" {
		httpReq.Header.Set("Authorization", "Bearer "+credential)
	}
	for key, value := range req.Provider.HeadersTemplate {
		httpReq.Header.Set(key, value)
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		return AttemptResponse{}, err
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return AttemptResponse{
			StatusCode: resp.StatusCode,
			Header:     resp.Header.Clone(),
			Stream:     resp.Body,
		}, nil
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return AttemptResponse{
		StatusCode: resp.StatusCode,
		Header:     resp.Header.Clone(),
		Body:       body,
	}, nil
}

func joinURLPath(base string, reqPath string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(base))
	if err != nil {
		return "", err
	}
	basePath := parsed.Path
	cleanReqPath := reqPath
	if strings.HasSuffix(basePath, "/v1") && strings.HasPrefix(cleanReqPath, "/v1/") {
		cleanReqPath = strings.TrimPrefix(cleanReqPath, "/v1")
	}
	joined := path.Join(basePath, cleanReqPath)
	if !strings.HasPrefix(joined, "/") {
		joined = "/" + joined
	}
	parsed.Path = joined
	parsed.RawPath = joined
	return parsed.String(), nil
}
