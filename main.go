package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/discovery/mdns"
	drouting "github.com/libp2p/go-libp2p/p2p/discovery/routing"
	dutil "github.com/libp2p/go-libp2p/p2p/discovery/util"
	"github.com/multiformats/go-multiaddr"
	"github.com/rivo/tview"
	"golang.org/x/crypto/chacha20poly1305"
)

// ─── Constants ────────────────────────────────────────────────────────────────

const (
	DiscoveryTag    = "p2p-messenger-v1"
	GlobalRoomTopic = "global-chat"
	MsgTypeChat     = "chat"
	MsgTypeJoin     = "join"
	MsgTypeLeave    = "leave"
	MsgTypePing     = "ping"
	PresenceTimeout = 3 * time.Minute
	PingInterval    = 60 * time.Second
)

// Публичные bootstrap ноды libp2p (IPFS)
var bootstrapPeers = []string{
	"/dnsaddr/bootstrap.libp2p.io/p2p/QmNnooDu7bfjPFoTZYxMNLWUQJyrVwtbZg5gBMjTezGAJN",
	"/dnsaddr/bootstrap.libp2p.io/p2p/QmQCU2EcMqAqQPR2i9bChDtGNJchTbq5TbXJJ16u19uLTa",
	"/dnsaddr/bootstrap.libp2p.io/p2p/QmbLHAnMoJPWSCR5Zhtx6BHJX9KiKNN6tpvbUcqanj75Nb",
	"/dnsaddr/bootstrap.libp2p.io/p2p/QmcZf59bWwK5XFi76CZX8cbJ4BhTzzA3gU1ZjYZcYW3dwt",
	"/ip4/104.131.131.82/tcp/4001/p2p/QmaCpDMGvV2BGHeYERUEnRQAwe3N8SzbUtfsmvsqQLuvuJ",
	"/ip4/104.236.179.241/tcp/4001/p2p/QmSoLPppuBtQSGwKDZT2M73ULpjvfd3aZ6ha4oFGL1KrGM",
}

// ─── Logger ───────────────────────────────────────────────────────────────────

var debugMode = false

func debugLog(format string, args ...interface{}) {
	if debugMode {
		fmt.Printf(format+"\n", args...)
	}
}

// ─── E2E Encryption ───────────────────────────────────────────────────────────

func deriveRoomKey(password string) []byte {
	h := sha256.Sum256([]byte(password))
	return h[:]
}

func encryptMsg(plaintext, key []byte) (string, error) {
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	ct := aead.Seal(nonce, nonce, plaintext, nil)
	return base64.StdEncoding.EncodeToString(ct), nil
}

func decryptMsg(b64 string, key []byte) ([]byte, error) {
	ct, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, err
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	ns := aead.NonceSize()
	if len(ct) < ns {
		return nil, fmt.Errorf("ciphertext too short")
	}
	return aead.Open(nil, ct[:ns], ct[ns:], nil)
}

// ─── Identity ─────────────────────────────────────────────────────────────────

func keyPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "identity.key"
	}
	return filepath.Join(home, ".p2pmessenger", "identity.key")
}

func loadOrCreateKey() (crypto.PrivKey, bool, error) {
	path := keyPath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, false, err
	}
	data, err := os.ReadFile(path)
	if err == nil {
		raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(data)))
		if err != nil {
			return nil, false, fmt.Errorf("decode key: %w", err)
		}
		priv, err := crypto.UnmarshalPrivateKey(raw)
		if err != nil {
			return nil, false, fmt.Errorf("unmarshal key: %w", err)
		}
		return priv, false, nil
	}
	priv, _, err := crypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		return nil, false, fmt.Errorf("generate key: %w", err)
	}
	raw, err := crypto.MarshalPrivateKey(priv)
	if err != nil {
		return nil, false, fmt.Errorf("marshal key: %w", err)
	}
	encoded := base64.StdEncoding.EncodeToString(raw)
	if err := os.WriteFile(path, []byte(encoded+"\n"), 0600); err != nil {
		return nil, false, fmt.Errorf("save key: %w", err)
	}
	return priv, true, nil
}

// ─── Message ──────────────────────────────────────────────────────────────────

type Message struct {
	Type      string    `json:"type"`
	From      string    `json:"from"`
	Nickname  string    `json:"nickname"`
	Text      string    `json:"text"`
	Encrypted string    `json:"enc,omitempty"`
	Timestamp time.Time `json:"timestamp"`
	Room      string    `json:"room"`
}

// ─── Room member ──────────────────────────────────────────────────────────────

type roomMember struct {
	Nickname string
	PeerID   peer.ID
	LastSeen time.Time
}

func (m *roomMember) isOnline() bool {
	return time.Since(m.LastSeen) < PresenceTimeout
}

