- **`protoc-gen-chameleon-go` carries the `.proto` file's leading comments
  into the sidecar**, above its generated-code marker, as `protoc-gen-go`
  does for the `.pb.go` beside it. A `.proto` that opens with a license
  header now gives a `.chameleon.go` that opens with it too. A `.proto`
  with no leading comments generates what it did before.

  With that, every Go file in the repository carries its
  `SPDX-License-Identifier: MIT` header: the analyzer fixtures under
  `cmd/chameleon/testdata` and the example's generated sidecar were the
  five without one.
