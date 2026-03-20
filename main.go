package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"p2p-messenger/internal/domain/chat"
)

func main() {
	torAddr := flag.String("tor", "", fmt.Sprintf("Tor SOCKS5 address (e.g. %s)", torDefaultAddr()))
	debug := flag.Bool("debug", false, "Enable debug logging")
	flag.Parse()

	debugMode = *debug

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	nickname := askNickname()
	fmt.Println("Starting P2P Messenger...")

	app, err := newApp(ctx, nickname, *torAddr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Print("Joining global chat room... ")
	if err := app.joinRoom(chat.GlobalRoomName); err != nil {
		fmt.Fprintf(os.Stderr, "join room: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("done")

	go app.startPresence(ctx)

	fmt.Print("Building TUI interface... ")
	app.buildTUI()
	fmt.Println("done")

	if err := app.tapp.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "TUI error: %v\n", err)
	}
}

func askNickname() string {
	reader := bufio.NewReader(os.Stdin)
	fmt.Print("Enter nickname: ")
	nick, _ := reader.ReadString('\n')
	nick = strings.TrimSpace(nick)
	if nick == "" {
		nick = fmt.Sprintf("user-%d", time.Now().Unix()%10000)
	}
	return nick
}