// ─── App ──────────────────────────────────────────────────────────────────────

type App struct {
	host        host.Host
	ps          *pubsub.PubSub
	topics      map[string]*pubsub.Topic
	subs        map[string]*pubsub.Subscription
	mu          sync.Mutex
	nickname    string
	curRoom     string
	rooms       []string
	roomMembers map[string]map[string]*roomMember
	roomKeys    map[string][]byte
	torMode     bool
	torAddr     string
	dht         *dht.IpfsDHT

	tapp         *tview.Application
	chatView     *tview.TextView
	inputField   *tview.InputField
	sidebar      *tview.List
	statusBar    *tview.TextView
	peerCount    int
	resetPending bool
}

type mdnsNotifee struct{ app *App }

func (n *mdnsNotifee) HandlePeerFound(pi peer.AddrInfo) {
	go n.app.host.Connect(context.Background(), pi) //nolint
}

func newApp(ctx context.Context, nickname, torAddr string) (*App, error) {
	// Используем обычный stdout для инициализации, так как TUI еще не запущен
	fmt.Print("Loading identity... ")
	priv, isNew, err := loadOrCreateKey()
	if err != nil {
		return nil, fmt.Errorf("identity: %w", err)
	}
	if isNew {
		fmt.Printf("new identity created (%s)\n", keyPath())
	} else {
		fmt.Println("loaded existing identity")
	}

	opts := []libp2p.Option{libp2p.Identity(priv)}

	if torAddr != "" {
		if err := os.Setenv("ALL_PROXY", "socks5://"+torAddr); err != nil {
			return nil, fmt.Errorf("set proxy env: %w", err)
		}
		opts = append(opts, libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
		fmt.Printf("Tor mode enabled (%s)\n", torAddr)
	}

	fmt.Print("Creating P2P host... ")
	h, err := libp2p.New(opts...)
	if err != nil {
		return nil, fmt.Errorf("create host: %w", err)
	}
	fmt.Println("done")

	fmt.Print("Creating PubSub... ")
	ps, err := pubsub.NewGossipSub(ctx, h)
	if err != nil {
		return nil, fmt.Errorf("create pubsub: %w", err)
	}
	fmt.Println("done")

	app := &App{
		host:        h,
		ps:          ps,
		topics:      make(map[string]*pubsub.Topic),
		subs:        make(map[string]*pubsub.Subscription),
		nickname:    nickname,
		curRoom:     GlobalRoomTopic,
		rooms:       []string{GlobalRoomTopic},
		roomMembers: make(map[string]map[string]*roomMember),
		roomKeys:    make(map[string][]byte),
		torMode:     torAddr != "",
		torAddr:     torAddr,
	}

	fmt.Print("Starting mDNS discovery... ")
	mdnsSvc := mdns.NewMdnsService(h, DiscoveryTag, &mdnsNotifee{app})
	if err := mdnsSvc.Start(); err != nil {
		return nil, fmt.Errorf("mdns: %w", err)
	}
	fmt.Println("done")

	// DHT и discovery в фоне
	go func() {
		time.Sleep(2 * time.Second)

		kd, err := dht.New(ctx, h, dht.Mode(dht.ModeAutoServer))
		if err != nil {
			debugLog("DHT creation error: %v", err)
			return
		}
		app.dht = kd
		rd := drouting.NewRoutingDiscovery(kd)

		// Подключаемся к bootstrap нодам
		for _, addr := range bootstrapPeers {
			go func(addr string) {
				ma, err := multiaddr.NewMultiaddr(addr)
				if err != nil {
					return
				}
				pi, err := peer.AddrInfoFromP2pAddr(ma)
				if err != nil {
					return
				}
				ctxConnect, cancel := context.WithTimeout(ctx, 5*time.Second)
				defer cancel()
				if err := h.Connect(ctxConnect, *pi); err == nil {
					debugLog("Connected to bootstrap: %s", pi.ID.String()[:8])
				}
			}(addr)
		}

		// Bootstrap DHT
		if err := kd.Bootstrap(ctx); err != nil {
			debugLog("DHT bootstrap warning: %v", err)
		}

		// Advertise себя в сети
		go dutil.Advertise(ctx, rd, DiscoveryTag)

		debugLog("DHT initialized")

		// Периодический поиск пиров
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				findCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
				peerCh, err := rd.FindPeers(findCtx, DiscoveryTag)
				cancel()
				if err != nil {
					continue
				}
				for pi := range peerCh {
					if pi.ID != h.ID() && len(pi.Addrs) > 0 {
						go func(pi peer.AddrInfo) {
							connectCtx, connectCancel := context.WithTimeout(ctx, 5*time.Second)
							defer connectCancel()
							h.Connect(connectCtx, pi)
						}(pi)
					}
				}
			}
		}
	}()

	// Мониторинг подключений в фоне
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				peers := h.Network().Peers()
				if len(peers) > 0 {
					debugLog("Connected to %d peers", len(peers))
				}
			}
		}
	}()

	h.Network().Notify(&network.NotifyBundle{
		ConnectedF: func(_ network.Network, conn network.Conn) {
			app.mu.Lock()
			app.peerCount++
			app.mu.Unlock()
			app.updateStatus()
		},
		DisconnectedF: func(_ network.Network, conn network.Conn) {
			app.mu.Lock()
			if app.peerCount > 0 {
				app.peerCount--
			}
			app.mu.Unlock()
			app.updateStatus()
		},
	})

	return app, nil
}

