// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Command htpasswd prints an htpasswd line with a bcrypt hash, the only
// format the distribution registry accepts, for hack/e2e/components/registry.sh:
//
//	htpasswd USER < password
package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: htpasswd USER < password")
		os.Exit(2)
	}
	pw, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && pw == "" {
		fmt.Fprintln(os.Stderr, "read password:", err)
		os.Exit(1)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(strings.TrimRight(pw, "\r\n")), bcrypt.DefaultCost)
	if err != nil {
		fmt.Fprintln(os.Stderr, "hash:", err)
		os.Exit(1)
	}
	fmt.Printf("%s:%s\n", os.Args[1], hash)
}
