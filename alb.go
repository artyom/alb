// Package alb provides adapter enabling usage of http.Handler inside AWS Lambda
// running behind AWS ALB as described here:
// https://docs.aws.amazon.com/lambda/latest/dg/services-alb.html
//
// Usage example:
//
//	package main
//
//	import (
//		"fmt"
//		"net/http"
//
//		"github.com/artyom/alb"
//		"github.com/aws/aws-lambda-go/lambda"
//	)
//
//	func main() { lambda.Start(alb.Handler(http.HandlerFunc(hello))) }
//
//	func hello(w http.ResponseWriter, r *http.Request) {
//		fmt.Fprintln(w, "Hello from AWS Lambda behind ALB")
//	}
//
// Note: since both request and reply to/from AWS Lambda are passed as
// json-encoded payloads, their sizes are limited. AWS documentation states
// that: "The maximum size of the request body that you can send to a Lambda
// function is 1 MB. [...] The maximum size of the response JSON that the Lambda
// function can send is 1 MB." Exact limit of response size also depends on
// whether its body is valid utf8 or not, as non-utf8 payloads are transparently
// base64-encoded, which adds some overhead.
//
// For further details see
// https://docs.aws.amazon.com/elasticloadbalancing/latest/application/lambda-functions.html
package alb

import (
	"bytes"
	"context"
	"encoding/base64"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"unicode/utf8"
)

// Handler returns function suitable to use as an AWS Lambda handler with
// github.com/aws/aws-lambda-go/lambda package.
//
// Note that request is fully cached in memory.
func Handler(h http.Handler) func(context.Context, request) (*response, error) {
	if h == nil {
		panic("Wrap called with nil handler")
	}
	hh := lambdaHandler{handler: h}
	return hh.Run
}

type request struct {
	Method      string            `json:"httpMethod"`
	Path        string            `json:"path"`                  // escaped
	Query       map[string]string `json:"queryStringParameters"` // escaped
	Headers     map[string]string `json:"headers"`
	Body        string            `json:"body"`
	BodyEncoded bool              `json:"isBase64Encoded"`
}

type response struct {
	StatusCode  int               `json:"statusCode"`
	Status      string            `json:"statusDescription"`
	Headers     map[string]string `json:"headers"`
	Body        string            `json:"body"`
	BodyEncoded bool              `json:"isBase64Encoded"`
}

type lambdaHandler struct {
	handler http.Handler
}

func (h *lambdaHandler) Run(ctx context.Context, req request) (*response, error) {
	u, err := buildURL(req.Path, req.Query)
	if err != nil {
		return nil, err
	}
	headers := make(http.Header, len(req.Headers))
	for k, v := range req.Headers {
		headers.Set(k, v)
	}
	r := &http.Request{
		ProtoMajor: 1,
		ProtoMinor: 1,
		Proto:      "HTTP/1.1",
		Method:     req.Method,
		URL:        u,
		Header:     headers,
		Host:       headers.Get("Host"),
	}
	r = r.WithContext(ctx)
	switch {
	case req.BodyEncoded:
		b, err := base64.StdEncoding.DecodeString(req.Body)
		if err != nil {
			return nil, err
		}
		r.Body = ioutil.NopCloser(bytes.NewReader(b))
		r.ContentLength = int64(len(b))
	default:
		r.Body = ioutil.NopCloser(strings.NewReader(req.Body))
		r.ContentLength = int64(len(req.Body))
	}
	recorder := httptest.NewRecorder()
	h.handler.ServeHTTP(recorder, r)
	res := recorder.Result()
	out := &response{
		StatusCode: res.StatusCode,
		Status:     res.Status,
		Headers:    make(map[string]string, len(res.Header)),
	}
	for k, vv := range res.Header {
		out.Headers[k] = strings.Join(vv, ",")
	}
	if b := recorder.Body.Bytes(); utf8.Valid(b) {
		out.Body = recorder.Body.String()
	} else {
		out.Body = base64.StdEncoding.EncodeToString(b)
		out.BodyEncoded = true
	}
	return out, nil
}

// buildURL constructs url from already escaped path and query string parameters
// minimizing allocations and escaping overhead.
func buildURL(path string, query map[string]string) (*url.URL, error) {
	if len(query) == 0 {
		return url.Parse(path)
	}
	var b strings.Builder
	b.WriteString(path)
	b.WriteByte('?')
	var i int
	for k, v := range query {
		if i != 0 {
			b.WriteByte('&')
		}
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(v)
		i++
	}
	return url.Parse(b.String())
}