// ─── Room management ──────────────────────────────────────────────────────────

func (a *App) joinRoom(room string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.topics[room]; ok {
		a.curRoom = room
		return nil
	}
	t, err := a.ps.Join(room)
	if err != nil {
		return err
	}
	sub, err := t.Subscribe()
	if err != nil {
		return err
	}
	a.topics[room] = t
	a.subs[room] = sub
	if a.roomMembers[room] == nil {
		a.roomMembers[room] = make(map[string]*roomMember)
	}
	found := false
	for _, r := range a.rooms {
		if r == room {
			found = true
			break
		}
	}
	if !found {
		a.rooms = append(a.rooms, room)
	}
	a.curRoom = room
	go a.readMessages(room, sub)
	return nil
}

func (a *App) publishMsg(msgType, room, text string) error {
	a.mu.Lock()
	t, ok := a.topics[room]
	key := a.roomKeys[room]
	a.mu.Unlock()
	if !ok {
		return fmt.Errorf("not in room %s", room)
	}
	msg := Message{
		Type:      msgType,
		From:      a.host.ID().String(),
		Nickname:  a.nickname,
		Timestamp: time.Now(),
		Room:      room,
	}
	if msgType == MsgTypeChat && key != nil {
		enc, err := encryptMsg([]byte(text), key)
		if err != nil {
			return fmt.Errorf("encrypt: %w", err)
		}
		msg.Encrypted = enc
	} else {
		msg.Text = text
	}
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return t.Publish(context.Background(), b)
}

func (a *App) readMessages(room string, sub *pubsub.Subscription) {
	for {
		m, err := sub.Next(context.Background())
		if err != nil {
			return
		}
		if m.ReceivedFrom == a.host.ID() {
			continue
		}
		var msg Message
		if err := json.Unmarshal(m.Data, &msg); err != nil {
			continue
		}

		a.mu.Lock()
		if a.roomMembers[room] == nil {
			a.roomMembers[room] = make(map[string]*roomMember)
		}
		existing, seen := a.roomMembers[room][msg.From]
		isNewcomer := !seen || !existing.isOnline()
		if msg.Type != MsgTypeLeave {
			a.roomMembers[room][msg.From] = &roomMember{
				Nickname: msg.Nickname,
				PeerID:   m.ReceivedFrom,
				LastSeen: time.Now(),
			}
		}
		a.mu.Unlock()

		switch msg.Type {
		case MsgTypeJoin:
			a.systemMsg(fmt.Sprintf("[green]-> %s[white] joined [cyan]%s", msg.Nickname, room))
			a.updateStatus()
		case MsgTypeLeave:
			a.systemMsg(fmt.Sprintf("[red]<- %s[white] left [cyan]%s", msg.Nickname, room))
			a.mu.Lock()
			delete(a.roomMembers[room], msg.From)
			a.mu.Unlock()
			a.updateStatus()
		case MsgTypePing:
			if isNewcomer {
				a.systemMsg(fmt.Sprintf("[green]* %s[white] online in [cyan]%s", msg.Nickname, room))
				a.updateStatus()
			}
		case MsgTypeChat:
			if isNewcomer {
				a.systemMsg(fmt.Sprintf("[green]-> %s[white] appeared in [cyan]%s", msg.Nickname, room))
				a.updateStatus()
			}
			if msg.Encrypted != "" {
				a.mu.Lock()
				key := a.roomKeys[room]
				a.mu.Unlock()
				if key == nil {
					a.systemMsg(fmt.Sprintf("[red]%s[white] sent encrypted msg — use [green]/key %s <password>", msg.Nickname, room))
					continue
				}
				plain, err := decryptMsg(msg.Encrypted, key)
				if err != nil {
					a.systemMsg(fmt.Sprintf("[red]Failed to decrypt msg from %s — wrong password?", msg.Nickname))
					continue
				}
				msg.Text = string(plain)
			}
			a.appendChat(msg)
		}
	}
}

