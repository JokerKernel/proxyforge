package app

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

func httpRequest(ctx context.Context, url string) (*http.Request, error) {
	return http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
}

type netHTTPClient struct{ timeout time.Duration }

func (c *netHTTPClient) do(req *http.Request) ([]byte, error) {
	client := &http.Client{Timeout: c.timeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("探测公网地址: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("探测公网地址返回 %s", resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 128))
}
