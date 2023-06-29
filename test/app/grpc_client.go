package main

import (
	"encoding/hex"
	"fmt"
	"os"
)

var grpcAddr = "tcp://localhost:36656"

func main() {
	args := os.Args
	if len(args) == 1 {
		fmt.Println("Must enter a transaction to send (hex)")
		os.Exit(1)
	}
	tx := args[1]
	_, err := hex.DecodeString(tx)
	if err != nil {
		fmt.Println("Invalid hex", err)
		os.Exit(1)
	}
}
