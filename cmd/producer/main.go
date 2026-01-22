package main

import (
	"fmt"
	"time"
)

func main() {
	fmt.Println("Producer service started")
	for {
		time.Sleep(1 * time.Second)
	}
}
