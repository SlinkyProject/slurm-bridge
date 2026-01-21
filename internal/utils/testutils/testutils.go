// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package testutils

import (
	"crypto/rand"
	"math/big"
	"os"
	"path/filepath"
)

func GetEnvTestBinary(rootPath string) string {
	basePath := filepath.Join(rootPath, "bin", "k8s")
	entries, err := os.ReadDir(basePath)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if entry.IsDir() {
			return filepath.Join(basePath, entry.Name())
		}
	}
	return ""
}

// REGEX: `[a-z]([-a-z0-9]*[a-z0-9])?`
func GenerateResourceName(length int) string {
	if length > 63 {
		panic("length cannot exceed 63 characters")
	}
	if length < 1 {
		panic("length cannot be less than 1 character")
	}
	const alphaLower = "abcdefghijklmnopqrstuvwxyz"
	const alphaNum = alphaLower + "0123456789"
	const alphaNumSym = "-" + alphaNum
	str := generateRandomString(alphaLower, 1)
	if length > 1 {
		str += generateRandomString(alphaNumSym, length-2)
		str += generateRandomString(alphaNum, 1)
	}
	return str
}

func generateRandomString(charset string, n int) string {
	if n < 1 {
		return ""
	}
	ret := make([]byte, n)
	for i := range n {
		num, err := rand.Int(rand.Reader, big.NewInt(int64(len(charset))))
		if err != nil {
			panic(err)
		}
		ret[i] = charset[num.Int64()]
	}
	return string(ret)
}
