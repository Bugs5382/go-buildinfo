package httpbuildinfo_test

/*
MIT License

Copyright (c) 2026 Shane

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE USE OR PERFORMANCE OF THIS SOFTWARE.
*/

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"

	"github.com/Bugs5382/go-buildinfo/health"
	"github.com/Bugs5382/go-buildinfo/httpbuildinfo"
)

func ExampleNew() {
	checker := health.New()
	if err := checker.Register(health.Dependency{
		Name:     "opa",
		Required: true,
		Check:    func(context.Context) error { return nil },
	}); err != nil {
		panic(err)
	}
	h, err := httpbuildinfo.New(httpbuildinfo.WithPrefix("acme"), httpbuildinfo.WithChecker(checker))
	if err != nil {
		panic(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/readyz", h.Readyz())
	mux.Handle("/livez", h.Livez())

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	fmt.Println(rec.Code, rec.Header().Get("Acme-Depstate-Opa"))
	// Output: 200 ok
}
