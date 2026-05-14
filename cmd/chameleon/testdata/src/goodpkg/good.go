// Package goodpkg exercises patterns `chameleon lint` should NOT flag.
package goodpkg

import "pxfv1"

type AppConfig struct {
	DbPassword *pxfv1.Secret
}

// Reading the metadata fields is fine — they aren't cleared.
func readHint(cfg *AppConfig) string {
	return cfg.DbPassword.Hint
}

func readFingerprint(cfg *AppConfig) string {
	return cfg.DbPassword.Fingerprint
}

// Accessing .Value on a non-Secret type (Other.Value) is fine — only
// pxfv1.Secret.Value triggers the linter.
type Other struct {
	Value string
}

func readOtherValue(o Other) string {
	return o.Value
}

// Helper methods on Secret are fine (the linter only flags .Value).
// Even if Secret had a Hint() method, accessing it would be fine.
