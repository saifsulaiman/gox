package main

import "fmt"

type User struct {
	Name   string
	Friend *User
}

var cached *User

func localGreeting(name string) string {
	user := &User{Name: name}
	return "hello " + user.Name
}

func newUser(name string) *User {
	return &User{Name: name}
}

func cacheUser(name string) {
	cached = &User{Name: name}
}

func main() {
	fmt.Println(localGreeting("Gopher"), newUser("GOX").Name)
	cacheUser("cached")
}
