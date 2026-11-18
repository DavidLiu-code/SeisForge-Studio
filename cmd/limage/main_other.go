//go:build !windows

package main

import "fmt"

func main() {
	fmt.Println("SeisForge Studio is built for Windows. Use GOOS=windows GOARCH=amd64.")
}
