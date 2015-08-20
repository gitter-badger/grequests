package grequests

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"net/http/cookiejar"

	"golang.org/x/net/publicsuffix"
)

const (
	// Default value for net.Dialer Timeout
	dialTimeout = 30 * time.Second

	// Default value for net.Dialer KeepAlive
	dialKeepAlive = 30 * time.Second

	// Default value for http.Transport TLSHandshakeTimeout
	tslHandshakeTimeout = 10 * time.Second
)

// RequestOptions is the location that of where the data
type RequestOptions struct {

	// Data is a map of key values that will eventually convert into the query string of a GET request or the
	// body of a POST request.
	Data map[string]string

	// Params is a map of query strings that may be used within a GET request
	Params map[string]string

	// Files is where you can include files to upload. The use of this data structure is limited to POST requests
	File *FileUpload

	// JSON can be used when you wish to send JSON within the request body
	JSON interface{}

	// XML can be used if you wish to send XML within the request body
	XML interface{}

	// If you want to add custom HTTP headers to the request, this is your friend
	Headers map[string]string

	// InsecureSkipVerify is a flag that specifies if we should validate the server's TLS certificate. It should be noted that
	// Go's TLS verify mechanism doesn't validate if a certificate has been revoked
	InsecureSkipVerify bool

	// DisableCompression will disable gzip compression on requests
	DisableCompression bool

	// UserAgent allows you to set an arbitrary custom user agent
	UserAgent string

	// Auth allows you to specify a user name and password that you wish to use when requesting
	// the URL. It will use basic HTTP authentication formatting the username and password in base64
	// the format is []string{username, password}
	Auth []string

	// IsAjax is a flag that can be set to make the request appear to be generated by browser Javascript
	IsAjax bool

	// Cookies is an array of `http.Cookie` that allows you to attach cookies to your request
	Cookies []http.Cookie

	// UseCookieJar will create a custom HTTP client that will process and store HTTP cookies when they are sent down
	UseCookieJar bool

	// Proxies is a map in the following format *protocol* => proxy address e.g http => http://127.0.0.1:8080
	Proxies map[string]*url.URL

	// TLSHandshakeTimeout specifies the maximum amount of time waiting to
	// wait for a TLS handshake. Zero means no timeout.
	TLSHandshakeTimeout time.Duration

	// DialTimeout is the maximum amount of time a dial will wait for
	// a connect to complete.
	DialTimeout time.Duration

	// KeepAlive specifies the keep-alive period for an active
	// network connection. If zero, keep-alive are not enabled.
	DialKeepAlive time.Duration

	// HTTPClient can be provided if you wish to supply a custom HTTP client
	// this is useful if you want to use an OAUTH client with your request
	HTTPClient *http.Client
}

func doRegularRequest(requestVerb, url string, ro *RequestOptions) (*Response, error) {
	return buildResponse(buildRequest(requestVerb, url, ro, nil))
}

func doSessionRequest(requestVerb, url string, ro *RequestOptions, httpClient *http.Client) (*Response, error) {
	return buildResponse(buildRequest(requestVerb, url, ro, httpClient))
}

// buildRequest is where most of the magic happens for request processing
func buildRequest(httpMethod, url string, ro *RequestOptions, httpClient *http.Client) (*http.Response, error) {
	if ro == nil {
		ro = &RequestOptions{}
	}
	// Create our own HTTP client

	if httpClient == nil {
		httpClient = BuildHTTPClient(*ro)
	}

	// Build our URL

	var (
		err error
	)

	if len(ro.Params) != 0 {
		if url, err = buildURLParams(url, ro.Params); err != nil {
			return nil, err
		}
	}

	// Build the request
	req, err := buildHTTPRequest(httpMethod, url, ro)

	if err != nil {
		return nil, err
	}

	// Do we need to add any HTTP headers or Basic Auth?
	addHTTPHeaders(ro, req)
	addCookies(ro, req)

	return httpClient.Do(req)
}