// ─── Presence ─────────────────────────────────────────────────────────────────

func (a *App) startPresence(ctx context.Context) {
	a.mu.Lock()
	rooms := make([]string, len(a.rooms))
	copy(rooms, a.rooms)
	a.mu.Unlock()
	for _, room := range rooms {
		r := room
		go a.publishMsg(MsgTypeJoin, r, "") //nolint
	}
	ticker := time.NewTicker(PingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			a.mu.Lock()
			rooms2 := make([]string, len(a.rooms))
			copy(rooms2, a.rooms)
			a.mu.Unlock()
			for _, room := range rooms2 {
				r := room
				go a.publishMsg(MsgTypeLeave, r, "") //nolint
			}
			return
		case <-ticker.C:
			a.mu.Lock()
			rooms2 := make([]string, len(a.rooms))
			copy(rooms2, a.rooms)
			a.mu.Unlock()
			for _, room := range rooms2 {
				r := room
				go a.publishMsg(MsgTypePing, r, "") //nolint
			}
		}
	}
}

// ─── TUI ──────────────────────────────────────────────────────────────────────

func (a *App) buildTUI() {
	a.tapp = tview.NewApplication()

	a.chatView = tview.NewTextView().SetDynamicColors(true).SetScrollable(true).SetWordWrap(true)
	a.chatView.SetBorder(true).SetBorderColor(tcell.ColorTeal).
		SetTitle(" Messages ").SetTitleColor(tcell.ColorAqua).
		SetBackgroundColor(tcell.ColorBlack)

	a.inputField = tview.NewInputField().
		SetLabel(" > ").SetLabelColor(tcell.ColorGreen).
		SetFieldWidth(0).SetFieldBackgroundColor(tcell.ColorBlack).
		SetFieldTextColor(tcell.ColorWhite)
	a.inputField.SetBorder(true).SetBorderColor(tcell.ColorTeal).
		SetTitle(" Message — /help for commands ").SetTitleColor(tcell.ColorDimGray)
	a.inputField.SetDoneFunc(func(key tcell.Key) {
		if key == tcell.KeyEnter {
			text := a.inputField.GetText()
			a.inputField.SetText("") // SetText внутри event loop — OK
			go a.handleInput(text)   // всё остальное — в горутине
		}
	})

	a.sidebar = tview.NewList().ShowSecondaryText(true).
		SetHighlightFullLine(true).
		SetSelectedBackgroundColor(tcell.ColorTeal).
		SetSelectedTextColor(tcell.ColorBlack)
	a.sidebar.SetBorder(true).SetBorderColor(tcell.ColorTeal).
		SetTitle(" Rooms ").SetTitleColor(tcell.ColorAqua).
		SetBackgroundColor(tcell.ColorBlack)
	a.sidebar.SetSelectedFunc(func(i int, main, _ string, _ rune) {
		room := strings.TrimPrefix(main, "# ")
		a.mu.Lock()
		a.curRoom = room
		a.mu.Unlock()
		go a.updateStatus() // в горутине — updateStatus вызывает QueueUpdateDraw
	})

	a.statusBar = tview.NewTextView().SetDynamicColors(true)
	a.statusBar.SetBackgroundColor(tcell.ColorDarkSlateGray)

	left := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(a.chatView, 0, 1, false).
		AddItem(a.inputField, 3, 0, true)
	main := tview.NewFlex().
		AddItem(a.sidebar, 22, 0, false).
		AddItem(left, 0, 1, true)
	root := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(a.buildHeader(), 3, 0, false).
		AddItem(main, 0, 1, true).
		AddItem(a.statusBar, 1, 0, false)

	a.tapp.SetRoot(root, true).EnableMouse(true)
	a.tapp.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyTab {
			if a.tapp.GetFocus() == a.inputField {
				a.tapp.SetFocus(a.sidebar)
			} else {
				a.tapp.SetFocus(a.inputField)
			}
			return nil
		}
		return event
	})
}

func (a *App) buildHeader() *tview.TextView {
	v := tview.NewTextView().SetDynamicColors(true).SetTextAlign(tview.AlignCenter)
	v.SetBackgroundColor(tcell.ColorTeal)
	torTag := ""
	if a.torMode {
		torTag = " | Tor ON"
	}
	fmt.Fprintf(v, "\n[black::b]P2P MESSENGER   [dimgray]id: %s[black]   nick: [black::b]%s%s",
		shortID(a.host.ID()), a.nickname, torTag)
	return v
}

