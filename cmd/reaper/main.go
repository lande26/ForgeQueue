package main

import (
	"fmt"
	"time"
)

func main() {
	fmt.Println("Reaper service started")
	for {
		time.Sleep(1 * time.Second)
	}
}
