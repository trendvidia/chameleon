// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package secret_test

import (
	"fmt"

	"github.com/trendvidia/chameleon/secret"
)

// ExampleSecret_Open shows the canonical Open / Destroy pattern for
// reading a Secret's plaintext within a bounded window. In
// production a Secret comes from the chameleon resolver's
// Resolution.Secrets map; this example uses FromString for
// self-containment.
//
// Every Open call must be paired with a Destroy on the returned
// OpenedBuffer — the underlying memory is mlocked and only the
// post-Destroy state is wiped. Keep the open window narrow.
func ExampleSecret_Open() {
	s := secret.FromString("supersecret", "Postgres primary", "")
	defer s.Destroy()

	buf, err := s.Open()
	if err != nil {
		fmt.Println("open:", err)
		return
	}
	defer buf.Destroy()

	fmt.Printf("hint=%q value=%q\n", s.Hint(), string(buf.Bytes()))
	// Output: hint="Postgres primary" value="supersecret"
}

// ExampleMap_Range iterates every secret in a Map. Useful for
// telemetry, validation passes, or pre-shutdown audits. Range
// snapshots the entries inside the Map's mutex, so it is safe to
// call concurrently with other Map methods — but the snapshot
// reflects state at Range's start.
func ExampleMap_Range() {
	m := secret.NewMap()
	defer m.Destroy()

	m.Set("db_password", secret.FromString("p1", "primary db", ""))
	m.Set("api_token", secret.FromString("t1", "external API", ""))

	// Collect into a sorted slice so the example's output is
	// deterministic — Range itself iterates in arbitrary order.
	paths := make([]string, 0, m.Len())
	m.Range(func(path string, _ *secret.Secret) bool {
		paths = append(paths, path)
		return true
	})
	// Sort for the Output: line.
	for i := 0; i < len(paths); i++ {
		for j := i + 1; j < len(paths); j++ {
			if paths[j] < paths[i] {
				paths[i], paths[j] = paths[j], paths[i]
			}
		}
	}
	for _, p := range paths {
		fmt.Println(p, "->", m.Get(p).Hint())
	}
	// Output:
	// api_token -> external API
	// db_password -> primary db
}
