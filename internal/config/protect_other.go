//go:build !windows

package config

// Development builds on other systems keep the token as it is: the file is
// already readable only by its owner (0600).
func protect(plain []byte) ([]byte, error) { return plain, nil }

func unprotect(sealed []byte) ([]byte, error) { return sealed, nil }