func (a *App) updateStatus() {
	if a.tapp == nil {
		return
	}
	a.mu.Lock()
	peers := a.peerCount
	room := a.curRoom
	members := 0
	if a.roomMembers[room] != nil {
		for _, m := range a.roomMembers[room] {
			if m.isOnline() {
				members++
			}
		}
	}
	rooms := make([]string, len(a.rooms))
	copy(rooms, a.rooms)
	rmembers := make(map[string]int)
	for r, mmap := range a.roomMembers {
		cnt := 0
		for _, m := range mmap {
			if m.isOnline() {
				cnt++
			}
		}
		rmembers[r] = cnt
	}
	encrypted := a.roomKeys[room] != nil
	a.mu.Unlock()

	encTag := ""
	if encrypted {
		encTag = "  [green]E2E[white]"
	}
	torTag := ""
	if a.torMode {
		torTag = "  [green]Tor[white]"
	}

	a.tapp.QueueUpdateDraw(func() {
		a.statusBar.Clear()
		fmt.Fprintf(a.statusBar,
			"  [green]*[white] peers:[yellow]%d[white]  room:[cyan]%s[white]  members:[green]%d[white]%s%s  [dimgray]TAB=focus /who=online /help",
			peers, room, members+1, encTag, torTag)
		a.sidebar.Clear()
		for i, r := range rooms {
			cnt := rmembers[r] + 1
			a.sidebar.AddItem("# "+r, fmt.Sprintf("  %d members", cnt), 0, nil)
			if r == room {
				a.sidebar.SetCurrentItem(i)
			}
		}
	})
}

func (a *App) appendChat(msg Message) {
	ts := msg.Timestamp.Format("15:04:05")
	shortPeer := shortID(peer.ID(msg.From))
	nameColor := "[cyan]"
	if msg.From == a.host.ID().String() {
		nameColor = "[green]"
	}
	a.mu.Lock()
	encrypted := a.roomKeys[msg.Room] != nil
	a.mu.Unlock()
	lockTag := ""
	if encrypted {
		lockTag = "[green][E2E][white] "
	}
	a.tapp.QueueUpdateDraw(func() {
		fmt.Fprintf(a.chatView, "[dimgray]%s %s%s%s[white]: %s\n[dimgray]         -> %s[white]\n",
			ts, lockTag, nameColor, msg.Nickname, msg.Text, shortPeer)
		a.chatView.ScrollToEnd()
	})
}

func (a *App) systemMsg(text string) {
	if a.tapp == nil {
		return
	}
	a.tapp.QueueUpdateDraw(func() {
		fmt.Fprintf(a.chatView, "[yellow]  * %s[white]\n", text)
		a.chatView.ScrollToEnd()
	})
}

// ─── Input ────────────────────────────────────────────────────────────────────

func (a *App) handleInput(raw string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return
	}
	if strings.HasPrefix(raw, "/") {
		a.handleCommand(raw)
		return
	}
	a.mu.Lock()
	wasPending := a.resetPending
	a.resetPending = false
	a.mu.Unlock()
	if wasPending {
		a.tapp.QueueUpdateDraw(func() {
			a.inputField.SetBorderColor(tcell.ColorTeal).
				SetTitle(" Message — /help for commands ")
		})
		a.systemMsg("Reset cancelled.")
	}
	msg := Message{
		Type: MsgTypeChat, From: a.host.ID().String(),
		Nickname: a.nickname, Text: raw,
		Timestamp: time.Now(), Room: a.curRoom,
	}
	a.appendChat(msg) // показываем сразу, не ждём сети
	room := a.curRoom
	go func() { // publish в горутине — не блокируем TUI
		if err := a.publishMsg(MsgTypeChat, room, raw); err != nil {
			a.systemMsg("send error: " + err.Error())
		}
	}()
}

