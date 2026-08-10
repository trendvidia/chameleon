- **`resolver.NoDecrypterError`** ([#141](https://github.com/trendvidia/chameleon/issues/141)). The concrete error behind `ErrNoDecrypter`, carrying the layer that turned out to be encrypted (`LayerID`), which of the two cases happened (`Disabled` — `WithoutDecrypter` was passed, as opposed to nothing being registered), and the crypt registry failure underneath (`Err`). Reach it with `errors.As`:

  ```go
  var nde *resolver.NoDecrypterError
  if errors.As(err, &nde) && nde.Disabled {
      return fmt.Errorf("layer %q is encrypted, and --decrypter=none was passed", nde.LayerID)
  }
  ```

  Added because a program that re-words this failure for its own users could not do so without repeating chameleon's Go option names to an audience that never typed them — scraping the layer id back out of the message string is not an interface. The chameleon CLI is the first consumer.

  **Nothing changes for callers that do not use it.** `errors.Is` against `ErrNoDecrypter`, `crypt.ErrNoBackend` and `crypt.ErrAmbiguousBackend` matches exactly as before, and both diagnostics are byte-for-byte what they were — pinned verbatim by `TestNoDecrypterError_CarriesTheLayerAndTheCase`.
