package waf

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/parthh37/nodehoster/internal/model"
)

// go test -bench . -benchmem ./internal/waf

// benchInspect inspects the same request over and over, with its body
// (if any) rewound each time.
func benchInspect(b *testing.B, e *Engine, mk func() *http.Request) {
	b.Helper()
	r := mk()
	var body []byte
	if r.Body != nil {
		body, _ = io.ReadAll(r.Body)
	}
	b.ReportAllocs()
	b.SetBytes(int64(len(body)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if body != nil {
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		e.Inspect(r)
		if r.Body != nil {
			r.Body.Close() // gives the buffered body's share of the budget back
		}
	}
}

// A browser's GET: path, a few query arguments, a dozen headers, six
// cookies.
func BenchmarkGET(b *testing.B) {
	benchInspect(b, pl1(), func() *http.Request {
		return browser("GET", "/products/shoes?page=2&sort=price_asc&q=running+shoes&size=42", "", "")
	})
}

func BenchmarkGETParanoia3(b *testing.B) {
	e := Compile(model.WAFConfig{Mode: model.WAFBlock, ParanoiaLevel: 3})
	benchInspect(b, e, func() *http.Request {
		return browser("GET", "/products/shoes?page=2&sort=price_asc&q=running+shoes&size=42", "", "")
	})
}

func BenchmarkJSONPost(b *testing.B) {
	body := `{"order":{"id":"ord_123","items":[{"sku":"A-1","qty":2,"price":19.99},{"sku":"B-2","qty":1,"price":5}],"shipping":{"name":"Siobhán O'Connor","street":"12 Main St.","city":"Dublin","notes":"Leave at door; ring bell 2x"},"coupon":null,"gift":true}}`
	benchInspect(b, pl1(), func() *http.Request { return browser("POST", "/api/orders", body, "application/json") })
}

func BenchmarkAttack(b *testing.B) {
	benchInspect(b, pl1(), func() *http.Request {
		return browser("GET", "/item?id="+q("-1' UNION SELECT username, password FROM users-- -"), "", "")
	})
}

// The worst case the limit allows: 128 KB of text dense with the
// literals that send values to the regular expressions.
func BenchmarkAdversarialBody(b *testing.B) {
	chunk := `select union from where or and ' " <script onload= ${ {{ ../ ; | && $( javascript: `
	body := strings.Repeat(chunk, (128<<10)/len(chunk))
	benchInspect(b, pl1(), func() *http.Request { return browser("POST", "/x", body, "text/plain") })
}

func BenchmarkLargeJSONBody(b *testing.B) {
	var sb strings.Builder
	sb.WriteString(`{"items":[`)
	for i := 0; sb.Len() < 120<<10; i++ {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(`{"id":12345,"name":"Product name with some words","description":"A fairly long description, with punctuation; and quotes' too.","tags":["a","b","c"]}`)
	}
	sb.WriteString(`]}`)
	body := sb.String()
	benchInspect(b, pl1(), func() *http.Request { return browser("POST", "/api/import", body, "application/json") })
}

// A JSON body that is all nesting ({"a":{"a":... to 128 KB): inspection
// stops at maxJSONDepth, its cost does not grow with the depth.
func BenchmarkDeepJSONBody(b *testing.B) {
	body := strings.Repeat(`{"a":`, (128<<10)/5)
	benchInspect(b, pl1(), func() *http.Request { return browser("POST", "/api", body, "application/json") })
}

// A gzip-compressed JSON body, inflated for inspection.
func BenchmarkGzipJSONPost(b *testing.B) {
	body := `{"order":{"id":"ord_123","items":[{"sku":"A-1","qty":2,"price":19.99},{"sku":"B-2","qty":1,"price":5}],"shipping":{"name":"Siobhán O'Connor","street":"12 Main St.","city":"Dublin","notes":"Leave at door; ring bell 2x"},"coupon":null,"gift":true}}`
	benchInspect(b, pl1(), func() *http.Request {
		r := browser("POST", "/api/orders", gzipped(body), "application/json")
		r.Header.Set("Content-Encoding", "gzip")
		return r
	})
}