func (a *App) handleCommand(raw string) {
	parts := strings.Fields(raw)
	cmd := parts[0]

	if cmd != "/reset" {
		a.mu.Lock()
		wasPending := a.resetPending
		a.resetPending = false
		a.mu.Unlock()
		if wasPending {
			a.tapp.QueueUpdateDraw(func() {
				a.inputField.SetBorderColor(tcell.ColorTeal).
					SetTitle(" Message — /help for commands ")
			})
			a.systemMsg("Reset cancelled.")
			if cmd != "/clear" {
				return
			}
		}
	}

	switch cmd {

	case "/help":
		a.tapp.QueueUpdateDraw(func() {
			fmt.Fprint(a.chatView, "\n[cyan]Commands:[white]\n")
			fmt.Fprint(a.chatView, "  [green]/who[white]              — who is online in current room\n")
			fmt.Fprint(a.chatView, "  [green]/join <room>[white]      — join or create a room\n")
			fmt.Fprint(a.chatView, "  [green]/switch <room>[white]    — switch active room\n")
			fmt.Fprint(a.chatView, "  [green]/rooms[white]            — list joined rooms\n")
			fmt.Fprint(a.chatView, "  [green]/peers[white]            — show direct P2P connections\n")
			fmt.Fprint(a.chatView, "  [green]/id[white]               — show your Peer ID and addresses\n")
			fmt.Fprint(a.chatView, "  [green]/nick <name>[white]      — change nickname\n")
			fmt.Fprint(a.chatView, "  [green]/connect <addr>[white]   — manually connect via multiaddr\n")
			fmt.Fprint(a.chatView, "  [green]/key <password>[white]   — enable E2E encryption in current room\n")
			fmt.Fprint(a.chatView, "  [green]/unkey[white]            — disable encryption in current room\n")
			fmt.Fprint(a.chatView, "  [green]/lock[white]             — show encryption status of rooms\n")
			fmt.Fprint(a.chatView, "  [green]/clear[white]            — clear chat screen\n")
			fmt.Fprint(a.chatView, "  [green]/reset[white]            — delete identity.key (confirm twice)\n")
			fmt.Fprint(a.chatView, "  [green]/quit[white]             — exit\n\n")
			a.chatView.ScrollToEnd()
		})

	case "/who":
		a.mu.Lock()
		room := a.curRoom
		mmap := a.roomMembers[room]
		type entry struct {
			nick string
			ago  time.Duration
			pid  string
		}
		var online, offline []entry
		for pidStr, m := range mmap {
			ago := time.Since(m.LastSeen).Truncate(time.Second)
			e := entry{m.Nickname, ago, shortID(peer.ID(pidStr))}
			if m.isOnline() {
				online = append(online, e)
			} else {
				offline = append(offline, e)
			}
		}
		myNick := a.nickname
		a.mu.Unlock()
		sort.Slice(online, func(i, j int) bool { return online[i].nick < online[j].nick })
		sort.Slice(offline, func(i, j int) bool { return offline[i].nick < offline[j].nick })
		a.tapp.QueueUpdateDraw(func() {
			fmt.Fprintf(a.chatView, "\n[cyan]Members of room [cyan::b]%s[white]:\n", room)
			fmt.Fprintf(a.chatView, "  [green]*[white] [green::b]%s[white] [dimgray](you)[white]\n", myNick)
			for _, e := range online {
				fmt.Fprintf(a.chatView, "  [green]*[white] [green]%s[white]  [dimgray]%s  active %s ago[white]\n", e.nick, e.pid, e.ago)
			}
			if len(offline) > 0 {
				fmt.Fprint(a.chatView, "[dimgray]  -- possibly offline --[white]\n")
				for _, e := range offline {
					fmt.Fprintf(a.chatView, "  [red]o[white] [dimgray]%s  %s  %s ago[white]\n", e.nick, e.pid, e.ago)
				}
			}
			if len(online) == 0 && len(offline) == 0 {
				fmt.Fprint(a.chatView, "  [dimgray]No one else yet[white]\n")
			}
			fmt.Fprintln(a.chatView)
			a.chatView.ScrollToEnd()
		})

	case "/join":
		if len(parts) < 2 {
			a.systemMsg("usage: /join <room-name>")
			return
		}
		room := parts[1]
		if err := a.joinRoom(room); err != nil {
			a.systemMsg("join error: " + err.Error())
			return
		}
		go a.publishMsg(MsgTypeJoin, room, "") //nolint
		a.systemMsg(fmt.Sprintf("Joined room [cyan]%s", room))
		a.updateStatus()

	case "/switch":
		if len(parts) < 2 {
			a.systemMsg("usage: /switch <room-name>")
			return
		}
		room := parts[1]
		a.mu.Lock()
		_, ok := a.topics[room]
		a.mu.Unlock()
		if !ok {
			a.systemMsg(fmt.Sprintf("Not in [cyan]%s[white]. Use /join first.", room))
			return
		}
		a.mu.Lock()
		a.curRoom = room
		a.mu.Unlock()
		a.systemMsg(fmt.Sprintf("Switched to [cyan]%s", room))
		a.updateStatus()

	case "/rooms":
		a.mu.Lock()
		rooms := make([]string, len(a.rooms))
		copy(rooms, a.rooms)
		cur := a.curRoom
		rmembers := make(map[string]int)
		for r, mmap := range a.roomMembers {
			cnt := 0
			for _, m := range mmap {
				if m.isOnline() {
					cnt++
				}
			}
			rmembers[r] = cnt
		}
		a.mu.Unlock()
		a.tapp.QueueUpdateDraw(func() {
			fmt.Fprint(a.chatView, "\n[cyan]Your rooms:[white]\n")
			for _, r := range rooms {
				mark := "  "
				if r == cur {
					mark = "[green]> "
				}
				fmt.Fprintf(a.chatView, "%s[white]%s  [dimgray](%d members)[white]\n", mark, r, rmembers[r]+1)
			}
			fmt.Fprintln(a.chatView)
			a.chatView.ScrollToEnd()
		})

	case "/peers":
		peers := a.host.Network().Peers()
		a.tapp.QueueUpdateDraw(func() {
			if len(peers) == 0 {
				fmt.Fprint(a.chatView, "[yellow]  * No direct P2P connections yet[white]\n")
				a.chatView.ScrollToEnd()
				return
			}
			fmt.Fprintf(a.chatView, "\n[cyan]Direct P2P connections (%d):[white]\n", len(peers))
			a.mu.Lock()
			for _, p := range peers {
				nick := "unknown"
				for _, mmap := range a.roomMembers {
					if m, ok := mmap[p.String()]; ok {
						nick = m.Nickname
						break
					}
				}
				fmt.Fprintf(a.chatView, "  [yellow]%s[white]  %s\n", shortID(p), nick)
			}
			a.mu.Unlock()
			fmt.Fprintln(a.chatView)
			a.chatView.ScrollToEnd()
		})

	case "/nick":
		if len(parts) < 2 {
			a.systemMsg("usage: /nick <new-name>")
			return
		}
		a.mu.Lock()
		a.nickname = parts[1]
		a.mu.Unlock()
		a.systemMsg(fmt.Sprintf("Nickname changed to [green]%s", parts[1]))

	case "/id":
		a.tapp.QueueUpdateDraw(func() {
			id := a.host.ID().String()
			addrs := a.host.Addrs()
			fmt.Fprint(a.chatView, "\n[teal]+--------------------------------------------------+[white]\n")
			fmt.Fprint(a.chatView, "[teal]|[white]              YOUR IDENTITY                    [teal]|[white]\n")
			fmt.Fprint(a.chatView, "[teal]+--------------------------------------------------+[white]\n")
			fmt.Fprintf(a.chatView, "[teal]|[yellow] %s\n", id)
			fmt.Fprint(a.chatView, "[teal]+--------------------------------------------------+[white]\n")
			fmt.Fprint(a.chatView, "[teal]|[white] Addresses for /connect:                      [teal]|[white]\n")
			for _, addr := range addrs {
				fmt.Fprintf(a.chatView, "[teal]|[cyan]  %s/p2p/%s\n", addr, id)
			}
			fmt.Fprintf(a.chatView, "[teal]+--------------------------------------------------+[white]\n")
			fmt.Fprintf(a.chatView, "[teal]|[dimgray] Key: %s\n", keyPath())
			fmt.Fprint(a.chatView, "[teal]+--------------------------------------------------+[white]\n\n")
			a.chatView.ScrollToEnd()
		})

	case "/key":
		if len(parts) < 2 {
			a.systemMsg("usage: /key <password>  or  /key <room> <password>")
			return
		}
		var room, password string
		if len(parts) >= 3 {
			room = parts[1]
			password = strings.Join(parts[2:], " ")
		} else {
			a.mu.Lock()
			room = a.curRoom
			a.mu.Unlock()
			password = strings.Join(parts[1:], " ")
		}
		key := deriveRoomKey(password)
		a.mu.Lock()
		a.roomKeys[room] = key
		a.mu.Unlock()
		a.tapp.QueueUpdateDraw(func() {
			fmt.Fprintf(a.chatView, "\n[green]E2E encryption enabled for [cyan]%s[white]\n", room)
			fmt.Fprint(a.chatView, "[dimgray]  Algorithm: XChaCha20-Poly1305\n")
			fmt.Fprint(a.chatView, "  Key derived from password via SHA-256\n")
			fmt.Fprint(a.chatView, "  Relay peers see only encrypted blob\n")
			fmt.Fprint(a.chatView, "  All room members must use the same password[white]\n\n")
			a.chatView.ScrollToEnd()
		})

	case "/unkey":
		a.mu.Lock()
		room := a.curRoom
		_, had := a.roomKeys[room]
		delete(a.roomKeys, room)
		a.mu.Unlock()
		if had {
			a.systemMsg(fmt.Sprintf("[yellow]Encryption removed from [cyan]%s[white].", room))
		} else {
			a.systemMsg(fmt.Sprintf("Room [cyan]%s[white] was not encrypted.", room))
		}

	case "/lock":
		a.mu.Lock()
		rooms := make([]string, len(a.rooms))
		copy(rooms, a.rooms)
		keys := make(map[string]bool)
		for r, k := range a.roomKeys {
			keys[r] = k != nil
		}
		a.mu.Unlock()
		a.tapp.QueueUpdateDraw(func() {
			fmt.Fprint(a.chatView, "\n[cyan]Encryption status:[white]\n")
			for _, r := range rooms {
				if keys[r] {
					fmt.Fprintf(a.chatView, "  [green][E2E] %s[white]  — encrypted\n", r)
				} else {
					fmt.Fprintf(a.chatView, "  [yellow][---] %s[white]  — plaintext\n", r)
				}
			}
			fmt.Fprintln(a.chatView)
			a.chatView.ScrollToEnd()
		})

	case "/clear":
		a.tapp.QueueUpdateDraw(func() {
			a.chatView.Clear()
			fmt.Fprint(a.chatView, "[dimgray]  -- screen cleared --[white]\n")
		})

	case "/reset":
		a.mu.Lock()
		pending := a.resetPending
		a.mu.Unlock()
		if !pending {
			a.mu.Lock()
			a.resetPending = true
			a.mu.Unlock()
			a.tapp.QueueUpdateDraw(func() {
				fmt.Fprint(a.chatView, "\n[red]+--------------------------------------------------+[white]\n")
				fmt.Fprint(a.chatView, "[red]|[white]  WARNING: RESET IDENTITY                      [red]|[white]\n")
				fmt.Fprint(a.chatView, "[red]+--------------------------------------------------+[white]\n")
				fmt.Fprintf(a.chatView, "[red]|[yellow]  File: %s\n", keyPath())
				fmt.Fprint(a.chatView, "[red]|[white]  Peer ID will change permanently.              [red]|[white]\n")
				fmt.Fprint(a.chatView, "[red]|[green]  Type /reset again[white] to confirm.                [red]|[white]\n")
				fmt.Fprint(a.chatView, "[red]|[dimgray]  Any other command cancels.                    [red]|[white]\n")
				fmt.Fprint(a.chatView, "[red]+--------------------------------------------------+[white]\n\n")
				a.chatView.ScrollToEnd()
				a.inputField.SetBorderColor(tcell.ColorRed).
					SetTitle(" WARNING: /reset again to confirm, any command to cancel ")
			})
		} else {
			a.mu.Lock()
			a.resetPending = false
			a.mu.Unlock()
			if err := os.Remove(keyPath()); err != nil && !os.IsNotExist(err) {
				a.systemMsg(fmt.Sprintf("[red]Error deleting key: %v", err))
				return
			}
			a.tapp.QueueUpdateDraw(func() {
				a.inputField.SetBorderColor(tcell.ColorTeal).
					SetTitle(" Message — /help for commands ")
				fmt.Fprint(a.chatView, "\n[green]identity.key deleted.[white]\n")
				fmt.Fprint(a.chatView, "[dimgray]Restart to generate a new Peer ID.[white]\n\n")
				a.chatView.ScrollToEnd()
			})
			go func() {
				time.Sleep(2 * time.Second)
				a.tapp.Stop()
			}()
		}

	case "/connect":
		if len(parts) < 2 {
			a.systemMsg("usage: /connect <multiaddr>")
			return
		}
		ma, err := multiaddr.NewMultiaddr(parts[1])
		if err != nil {
			a.systemMsg("invalid address: " + err.Error())
			return
		}
		pi, err := peer.AddrInfoFromP2pAddr(ma)
		if err != nil {
			a.systemMsg("parse error: " + err.Error())
			return
		}
		go func() {
			if err := a.host.Connect(context.Background(), *pi); err != nil {
				a.systemMsg("connect failed: " + err.Error())
				return
			}
			a.systemMsg(fmt.Sprintf("Connected to [green]%s", shortID(pi.ID)))
		}()

	case "/quit":
		a.tapp.Stop()

	default:
		a.systemMsg(fmt.Sprintf("Unknown command: %s  (try /help)", cmd))
	}
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func shortID(id peer.ID) string {
	s := id.String()
	if len(s) <= 12 {
		return s
	}
	return s[:6] + "..." + s[len(s)-6:]
}

// ─── Main ─────────────────────────────────────────────────────────────────────

func main() {
	torAddr := flag.String("tor", "", "Tor SOCKS5 address (e.g. 127.0.0.1:9050)")
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
	if err := app.joinRoom(GlobalRoomTopic); err != nil {
		fmt.Fprintf(os.Stderr, "join room: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("done")

	go app.startPresence(ctx)

	fmt.Print("Building TUI interface... ")
	app.buildTUI()
	fmt.Println("done")

	// Больше НЕ перенаправляем stdout в /dev/null — TUI должен рисовать!
	// Debug-логи всё равно печатаются только при debugMode = true.

	// Запускаем TUI (с отладкой)
	fmt.Println("=== TUI RUN START ===")
	if err := app.tapp.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "TUI error: %v\n", err)
	}
	fmt.Println("=== TUI RUN ENDED ===")

	
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