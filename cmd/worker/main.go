package main

import (
	"fmt"
	"time"
)

func main() {
	fmt.Println("Worker service started")
	for {
		time.Sleep(1 * time.Second)
	}
}
