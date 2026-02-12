// Package elgamal implements additively homomorphic El Gamal encryption
// over the Pallas curve using the mikelodder7/curvey library.
//
// It provides keypair generation, encryption/decryption, homomorphic
// ciphertext addition, baby-step giant-step discrete log recovery,
// and Chaum-Pedersen DLEQ proofs for correct decryption.
package elgamal
