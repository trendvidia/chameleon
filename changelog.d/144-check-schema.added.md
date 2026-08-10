- **`chameleon check-schema` — ask whether a `.proto` is PXF-conformant, without resolving anything** ([#144](https://github.com/trendvidia/chameleon/issues/144)). Every other subcommand that takes `--schema` also needs a chain, layers and an environment, because it is on its way to resolving something. The bind-time rules — PXF-reserved names, `(pxf.key)` / `(pxf.default)` / `(pxf.required)` placement — are a property of a file closure, so they can be answered without any of that:

  ```sh
  chameleon check-schema --schema=config.proto
  # OK    config.proto    4 file(s) checked
  ```

  Non-zero exit and one line per violation otherwise. **The violation names the file that declares it, which need not be the one you passed**: since [protowire-go#71](https://github.com/trendvidia/protowire-go/issues/71) the checks cover the transitive import closure, so a misplaced annotation in a shared `common.proto` fails every schema importing it, whether or not the importing message refers to the offending type. Run this in the repo that *owns* a shared schema — a consumer's resolve is the wrong place to find out, and a schema-only repo has nothing else to run.

  `--message` is optional and does **not** narrow the check; it only additionally asserts the named message exists, in the schema or in anything it imports. The file count on the `OK` line is the closure size rather than the number of files named, so it says how much of the closure a green run actually spoke for.
