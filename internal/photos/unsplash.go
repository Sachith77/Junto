package photos

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/junto/junto/internal/domain"
)

// Unsplash implements destination search using Unsplash's JSON API. Returned image URLs are
// intentionally hotlinked; copying the bytes into our bucket would violate their API rules.
type Unsplash struct {
	accessKey string
	client    *http.Client
}

func NewUnsplash(accessKey string) *Unsplash {
	return &Unsplash{accessKey: strings.TrimSpace(accessKey), client: &http.Client{Timeout: 8 * time.Second}}
}

type searchResponse struct {
	Results []struct {
		URLs struct {
			Regular string `json:"regular"`
		} `json:"urls"`
		Links struct {
			HTML             string `json:"html"`
			DownloadLocation string `json:"download_location"`
		} `json:"links"`
		User struct {
			Name  string `json:"name"`
			Links struct {
				HTML string `json:"html"`
			} `json:"links"`
		} `json:"user"`
	} `json:"results"`
}

func (u *Unsplash) Search(ctx context.Context, query string) (*domain.TripCoverSuggestion, error) {
	if u.accessKey == "" {
		return nil, nil
	}
	q := url.Values{}
	q.Set("query", query)
	q.Set("orientation", "landscape")
	q.Set("content_filter", "high")
	q.Set("per_page", "1")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.unsplash.com/search/photos?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Client-ID "+u.accessKey)
	req.Header.Set("Accept-Version", "v1")

	res, err := u.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("searching Unsplash: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("searching Unsplash: status %d", res.StatusCode)
	}
	var body searchResponse
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("decoding Unsplash search: %w", err)
	}
	if len(body.Results) == 0 {
		return nil, nil
	}
	p := body.Results[0]
	return &domain.TripCoverSuggestion{
		Found: true, ImageURL: p.URLs.Regular, PhotographerName: p.User.Name,
		PhotographerURL: referralURL(p.User.Links.HTML), PhotoURL: referralURL(p.Links.HTML),
		DownloadLocation: p.Links.DownloadLocation,
	}, nil
}

func (u *Unsplash) TrackSelection(ctx context.Context, location string) error {
	if u.accessKey == "" || location == "" {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, location, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Client-ID "+u.accessKey)
	req.Header.Set("Accept-Version", "v1")
	res, err := u.client.Do(req)
	if err != nil {
		return fmt.Errorf("tracking Unsplash selection: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("tracking Unsplash selection: status %d", res.StatusCode)
	}
	return nil
}

func referralURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	q := u.Query()
	q.Set("utm_source", "junto")
	q.Set("utm_medium", "referral")
	u.RawQuery = q.Encode()
	return u.String()
}
