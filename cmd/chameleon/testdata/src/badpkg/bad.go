// Package badpkg exercises the patterns `chameleon lint` should flag.
package badpkg

import "pxfv1"

type AppConfig struct {
	DbPassword *pxfv1.Secret
}

// Read on pointer receiver.
func readPointer(cfg *AppConfig) string {
	return cfg.DbPassword.Value // want `pxfv1.Secret.Value is cleared`
}

// Write on pointer receiver.
func writePointer(cfg *AppConfig) {
	cfg.DbPassword.Value = "won't help — parse.Move clears it" // want `pxfv1.Secret.Value is cleared`
}

// Comparison — the documented footgun pattern.
func panicGuard(cfg *AppConfig) {
	if cfg.DbPassword.Value == "" { // want `pxfv1.Secret.Value is cleared`
		panic("missing")
	}
}

// Direct value access via a temporary.
func via(s *pxfv1.Secret) string {
	return s.Value // want `pxfv1.Secret.Value is cleared`
}

// Value type (not pointer) — still flagged.
func valueAccess(s pxfv1.Secret) string {
	return s.Value // want `pxfv1.Secret.Value is cleared`
}