func buildHTTPRequest(httpMethod, userURL string, ro *RequestOptions) (*http.Request, error) {
	if ro.JSON != nil {
		return createBasicJSONRequest(httpMethod, userURL, ro)
	}

	if ro.XML != nil {
		return createBasicXMLRequest(httpMethod, userURL, ro)
	}

	if ro.File != nil {
		return createFileUploadRequest(httpMethod, userURL, ro)
	}

	if ro.Data != nil {
		return createBasicRequest(httpMethod, userURL, ro)
	}

	return http.NewRequest(httpMethod, userURL, nil)
}

func createFileUploadRequest(httpMethod, userURL string, ro *RequestOptions) (*http.Request, error) {
	if httpMethod == "POST" {
		return createMultiPartPostRequest(httpMethod, userURL, ro)
	}

	// This may be a PUT or PATCH request so we will just put the raw
	// io.ReadCloser in the request body
	// and guess the MIME type from the file name

	// At the moment, we will only support 1 file upload as a time
	// when uploading using PUT or PATCH

	req, err := http.NewRequest(httpMethod, userURL, ro.File.FileContents)

	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", mime.TypeByExtension(ro.File.FileName))

	return req, nil

}

func createBasicXMLRequest(httpMethod, userURL string, ro *RequestOptions) (*http.Request, error) {
	tempBuffer := &bytes.Buffer{}

	if err := xml.NewEncoder(tempBuffer).Encode(ro.XML); err != nil {
		return nil, err
	}

	req, err := http.NewRequest(httpMethod, userURL, tempBuffer)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/xml")

	return req, nil

}
func createMultiPartPostRequest(httpMethod, userURL string, ro *RequestOptions) (*http.Request, error) {
	requestBody := &bytes.Buffer{}

	multipartWriter := multipart.NewWriter(requestBody)
	writer, err := multipartWriter.CreateFormFile("file", ro.File.FileName)

	if err != nil {
		return nil, err
	}

	if ro.File.FileContents == nil {
		return nil, errors.New("grequests: Pointer FileContents cannot be nil")
	}

	if _, err = io.Copy(writer, ro.File.FileContents); err != nil && err != io.EOF {
		return nil, err
	}

	defer ro.File.FileContents.Close()

	// Populate the other parts of the form (if there are any)
	for key, value := range ro.Data {
		multipartWriter.WriteField(key, value)
	}

	if err = multipartWriter.Close(); err != nil {
		return nil, err
	}

	req, err := http.NewRequest(httpMethod, userURL, requestBody)

	if err != nil {
		return nil, err
	}

	req.Header.Add("Content-Type", multipartWriter.FormDataContentType())

	return req, err
}

func createBasicJSONRequest(httpMethod, userURL string, ro *RequestOptions) (*http.Request, error) {

	tempBuffer := &bytes.Buffer{}

	if err := json.NewEncoder(tempBuffer).Encode(ro.JSON); err != nil {
		return nil, err
	}

	req, err := http.NewRequest(httpMethod, userURL, tempBuffer)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")

	return req, nil

}
func createBasicRequest(httpMethod, userURL string, ro *RequestOptions) (*http.Request, error) {

	req, err := http.NewRequest(httpMethod, userURL, strings.NewReader(encodePostValues(ro.Data)))

	if err != nil {
		return nil, err
	}

	// The content type must be set to a regular form
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	return req, nil
}

func encodePostValues(postValues map[string]string) string {
	urlValues := &url.Values{}

	for key, value := range postValues {
		urlValues.Set(key, value)
	}

	return urlValues.Encode() // This will sort all of the string values
}

// proxySettings will default to the default proxy settings if none are provided
// if settings are provided – they will override the environment variables
func (ro RequestOptions) proxySettings(req *http.Request) (*url.URL, error) {
	// No proxies – lets use the default
	if len(ro.Proxies) == 0 {
		return http.ProxyFromEnvironment(req)
	}

	// There was a proxy specified – do we support the protocol?
	if _, ok := ro.Proxies[req.URL.Scheme]; ok {
		return ro.Proxies[req.URL.Scheme], nil
	}

	// Proxies were specified but not for any protocol that we use
	return http.ProxyFromEnvironment(req)

}

