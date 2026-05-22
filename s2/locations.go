package s2

import (
	"context"
	"fmt"
)

type LocationsClient struct {
	client *Client
}

// List locations available to the account.
func (l *LocationsClient) List(ctx context.Context) ([]LocationInfo, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	return withRetries(ctx, l.client.retryConfig, l.client.logger, func() ([]LocationInfo, error) {
		httpClient := &httpClient{
			client:      l.client.httpClient,
			baseURL:     l.client.baseURL,
			accessToken: l.client.accessToken,
			logger:      l.client.logger,
			compression: l.client.compression,
		}

		var result []LocationInfo
		if err := httpClient.request(ctx, "GET", "/locations", nil, &result); err != nil {
			return nil, err
		}
		return result, nil
	})
}

// GetDefault returns the account's default location.
func (l *LocationsClient) GetDefault(ctx context.Context) (*LocationInfo, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	return withRetries(ctx, l.client.retryConfig, l.client.logger, func() (*LocationInfo, error) {
		httpClient := &httpClient{
			client:      l.client.httpClient,
			baseURL:     l.client.baseURL,
			accessToken: l.client.accessToken,
			logger:      l.client.logger,
			compression: l.client.compression,
		}

		var result LocationInfo
		if err := httpClient.request(ctx, "GET", "/locations/default", nil, &result); err != nil {
			return nil, err
		}
		return &result, nil
	})
}

// SetDefault sets the account's default location.
func (l *LocationsClient) SetDefault(ctx context.Context, location LocationName) (*LocationInfo, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if location == "" {
		return nil, fmt.Errorf("location name cannot be empty")
	}

	return withRetries(ctx, l.client.retryConfig, l.client.logger, func() (*LocationInfo, error) {
		httpClient := &httpClient{
			client:      l.client.httpClient,
			baseURL:     l.client.baseURL,
			accessToken: l.client.accessToken,
			logger:      l.client.logger,
			compression: l.client.compression,
		}

		var result LocationInfo
		if err := httpClient.request(ctx, "PUT", "/locations/default", location, &result); err != nil {
			return nil, err
		}
		return &result, nil
	})
}
