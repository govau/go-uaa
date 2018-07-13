package uaa

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/cloudfoundry-community/go-uaa/passwordcredentials"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
)

//go:generate go run ./generator/generator.go

// API is a client to the UAA API.
type API struct {
	AuthenticatedClient   *http.Client
	UnauthenticatedClient *http.Client
	TargetURL             *url.URL
	SkipSSLValidation     bool
	Verbose               bool
	ZoneID                string
}

// TokenFormat is the format of a token.
type TokenFormat int

// Valid TokenFormat values.
const (
	OpaqueToken TokenFormat = iota
	JSONWebToken
)

func (t TokenFormat) String() string {
	if t == OpaqueToken {
		return "opaque"
	}
	if t == JSONWebToken {
		return "jwt"
	}
	return ""
}

type tokenTransport struct {
	underlyingTransport *http.Transport
	token               oauth2.Token
}

func (t *tokenTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set("Authorization", fmt.Sprintf("%s %s", t.token.Type(), t.token.AccessToken))
	return t.underlyingTransport.RoundTrip(req)
}

// NewWithToken builds an API that uses the given token to make authenticated
// requests to the UAA API.
func NewWithToken(target string, zoneID string, token oauth2.Token) (*API, error) {
	if token.AccessToken == "" || token.Expiry.Before(time.Now()) {
		return nil, errors.New("must supply a valid token")
	}
	u, err := BuildTargetURL(target)
	if err != nil {
		return nil, err
	}

	tokenClient := &http.Client{
		Transport: &tokenTransport{
			underlyingTransport: &http.Transport{
				Proxy: http.ProxyFromEnvironment,
				DialContext: (&net.Dialer{
					Timeout:   30 * time.Second,
					KeepAlive: 30 * time.Second,
					DualStack: true,
				}).DialContext,
				MaxIdleConns:          100,
				IdleConnTimeout:       90 * time.Second,
				TLSHandshakeTimeout:   10 * time.Second,
				ExpectContinueTimeout: 1 * time.Second,
			},
			token: token,
		},
	}

	client := &http.Client{Transport: http.DefaultTransport}
	return &API{
		UnauthenticatedClient: client,
		AuthenticatedClient:   tokenClient,
		TargetURL:             u,
		ZoneID:                zoneID,
	}, nil
}

// NewWithClientCredentials builds an API that uses the client credentials grant
// to get a token for use with the UAA API.
func NewWithClientCredentials(target string, zoneID string, clientID string, clientSecret string, tokenFormat TokenFormat) (*API, error) {
	u, err := BuildTargetURL(target)
	if err != nil {
		return nil, err
	}

	tokenURL := urlWithPath(*u, "/oauth/token")
	v := url.Values{}
	v.Add("token_format", tokenFormat.String())
	c := &clientcredentials.Config{
		ClientID:       clientID,
		ClientSecret:   clientSecret,
		TokenURL:       tokenURL.String(),
		EndpointParams: v,
	}
	client := &http.Client{Transport: http.DefaultTransport}
	return &API{
		UnauthenticatedClient: client,
		AuthenticatedClient:   c.Client(context.WithValue(context.Background(), oauth2.HTTPClient, client)),
		TargetURL:             u,
		ZoneID:                zoneID,
	}, nil
}

// NewWithPasswordCredentials builds an API that uses the password credentials
// grant to get a token for use with the UAA API.
func NewWithPasswordCredentials(target string, zoneID string, clientID string, clientSecret string, username string, password string, tokenFormat TokenFormat) (*API, error) {
	u, err := BuildTargetURL(target)
	if err != nil {
		return nil, err
	}

	tokenURL := urlWithPath(*u, "/oauth/token")
	v := url.Values{}
	v.Add("token_format", tokenFormat.String())
	c := &passwordcredentials.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Username:     username,
		Password:     password,
		Endpoint: oauth2.Endpoint{
			TokenURL: tokenURL.String(),
		},
		EndpointParams: v,
	}
	client := &http.Client{Transport: http.DefaultTransport}
	return &API{
		UnauthenticatedClient: client,
		AuthenticatedClient:   c.Client(context.WithValue(context.Background(), oauth2.HTTPClient, client)),
		TargetURL:             u,
		ZoneID:                zoneID,
	}, nil
}

// NewWithAuthorizationCode builds an API that uses the authorization code
// grant to get a token for use with the UAA API.

func NewWithAuthorizationCode(target string, zoneID string, clientID string, clientSecret string, code string, skipSSLValidation bool, tokenFormat TokenFormat) (*API, error) {
	url, err := BuildTargetURL(target)
	if err != nil {
		return nil, err
	}

	tokenURL := urlWithPath(*url, "/oauth/token")
	c := &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Endpoint: oauth2.Endpoint{
			TokenURL: tokenURL.String(),
		},
	}

	client := &http.Client{Transport: http.DefaultTransport}
	a := &API{
		UnauthenticatedClient: client,
		TargetURL:             url,
		SkipSSLValidation:     skipSSLValidation,
		ZoneID:                zoneID,
	}
	a.ensureTransport(a.UnauthenticatedClient)
	ctx := context.WithValue(context.Background(), oauth2.HTTPClient, a.UnauthenticatedClient)
	tokenFormatParam := oauth2.SetAuthURLParam("token_format", tokenFormat.String())
	responseTypeParam := oauth2.SetAuthURLParam("response_type", "token")

	t, err := c.Exchange(ctx, code, tokenFormatParam, responseTypeParam)
	if err != nil {
		return nil, err
	}

	a.AuthenticatedClient = c.Client(ctx, t)

	return a, nil
}

// NewWithRefreshToken builds an API that uses the given refresh token to get an
// access token for use with the UAA API.
func NewWithRefreshToken(target string, zoneID string, clientID string, clientSecret string, refreshToken string, skipSSLValidation bool, tokenFormat TokenFormat) (*API, error) {
	url, err := BuildTargetURL(target)
	if err != nil {
		return nil, err
	}
	tokenURL := urlWithPath(*url, "/oauth/token")
	query := tokenURL.Query()
	query.Set("token_format", tokenFormat.String())
	tokenURL.RawQuery = query.Encode()

	c := &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Endpoint: oauth2.Endpoint{
			TokenURL: tokenURL.String(),
		},
	}

	a := &API{
		UnauthenticatedClient: &http.Client{Transport: http.DefaultTransport},
		TargetURL:             url,
		SkipSSLValidation:     skipSSLValidation,
		ZoneID:                zoneID,
	}

	ctx := context.WithValue(context.Background(), oauth2.HTTPClient, a.UnauthenticatedClient)
	tokenSource := c.TokenSource(ctx, &oauth2.Token{
		RefreshToken: refreshToken,
	})

	token, err := tokenSource.Token()
	if err != nil {
		return nil, err
	}

	a.AuthenticatedClient = c.Client(ctx, token)

	return a, nil
}