// DontUseDefaultClient will tell the "client creator" if a custom client is needed
// it checks the following items (and will create a custom client of these are)
// true
// 1. Do we want to accept invalid SSL certificates?
// 2. Do we want to disable compression?
// 3. Do we want a custom proxy?
// 4. Do we want to change the default timeout for TLS Handshake?
// 5. Do we want to change the default request timeout?
// 6. Do we want to change the default connection timeout?
func (ro RequestOptions) DontUseDefaultClient() bool {
	return ro.InsecureSkipVerify == true ||
		ro.DisableCompression == true ||
		len(ro.Proxies) != 0 ||
		ro.TLSHandshakeTimeout != 0 ||
		ro.DialTimeout != 0 ||
		ro.DialKeepAlive != 0 ||
		len(ro.Cookies) != 0 ||
		ro.UseCookieJar != false
}

// BuildHTTPClient is a function that will return a custom HTTP client based on the request options provided
// the check is in UseDefaultClient
func BuildHTTPClient(ro RequestOptions) *http.Client {

	if ro.HTTPClient != nil {
		return ro.HTTPClient
	}

	// Does the user want to change the defaults?
	if !ro.DontUseDefaultClient() {
		return http.DefaultClient
	}

	// Using the user config for tls timeout or default
	if ro.TLSHandshakeTimeout == 0 {
		ro.TLSHandshakeTimeout = tslHandshakeTimeout
	}

	// Using the user config for dial timeout or default
	if ro.DialTimeout == 0 {
		ro.DialTimeout = dialTimeout
	}

	// Using the user config for dial keep alive or default
	if ro.DialKeepAlive == 0 {
		ro.DialKeepAlive = dialKeepAlive
	}

	// The function does not return an error ever... so we are just ignoring it
	cookieJar, _ := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})

	return &http.Client{
		Jar: cookieJar,
		Transport: &http.Transport{
			// These are borrowed from the default transporter
			Proxy: ro.proxySettings,
			Dial: (&net.Dialer{
				Timeout:   ro.DialTimeout,
				KeepAlive: ro.DialKeepAlive,
			}).Dial,
			TLSHandshakeTimeout: ro.TLSHandshakeTimeout,

			// Here comes the user settings
			TLSClientConfig:    &tls.Config{InsecureSkipVerify: ro.InsecureSkipVerify},
			DisableCompression: ro.DisableCompression,
		},
	}
}

// buildURLParams returns a URL with all of the params
// Note: This function will override current URL params if they contradict what is provided in the map
// That is what the "magic" is on the last line
func buildURLParams(userURL string, params map[string]string) (string, error) {
	parsedURL, err := url.Parse(userURL)

	if err != nil {
		return "", err
	}

	parsedQuery, err := url.ParseQuery(parsedURL.RawQuery)

	for key, value := range params {
		parsedQuery.Set(key, value)
	}

	return strings.Join(
		[]string{strings.Replace(parsedURL.String(),
			"?"+parsedURL.RawQuery, "", -1),
			parsedQuery.Encode()},
		"?"), nil
}

// addHTTPHeaders adds any additional HTTP headers that need to be added are added here including:
// 1. Custom User agent
// 2. Authorization Headers
// 3. Any other header requested
func addHTTPHeaders(ro *RequestOptions, req *http.Request) {
	for key, value := range ro.Headers {
		req.Header.Set(key, value)
	}

	if ro.UserAgent != "" {
		req.Header.Set("User-Agent", ro.UserAgent)
	} else {
		req.Header.Set("User-Agent", localUserAgent)
	}

	if ro.Auth != nil {
		req.SetBasicAuth(ro.Auth[0], ro.Auth[1])
	}

	if ro.IsAjax == true {
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
	}
}

func addCookies(ro *RequestOptions, req *http.Request) {
	for _, c := range ro.Cookies {
		req.AddCookie(&c)
	}
}
