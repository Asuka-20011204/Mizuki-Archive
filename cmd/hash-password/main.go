package main

import (
	"fmt"
	"log"
	"os"

	"golang.org/x/crypto/bcrypt"
	"golang.org/x/term"
)

// main 是一次性辅助命令，不启动 Web 服务；从终端安全读取密码并输出 bcrypt 哈希。
func main() {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		log.Fatal("run from an interactive terminal")
	}
	fmt.Fprint(os.Stderr, "管理员密码：")
	password, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		log.Fatal("cannot read password")
	}
	if len(password) < 12 {
		log.Fatal("password must be at least 12 bytes")
	}
	hash, err := bcrypt.GenerateFromPassword(password, bcrypt.DefaultCost)
	for index := range password {
		password[index] = 0
	}
	if err != nil {
		log.Fatal("cannot hash password")
	}
	fmt.Println(string(hash))
}
