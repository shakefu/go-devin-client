package devincli

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

type sdkDispatcher interface {
	Do(context.Context, *http.Request) (*http.Response, error)
}

type sdkFactory func(settings, *http.Client) (sdkDispatcher, error)

func pendingSDKFactory(settings, *http.Client) (sdkDispatcher, error) {
	return nil, fmt.Errorf("SDK integration is pending: this command needs the merged New(Config) SDK adapter")
}

func requestHTTPClient(client *http.Client, timeout time.Duration) *http.Client {
	if client == nil {
		client = http.DefaultClient
	}
	configured := *client
	if timeout > 0 {
		configured.Timeout = timeout
	}
	configured.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &configured
}
