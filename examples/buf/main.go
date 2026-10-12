// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

// This program is the one behind the top-level README's quick start:
// it resolves the dev environment of chain.pxf against layers/ into the
// typed myapp.v1.AppConfig generated under gen/go. Run it from this
// directory:
//
//	go run .
//
// gen/go is committed so the example builds without a code-generation
// step; `go generate` refreshes it (needs buf, protoc-gen-go and
// protoc-gen-chameleon-go on PATH — see buf.yaml).
//
//go:generate buf generate
package main

import (
	"fmt"
	"io"
	"log"
	"os"

	"github.com/trendvidia/chameleon"
	"github.com/trendvidia/chameleon/chain"

	myappv1 "github.com/trendvidia/chameleon/examples/buf/gen/go/myapp/v1"
)

func main() {
	if err := run(os.Stdout); err != nil {
		log.Fatal(err)
	}
}

func run(w io.Writer) error {
	// README:quickstart begin
	spec, err := chain.LoadSpec("chain.pxf")
	if err != nil {
		return err
	}

	res, err := chameleon.Load[*myappv1.AppConfig](
		&myappv1.AppConfig{},
		spec,
		os.DirFS("layers"),
		"dev",
	)
	if err != nil {
		return err
	}
	defer res.Destroy()

	cfg := res.Config // typed *myappv1.AppConfig
	fmt.Fprintln(w, "base_url:", cfg.GetBaseUrl())

	// Secrets never sit on cfg as plain strings. The generated accessor
	// reaches the memguard-backed value; a renamed field fails to compile.
	pw := myappv1.AppConfigSecret_DbPassword(res.Secrets)
	if pw == nil {
		return fmt.Errorf("db_password is not set")
	}
	buf, err := pw.Open()
	if err != nil {
		return err
	}
	defer buf.Destroy()
	fmt.Fprintf(w, "db_password: %d bytes\n", len(buf.Bytes()))
	// README:quickstart end
	return nil
}
