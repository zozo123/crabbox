// testprog is compiled to GOOS=wasip1 GOARCH=wasm at test time to exercise the
// engine end-to-end: it echoes argv, reads one env var, optionally reads a
// mounted file, and exits with a requested code.
//
//	args:        printed space-joined to stdout, prefixed "args:"
//	env GREETEE: printed as "hello:<value>"
//	env READ:    if set, read that path and print "file:<contents>" (or "fileerr:<err>")
//	env EXIT:    integer exit code (default 0)
package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

func main() {
	fmt.Printf("args:%s\n", strings.Join(os.Args[1:], " "))
	fmt.Printf("hello:%s\n", os.Getenv("GREETEE"))
	if p := os.Getenv("READ"); p != "" {
		if b, err := os.ReadFile(p); err != nil {
			fmt.Printf("fileerr:%v\n", err)
		} else {
			fmt.Printf("file:%s\n", strings.TrimSpace(string(b)))
		}
	}
	code := 0
	if c := os.Getenv("EXIT"); c != "" {
		if n, err := strconv.Atoi(c); err == nil {
			code = n
		}
	}
	os.Exit(code)
}
