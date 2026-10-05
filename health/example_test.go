package health_test

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
	"time"

	"github.com/Bugs5382/go-buildinfo/health"
)

func ExampleChecker() {
	c := health.New(health.WithTTL(5*time.Second), health.WithTimeout(2*time.Second))
	if err := c.Register(
		health.Dependency{
			Name:     "postgres",
			Required: true,
			Check:    func(context.Context) error { return nil },
			Version:  func(context.Context) (string, error) { return "17.2", nil },
		},
		health.Dependency{
			Name:  "cache",
			Check: func(context.Context) error { return fmt.Errorf("dial: %w", context.DeadlineExceeded) },
		},
	); err != nil {
		panic(err)
	}
	r := c.Report(context.Background())
	fmt.Println(r.Status, r.Ready)
	for _, d := range r.Dependencies {
		fmt.Println(d.Name, d.State, d.Error, d.Version)
	}
	// Output:
	// degraded true
	// postgres ok  17.2
	// cache degraded timeout
}
