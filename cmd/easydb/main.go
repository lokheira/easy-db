package main

import (
	"fmt"
	"io"
	"strings"
)

func main() {
	fmt.Println("Welcome to use easyDB!")

	var command string

	for {
		fmt.Print("> ")
		_, err := fmt.Scan(&command)
		if err != nil {
			if err == io.EOF {
				fmt.Println("Bye!")
				return
			}
			panic(err)
		}

		if strings.ToLower(command) == "exit" {
			fmt.Println("Bye!")
			break
		}

		fmt.Printf("you have input command: %s\n\n", command)
	}
}
