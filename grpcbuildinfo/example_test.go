package grpcbuildinfo_test

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

	"github.com/Bugs5382/go-buildinfo/grpcbuildinfo"
	"github.com/Bugs5382/go-buildinfo/health"
	"google.golang.org/grpc"
	grpchealth "google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

func ExampleNew() {
	checker := health.New()
	if err := checker.Register(health.Dependency{Name: "postgres", Required: true, Check: func(context.Context) error { return nil }}); err != nil {
		panic(err)
	}
	hs := grpchealth.NewServer()
	bi, err := grpcbuildinfo.New(
		grpcbuildinfo.WithPrefix("acme"),
		grpcbuildinfo.WithChecker(checker),
		grpcbuildinfo.WithHealthServer(hs),
		grpcbuildinfo.WithServices("acme.v1.Things"),
		grpcbuildinfo.WithLivenessService("liveness"),
	)
	if err != nil {
		panic(err)
	}
	srv := grpc.NewServer(
		grpc.ChainUnaryInterceptor(bi.UnaryServerInterceptor()),
		grpc.ChainStreamInterceptor(bi.StreamServerInterceptor()),
	)
	healthpb.RegisterHealthServer(srv, hs)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go bi.Run(ctx)
	fmt.Println("ready:", bi.Update(ctx).Ready)
	// Output: ready: true
}

func ExampleRead() {
	var conn grpc.ClientConnInterface // a connection to another service
	if conn == nil {
		return
	}
	res, err := grpcbuildinfo.Read(context.Background(), conn, "acme")
	if err != nil {
		panic(err)
	}
	fmt.Println(res.Status, res.Version, res.Commit, res.Dependencies["postgres"].State)
}
