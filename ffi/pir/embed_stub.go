//go:build !embed_pir

package pir

// When built without the embed_pir tag, no nf-server binary is bundled.
// The --pir flag and svoted pir subcommands will return a clear error.
var nfServerBinary []byte
